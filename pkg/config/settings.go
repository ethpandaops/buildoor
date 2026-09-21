package config

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// Source identifies which layer currently wins for a setting.
const (
	SourceDefault = "default"
	SourceCLI     = "cli"
	SourceUI      = "ui"
)

// keyState holds the in-memory 3-way state for one setting key.
type keyState struct {
	hasCLI   bool
	cliValue any
	cliSeq   int64
	hasUI    bool
	uiValue  any
	uiSeq    int64
}

// Service is the central authority for buildoor's mutable runtime configuration.
// It owns the effective Config every module reads and is the single writer,
// layering three sources: hardcoded defaults < CLI-supplied < UI override. CLI
// and UI are resolved by recency (a monotonic seq), not a fixed priority: a CLI
// value that changed since the last run wins over an older UI override, while an
// unchanged CLI flag lets a newer UI override win. UI overrides persist across
// restarts via the optional state-db. The effective Config is the same pointer
// handed to every module, so writes (applied in place under the service lock)
// are observed live by all readers.
type Service struct {
	log          logrus.FieldLogger
	store        *db.Database
	fields       []Field
	byKey        map[string]Field
	defaults     *Config       // pristine, slot-adjusted defaults — the floor
	effective    *Config       // shared config; mutated in place
	slotDuration time.Duration // 0 = unknown; skips the reveal-time upper bound check

	mu          sync.Mutex
	seq         int64
	keyState    map[string]*keyState
	subscribers []func()
}

// New constructs the settings service.
//
//   - effective is the resolved operator config (defaults + flags/env/file,
//     already slot-adjusted); it becomes the shared config modules read and is
//     mutated in place to apply overrides.
//   - defaults is a pristine, slot-adjusted default Config used as the floor.
//   - supplied maps each field key to whether the operator explicitly provided
//     it (viper.IsSet); only supplied keys form the CLI layer.
//   - slotDuration is the network's slot duration, used to bound
//     reveal-relative timing overrides; pass 0 if not yet known.
//   - store is the optional state-db (may be disabled).
func NewService(
	effective, defaults *Config, supplied map[string]bool, slotDuration time.Duration,
	store *db.Database, log logrus.FieldLogger,
) (*Service, error) {
	s := &Service{
		log:          log.WithField("module", "settings"),
		store:        store,
		fields:       Fields(),
		defaults:     defaults,
		effective:    effective,
		slotDuration: slotDuration,
		keyState:     make(map[string]*keyState),
	}

	s.byKey = make(map[string]Field, len(s.fields))
	for _, f := range s.fields {
		s.byKey[f.Key] = f
	}

	rows, err := store.GetSettings()
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	rowByKey := make(map[string]db.SettingRow, len(rows))
	for _, r := range rows {
		rowByKey[r.Key] = r
	}

	// Pass 1: load persisted UI state and find the high-water seq mark so any
	// newly-allocated CLI-change seq is guaranteed greater than every stored one.
	for _, f := range s.fields {
		ks := &keyState{}
		s.keyState[f.Key] = ks

		row, ok := rowByKey[f.Key]
		if !ok {
			continue
		}

		if row.CLISeq > s.seq {
			s.seq = row.CLISeq
		}

		if row.UISeq > s.seq {
			s.seq = row.UISeq
		}

		if row.UISeq > 0 && row.UIValue.Valid {
			if v, derr := f.Decode(json.RawMessage(row.UIValue.String)); derr == nil {
				ks.hasUI = true
				ks.uiValue = v
				ks.uiSeq = row.UISeq
			} else {
				s.log.WithError(derr).WithField("key", f.Key).Warn("ignoring undecodable ui override")
			}
		}
	}

	// Pass 2: reconcile the CLI layer against what the operator supplied now.
	// Every changed row is batched into a single transaction below rather
	// than persisted one key at a time, so a crash or a transient state-db
	// failure mid-pass can never leave only a prefix of this reconciliation
	// durable.
	now := time.Now().UnixMilli()
	changedRows := make([]db.SettingRow, 0, len(s.fields))

	for _, f := range s.fields {
		ks := s.keyState[f.Key]
		row := rowByKey[f.Key]

		var storedCLI any

		storedHasCLI := row.CLISeq > 0 && row.CLIValue.Valid
		if storedHasCLI {
			if v, derr := f.Decode(json.RawMessage(row.CLIValue.String)); derr == nil {
				storedCLI = v
			} else {
				storedHasCLI = false
			}
		}

		isSupplied := supplied[f.Key]
		changed := false

		switch {
		case isSupplied:
			suppliedVal := f.Get(s.effective)
			if !storedHasCLI || !f.Equal(suppliedVal, storedCLI) {
				// New or changed operator value — counts as a fresh write.
				ks.hasCLI = true
				ks.cliValue = suppliedVal
				ks.cliSeq = s.nextSeq()
				changed = true
			} else {
				ks.hasCLI = true
				ks.cliValue = storedCLI
				ks.cliSeq = row.CLISeq
			}
		case storedHasCLI:
			// Flag was removed — operator no longer asserts it; drop the layer.
			ks.hasCLI = false
			changed = true
		}

		if changed {
			built, err := buildSettingRow(f, ks, SourceCLI, now)
			if err != nil {
				return nil, fmt.Errorf("encode %q: %w", f.Key, err)
			}

			changedRows = append(changedRows, built)
		}
	}

	if err := store.PutSettings(changedRows); err != nil {
		return nil, fmt.Errorf("persist cli settings: %w", err)
	}

	s.recompute()

	return s, nil
}

