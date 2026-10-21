package config

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// slotTimingKey is the reserved settings row recording the slot duration the
// slot-relative timing settings are scaled for. It is no registered setting:
// the settings resolution never reads it.
const slotTimingKey = "_slot_timing"

// SlotTimingActor attributes the settings writes of a slot duration change.
const SlotTimingActor = "slot-duration-change"

// SlotTiming names the slot duration the timing settings are scaled for and
// the first slot of the chain segment that duration belongs to.
type SlotTiming struct {
	Slot       uint64 `json:"slot"`
	DurationMs int64  `json:"duration_ms"`
}

// LoadSlotTiming reads the persisted slot timing; ok is false when the
// settings were never scaled (or no state-db is configured), in which case
// the operator values are taken as written for the genesis slot duration.
func LoadSlotTiming(store *db.Database) (SlotTiming, bool, error) {
	rows, err := store.GetSettings()
	if err != nil {
		return SlotTiming{}, false, fmt.Errorf("load settings: %w", err)
	}

	for _, row := range rows {
		if row.Key != slotTimingKey || !row.UIValue.Valid {
			continue
		}

		var timing SlotTiming
		if err := json.Unmarshal([]byte(row.UIValue.String), &timing); err != nil {
			return SlotTiming{}, false, fmt.Errorf("decode %s: %w", slotTimingKey, err)
		}

		if timing.DurationMs <= 0 {
			return SlotTiming{}, false, nil
		}

		return timing, true, nil
	}

	return SlotTiming{}, false, nil
}

// SlotTiming returns the slot duration the timing settings are scaled for.
func (s *Service) SlotTiming() SlotTiming {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.slotTiming
}

// InitSlotTiming records the slot duration the effective timing settings are
// written for (the persisted one, or the genesis duration). Called once at
// startup, before any RescaleSlotTiming.
func (s *Service) InitSlotTiming(timing SlotTiming) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.slotTiming = timing
	s.persistSlotTiming()
}

// RescaleSlotTiming patches the timing settings once for a new slot duration:
// the defaults are recomputed for it (ApplySlotDefaults), and every slot-scaled
// setting carrying an operator value (CLI or UI) is scaled by new/old and
// written as a UI override, so it persists and an unchanged CLI flag does not
// restore the old value on restart. It only moves forward: a target whose
// segment does not start after the current one is ignored. Reports whether
// the settings changed. The effective config is updated before returning;
// the subscribers are notified asynchronously, as the patch runs on the
// decision paths that freeze slot plans.
func (s *Service) RescaleSlotTiming(target SlotTiming) bool {
	s.mu.Lock()

	from := s.slotTiming
	if target.Slot <= from.Slot || target.DurationMs <= 0 {
		s.mu.Unlock()

		return false
	}

	s.slotTiming = target
	s.persistSlotTiming()

	if target.DurationMs == from.DurationMs || from.DurationMs <= 0 {
		s.mu.Unlock()

		return false
	}

	defaults := DefaultConfig()
	defaults.ApplySlotDefaults(target.DurationMs)
	s.defaults = defaults

	now := time.Now().UnixMilli()
	scaled := make(logrus.Fields, len(slotScaledKeys))

	for _, f := range s.fields {
		if !f.SlotScaled {
			continue
		}

		ks := s.keyState[f.Key]

		value, ok := ks.winningValue()
		if !ok {
			continue
		}

		next, err := scaleTiming(value, from.DurationMs, target.DurationMs)
		if err != nil {
			s.log.WithError(err).WithField("key", f.Key).Error("failed to rescale setting")

			continue
		}

		ks.hasUI = true
		ks.uiValue = next
		ks.uiSeq = s.nextSeq()
		ks.uiAt = now
		s.persist(f, ks, SlotTimingActor)

		scaled[f.Key] = fmt.Sprintf("%v -> %v", value, next)
	}

	s.recompute()

	subs := make([]func(), len(s.subscribers))
	copy(subs, s.subscribers)
	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":             target.Slot,
		"from_duration_ms": from.DurationMs,
		"to_duration_ms":   target.DurationMs,
		"scaled":           scaled,
	}).Info("Slot duration changed: timing settings rescaled")

	go func() {
		for _, fn := range subs {
			fn()
		}
	}()

	return true
}

// winningValue returns the operator value (CLI or UI, by recency) of the key,
// or false when the default applies.
func (ks *keyState) winningValue() (any, bool) {
	var (
		value  any
		winSeq int64
	)

	if ks.hasCLI && ks.cliSeq > winSeq {
		value, winSeq = ks.cliValue, ks.cliSeq
	}

	if ks.hasUI && ks.uiSeq > winSeq {
		value, winSeq = ks.uiValue, ks.uiSeq
	}

	return value, winSeq > 0
}

// scaleTiming scales a millisecond timing by to/from, rounded to the nearest
// millisecond.
func scaleTiming(value any, from, to int64) (any, error) {
	switch v := value.(type) {
	case int64:
		return scaleMs(v, from, to), nil
	case uint64:
		return uint64(scaleMs(int64(v), from, to)), nil //nolint:gosec // timings are far below MaxInt64
	default:
		return nil, fmt.Errorf("unsupported timing type %T", value)
	}
}

func scaleMs(v, from, to int64) int64 {
	half := from / 2
	if v < 0 {
		half = -half
	}

	return (v*to + half) / from
}

// persistSlotTiming stores the slot timing row. Must hold mu.
func (s *Service) persistSlotTiming() {
	if !s.store.Enabled() {
		return
	}

	b, err := json.Marshal(s.slotTiming)
	if err != nil {
		return
	}

	row := db.SettingRow{
		Key:       slotTimingKey,
		UIValue:   sql.NullString{String: string(b), Valid: true},
		UpdatedAt: time.Now().UnixMilli(),
		Actor:     SlotTimingActor,
	}

	if err := s.store.PutSetting(row); err != nil {
		s.log.WithError(err).Warn("failed to persist slot timing")
	}
}