// Load returns the effective  The returned pointer is the shared config
// modules read; callers must treat it as read-only.
func (s *Service) Load() *Config {
	return s.effective
}

// OnChange registers a callback invoked (outside the service lock) after every
// applied change. Used to trigger module-side resets and re-reads.
func (s *Service) OnChange(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subscribers = append(s.subscribers, fn)
}

// Set applies a single UI override.
func (s *Service) Set(key string, raw json.RawMessage, actor string) error {
	return s.SetMany(map[string]json.RawMessage{key: raw}, actor)
}

// SetMany applies a batch of UI overrides atomically: all values are
// validated and decoded first, then checked together against the timing
// invariants (§ValidateTimingBounds) the batch would produce, then persisted
// in one state-db transaction, and only once that durably commits (or
// persistence is disabled) applied to the effective config and recomputed,
// before subscribers are notified once. Persistence failing anywhere in the
// batch leaves both the in-memory state and the state-db exactly as they
// were before the call — a caller that observes an error can rely on nothing
// having changed, instead of the previous behaviour of always reporting
// success while some or all of the batch had silently failed to persist.
func (s *Service) SetMany(updates map[string]json.RawMessage, actor string) error {
	s.mu.Lock()

	decoded := make(map[string]any, len(updates))

	for key, raw := range updates {
		f, ok := s.byKey[key]
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("unknown setting %q", key)
		}

		v, err := f.Decode(raw)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("decode %q: %w", key, err)
		}

		if err := validateValue(key, v); err != nil {
			s.mu.Unlock()
			return err
		}

		decoded[key] = v
	}

	// Validate the batch's cross-field / slot-relative timing invariants
	// against what the effective config would become if it commits: apply it
	// to a scratch copy first, leaving fields the batch doesn't touch at
	// their current effective value.
	scratch := *s.effective
	for key, v := range decoded {
		if err := s.byKey[key].Set(&scratch, v); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("apply %q: %w", key, err)
		}
	}

	if err := ValidateTimingBounds(&scratch, s.slotDuration); err != nil {
		s.mu.Unlock()
		return err
	}

	// Stage the new key state and build every row up front: nothing is
	// applied to s.keyState/s.effective until the whole batch durably
	// persists.
	now := time.Now().UnixMilli()
	rows := make([]db.SettingRow, 0, len(decoded))
	staged := make(map[string]*keyState, len(decoded))

	for key, v := range decoded {
		next := *s.keyState[key]
		next.hasUI = true
		next.uiValue = v
		next.uiSeq = s.nextSeq()

		row, err := buildSettingRow(s.byKey[key], &next, actor, now)
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("encode %q: %w", key, err)
		}

		rows = append(rows, row)
		staged[key] = &next
	}

	if err := s.store.PutSettings(rows); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("persist settings: %w", err)
	}

	for key, next := range staged {
		s.keyState[key] = next
	}

	s.recompute()

	subs := make([]func(), len(s.subscribers))
	copy(subs, s.subscribers)
	s.mu.Unlock()

	for _, fn := range subs {
		fn()
	}

	return nil
}

// recompute rebuilds the effective config in place: each registered field is set
// to the highest-seq layer present (defaults are the seq-0 floor). Must hold mu.
func (s *Service) recompute() {
	for _, f := range s.fields {
		ks := s.keyState[f.Key]
		val := f.Get(s.defaults)
		winSeq := int64(0)

		if ks.hasCLI && ks.cliSeq > winSeq {
			val = ks.cliValue
			winSeq = ks.cliSeq
		}

		if ks.hasUI && ks.uiSeq > winSeq {
			val = ks.uiValue
		}

		if err := f.Set(s.effective, val); err != nil {
			s.log.WithError(err).WithField("key", f.Key).Error("failed to apply setting")
		}
	}
}

// nextSeq allocates a monotonic sequence number. Must hold mu.
func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

// buildSettingRow builds the full 3-way persisted row for a key from its
// keyState, without writing anything: callers batch rows from several keys
// into a single db.Database.PutSettings transaction.
func buildSettingRow(f Field, ks *keyState, actor string, updatedAt int64) (db.SettingRow, error) {
	row := db.SettingRow{
		Key:       f.Key,
		UpdatedAt: updatedAt,
		Actor:     actor,
	}

	if ks.hasCLI {
		b, err := f.Encode(ks.cliValue)
		if err != nil {
			return db.SettingRow{}, fmt.Errorf("encode %q cli value: %w", f.Key, err)
		}

		row.CLIValue = sql.NullString{String: string(b), Valid: true}
		row.CLISeq = ks.cliSeq
	}

	if ks.hasUI {
		b, err := f.Encode(ks.uiValue)
		if err != nil {
			return db.SettingRow{}, fmt.Errorf("encode %q ui value: %w", f.Key, err)
		}

		row.UIValue = sql.NullString{String: string(b), Valid: true}
		row.UISeq = ks.uiSeq
	}

	return row, nil
}

// validateValue performs light per-field validation of incoming UI values.
func validateValue(key string, v any) error {
	if key == KeyScheduleMode {
		mode, _ := v.(ScheduleMode)
		switch mode {
		case ScheduleModeAll, ScheduleModeEveryN, ScheduleModeNextN:
		default:
			return fmt.Errorf("invalid schedule mode %q", mode)
		}
	}

	if key == KeySlotResultRetentionEpochs || key == KeySlotArtifactRetentionEpochs {
		epochs, _ := v.(uint64)
		if epochs == 0 {
			return fmt.Errorf("%s must be greater than 0", key)
		}
	}

	return nil
}

// ValidateTimingBounds checks the cross-field / slot-relative invariants a
// mutable timing setting must satisfy regardless of which layer (CLI, config
// file, or UI override) sets it. Neither violation crashes or corrupts
// anything by itself — an inverted bid window just suppresses bidding, and a
// too-late reveal time is cleanly skipped rather than published wrong — but
// both silently defeat the feature for the rest of the run, so they are
// better rejected outright at the point a value is accepted. slotDuration <=
// 0 skips the reveal-time upper bound (unknown yet, e.g. before the chain
// spec has been fetched at startup).
func ValidateTimingBounds(cfg *Config, slotDuration time.Duration) error {
	if cfg.EPBS.BidStartTime > cfg.EPBS.BidEndTime {
		return fmt.Errorf("%s (%dms) must not be after %s (%dms)",
			KeyEPBSBidStartTime, cfg.EPBS.BidStartTime, KeyEPBSBidEndTime, cfg.EPBS.BidEndTime)
	}

	if cfg.Reveal.TimeMs < 0 {
		return fmt.Errorf("%s must not be negative, got %dms", KeyRevealTimeMs, cfg.Reveal.TimeMs)
	}

	if slotDuration > 0 && time.Duration(cfg.Reveal.TimeMs)*time.Millisecond >= slotDuration {
		return fmt.Errorf("%s (%dms) must be less than the slot duration (%s)",
			KeyRevealTimeMs, cfg.Reveal.TimeMs, slotDuration)
	}

	return nil
}
