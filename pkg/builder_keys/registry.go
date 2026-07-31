package builder_keys

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

const (
	// defaultDiscoveryGap ends the startup scan after this many consecutive
	// never-used indices when the config leaves it at zero.
	defaultDiscoveryGap = 100
	// defaultMaxIndex bounds derivation when the config leaves it at zero.
	defaultMaxIndex = 1000
	// depositPendingTTL is how long a submitted deposit keeps its key in the
	// depositing state without any on-chain evidence. Beyond it the key falls
	// back to unused/withdrawn so the reconciler retries instead of waiting
	// forever on a deposit that never made it into the queue.
	depositPendingTTL = 30 * time.Minute
)

// BalanceAdjuster supplies the local balance delta of a key: credits from
// top-ups and debits from revealed payments that the latest beacon state
// snapshot does not reflect yet. Implemented by payload_bidder.PaymentTracker.
type BalanceAdjuster interface {
	GetBalanceAdjustment(keyIndex uint64) int64
}

// ChangeEvent carries the full key set whenever any key's state changes, so
// subscribers (the WebUI bridge) can push a complete snapshot without querying.
type ChangeEvent struct {
	States    []*State
	Aggregate Aggregate
}

// keyRuntime is the registry-owned mutable state of one key that does not come
// from the beacon state: our own in-flight operations and rolling counters.
type keyRuntime struct {
	key   *Key
	usage *Usage

	// depositPendingUntil keeps the key in the depositing state after we
	// submitted a deposit but before the beacon state shows it.
	depositPendingUntil time.Time
	lastTopupEpoch      phase0.Epoch
	bidsSubmitted       uint64
	bidsWon             uint64
}

// Registry is the single owner of the builder key set: derivation, per-key
// on-chain state, usage history and selection. It is the identity dependency of
// every module that used to hold a single *signer.BLSSigner.
type Registry struct {
	cfg      *config.Config
	entrySK  string
	log      logrus.FieldLogger
	adjuster BalanceAdjuster

	mu sync.RWMutex
	// runtimes holds every derived key (the derivation cache); tracked/order
	// hold the subset that forms the visible fleet.
	runtimes       map[uint64]*keyRuntime
	tracked        map[uint64]struct{}
	order          []uint64
	byPubkey       map[phase0.BLSPubKey]*Key
	byBuilderIndex map[uint64]*Key

	usage    *memstore.Store[uint64, *Usage]
	chainSvc chain.Service
	changes  utils.Dispatcher[*ChangeEvent]

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRegistry creates the key set rooted at the operator's entry key. It derives
// key 0 eagerly, which validates the supplied key material; every other key is
// derived on demand and cached for the process lifetime.
func NewRegistry(cfg *config.Config, entryPrivkeyHex string, log logrus.FieldLogger) (*Registry, error) {
	r := &Registry{
		cfg:            cfg,
		entrySK:        entryPrivkeyHex,
		log:            log.WithField("component", "builder-keys"),
		runtimes:       make(map[uint64]*keyRuntime, 8),
		tracked:        make(map[uint64]struct{}, 8),
		order:          make([]uint64, 0, 8),
		byPubkey:       make(map[phase0.BLSPubKey]*Key, 8),
		byBuilderIndex: make(map[uint64]*Key, 8),
		usage:          memstore.New[uint64, *Usage](),
	}

	if _, err := r.derive(0); err != nil {
		return nil, err
	}

	r.setTracked(0, true)

	return r, nil
}

// SetBalanceAdjuster wires the source of local balance deltas. Optional: without
// it, effective balances reflect the last beacon state snapshot only.
func (r *Registry) SetBalanceAdjuster(adjuster BalanceAdjuster) {
	r.mu.Lock()
	r.adjuster = adjuster
	r.mu.Unlock()
}

// Start attaches usage persistence, runs the initial discovery scan and keeps
// key states in sync with every epoch's beacon state.
func (r *Registry) Start(ctx context.Context, chainSvc chain.Service, stateDB *db.Database) error {
	r.mu.Lock()
	r.chainSvc = chainSvc
	r.mu.Unlock()

	if stateDB != nil {
		r.usage.SetPersistence(ctx, db.NewKVPersistence(stateDB, Namespace, UsageCodec{}), r.log)

		if err := r.verifyPersistedKeys(); err != nil {
			return err
		}
	}

	r.Refresh()

	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	r.wg.Add(1)

	go r.run(runCtx)

	r.log.WithFields(logrus.Fields{
		"target":      r.cfg.BuilderKeys.EffectiveTargetCount(),
		"known_keys":  len(r.order),
		"primary_key": r.Primary().String(),
	}).Info("Builder key registry started")

	return nil
}

// Stop stops the registry's refresh loop and flushes usage persistence.
func (r *Registry) Stop() {
	if r.cancel != nil {
		r.cancel()
	}

	r.wg.Wait()
	r.usage.Stop()
}

// run refreshes key states on every epoch transition: the beacon state snapshot
// that backs balances, registrations and pending payments is replaced there.
func (r *Registry) run(ctx context.Context) {
	defer r.wg.Done()

	r.mu.RLock()
	chainSvc := r.chainSvc
	r.mu.RUnlock()

	// A nil channel simply never fires, which is what a registry without a chain
	// service (tests, pre-chain startup) wants.
	var epochCh <-chan *chain.EpochStats

	if chainSvc != nil {
		epochSub := chainSvc.SubscribeEpochStats()
		defer epochSub.Unsubscribe()

		epochCh = epochSub.Channel()
	}

	// A slower standalone tick keeps locally-driven transitions (a submitted
	// deposit, an expired deposit TTL) visible between epochs.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-epochCh:
			if !ok {
				return
			}

			r.Refresh()
		case <-ticker.C:
			r.Refresh()
		}
	}
}

// verifyPersistedKeys checks that every persisted usage record still matches the
// key its index derives to. A mismatch means the entry key changed, in which
// case the whole record set describes a different fleet and acting on it would
// deposit against, top up, or exit the wrong keys.
func (r *Registry) verifyPersistedKeys() error {
	maxIndex := r.maxIndex()

	for keyIndex, usage := range r.usage.Entries() {
		if keyIndex > maxIndex {
			r.log.WithFields(logrus.Fields{
				"key_index": keyIndex,
				"max_index": maxIndex,
			}).Warn("Persisted builder key usage above the derivation cap; ignoring")

			continue
		}

		key, err := r.derive(keyIndex)
		if err != nil {
			return err
		}

		// A key we have deposited for before is part of the fleet regardless of
		// where it sits relative to the current target.
		r.setTracked(keyIndex, true)

		if usage.Pubkey == "" {
			continue
		}

		if derived := fmt.Sprintf("%#x", key.Pubkey()); usage.Pubkey != derived {
			return fmt.Errorf(
				"persisted builder key %d has pubkey %s but the configured entry key derives %s: "+
					"the builder key source changed, refusing to manage a foreign key set",
				keyIndex, usage.Pubkey, derived)
		}
	}

	return nil
}

// derive derives the key at the given internal index and caches it for the
// process lifetime. Derived is not the same as tracked: the discovery scan
// derives well past the target to look for keys we used before, but only the
// keys that matter (within the target, or known) join the visible set.
func (r *Registry) derive(keyIndex uint64) (*Key, error) {
	r.mu.RLock()
	runtime, ok := r.runtimes[keyIndex]
	r.mu.RUnlock()

	if ok {
		return runtime.key, nil
	}

	key, err := newKey(r.entrySK, keyIndex)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.runtimes[keyIndex]; ok {
		return existing.key, nil
	}

	r.runtimes[keyIndex] = &keyRuntime{key: key}
	r.byPubkey[key.Pubkey()] = key

	return key, nil
}

// setTracked adds or removes a key from the visible set. Tracked keys are the
// ones the fleet consists of: everything within the target count plus every key
// with an on-chain entry, a deposit in flight, or a usage history.
func (r *Registry) setTracked(keyIndex uint64, tracked bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, known := r.tracked[keyIndex]
	if known == tracked {
		return
	}

	if tracked {
		r.tracked[keyIndex] = struct{}{}
		r.order = append(r.order, keyIndex)

		slices.Sort(r.order)

		return
	}

	delete(r.tracked, keyIndex)

	r.order = slices.DeleteFunc(r.order, func(index uint64) bool { return index == keyIndex })
}

// highestTracked returns the highest tracked key index, or 0 when none is
// tracked yet.
func (r *Registry) highestTracked() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.order) == 0 {
		return 0
	}

	return r.order[len(r.order)-1]
}

// maxIndex returns the effective derivation cap.
func (r *Registry) maxIndex() uint64 {
	if r.cfg.BuilderKeys.MaxIndex > 0 {
		return r.cfg.BuilderKeys.MaxIndex
	}

	return defaultMaxIndex
}

// discoveryGap returns the effective number of consecutive unused indices that
// ends the discovery scan.
func (r *Registry) discoveryGap() uint64 {
	if r.cfg.BuilderKeys.DiscoveryGap > 0 {
		return r.cfg.BuilderKeys.DiscoveryGap
	}

	return defaultDiscoveryGap
}

// Refresh recomputes every key's state from the current beacon state and rescans
// for keys we have used before.
//
// The scan always covers the target count and everything already tracked, and
// keeps going past that while it finds keys of ours — stopping only after a full
// run of never-used indices (the discovery gap). Unused indices above the target
// are derived but not tracked, so the fleet view stays the size of the fleet
// rather than the size of the scan. Fires a ChangeEvent when anything changed.
func (r *Registry) Refresh() {
	snapshot := r.chainSnapshot()

	target := r.cfg.BuilderKeys.EffectiveTargetCount()
	maxIndex := r.maxIndex()
	gapLimit := r.discoveryGap()

	// Never stop before the target or before the highest key we already know
	// about: a key touched by an operation must be refreshed even when it sits
	// far above the target.
	scanFloor := max(target, r.highestTracked()+1)

	changed := false
	gap := uint64(0)

	for keyIndex := uint64(0); keyIndex <= maxIndex; keyIndex++ {
		if keyIndex >= scanFloor && gap >= gapLimit {
			break
		}

		key, err := r.derive(keyIndex)
		if err != nil {
			r.log.WithError(err).WithField("key_index", keyIndex).Error("Failed to derive builder key")

			break
		}

		state, dirty := r.refreshKey(key, snapshot)
		changed = changed || dirty

		known := state.Status != StatusUnused
		r.setTracked(keyIndex, known || keyIndex < target)

		switch {
		case keyIndex < target:
			// Always covered; these do not count toward the discovery gap.
		case known:
			gap = 0
			scanFloor = max(scanFloor, keyIndex+1)
		default:
			gap++
		}
	}

	r.rebuildBuilderIndex()

	if changed {
		r.changes.Fire(&ChangeEvent{States: r.States(), Aggregate: r.Aggregate()})
	}
}

// chainState is the per-refresh view of the beacon state, resolved once so a
// refresh over hundreds of keys stays a single pass.
type chainState struct {
	builders        map[phase0.BLSPubKey]*chain.BuilderInfo
	pendingDeposits map[phase0.BLSPubKey]struct{}
	currentEpoch    phase0.Epoch
	finalizedEpoch  uint64
}

// chainSnapshot builds the per-refresh beacon state view.
func (r *Registry) chainSnapshot() *chainState {
	r.mu.RLock()
	chainSvc := r.chainSvc
	r.mu.RUnlock()

	snapshot := &chainState{
		builders:        map[phase0.BLSPubKey]*chain.BuilderInfo{},
		pendingDeposits: map[phase0.BLSPubKey]struct{}{},
	}

	if chainSvc == nil {
		return snapshot
	}

	snapshot.currentEpoch = chainSvc.GetCurrentEpoch()
	snapshot.finalizedEpoch = uint64(chainSvc.GetFinalizedEpoch())

	for _, info := range chainSvc.GetBuilders() {
		if info != nil {
			snapshot.builders[info.Pubkey] = info
		}
	}

	if stats := chainSvc.GetCurrentEpochStats(); stats != nil {
		for i := range stats.PendingDeposits {
			snapshot.pendingDeposits[stats.PendingDeposits[i].Pubkey] = struct{}{}
		}
	}

	return snapshot
}

// refreshKey recomputes one key's state snapshot, returning it plus whether it
// differs from the previous one.
func (r *Registry) refreshKey(key *Key, snapshot *chainState) (*State, bool) {
	r.mu.Lock()

	runtime := r.runtimes[key.KeyIndex()]
	if runtime == nil {
		r.mu.Unlock()

		return key.State(), false
	}

	usage, _ := r.usage.Get(key.KeyIndex())
	runtime.usage = usage
	adjuster := r.adjuster

	state := &State{
		KeyIndex:       key.KeyIndex(),
		Pubkey:         key.Pubkey(),
		PubkeyHex:      fmt.Sprintf("%#x", key.Pubkey()),
		LastTopupEpoch: runtime.lastTopupEpoch,
		BidsSubmitted:  runtime.bidsSubmitted,
		BidsWon:        runtime.bidsWon,
	}

	if usage != nil {
		state.UseCount = usage.UseCount
		state.LastDepositAt = usage.LastDepositAt
		state.LastExitAt = usage.LastExitAt
	}

	info := snapshot.builders[key.Pubkey()]
	_, queued := snapshot.pendingDeposits[key.Pubkey()]
	depositInFlight := queued || time.Now().Before(runtime.depositPendingUntil)

	r.mu.Unlock()

	if info != nil {
		state.BuilderIndex = info.Index
		state.HasBuilderIndex = true
		state.Balance = info.Balance
		state.PendingPayments = info.PendingPayments
		state.DepositEpoch = info.DepositEpoch
		state.WithdrawableEpoch = info.WithdrawableEpoch
	}

	state.Status = resolveStatus(info, depositInFlight, state.UseCount,
		snapshot.currentEpoch, snapshot.finalizedEpoch)

	if adjuster != nil {
		state.BalanceAdjustment = adjuster.GetBalanceAdjustment(key.KeyIndex())
	}

	state.EffectiveBalance = effectiveBalance(state)

	previous := key.State()
	if *previous == *state {
		return previous, false
	}

	key.state.Store(state)

	if previous.Status != state.Status {
		r.log.WithFields(logrus.Fields{
			"key":  key.String(),
			"from": previous.Status,
			"to":   state.Status,
		}).Info("Builder key status changed")
	}

	return state, true
}

// resolveStatus decides a key's lifecycle position from its on-chain registry
// entry, whether a deposit of ours is in flight, and how often the key has been
// used before.
//
// The deposit-in-flight check must precede the usage check: a key whose first
// deposit was just submitted already has a non-zero use count while its pubkey
// is still absent from the registry, and reading that as "withdrawn" would let
// the reconciler deposit for it a second time.
func resolveStatus(
	info *chain.BuilderInfo,
	depositInFlight bool,
	useCount uint32,
	currentEpoch phase0.Epoch,
	finalizedEpoch uint64,
) Status {
	switch {
	case info == nil && depositInFlight:
		return StatusDepositing
	case info == nil && useCount > 0:
		// Used before and gone from the registry: depositable again.
		return StatusWithdrawn
	case info == nil:
		return StatusUnused
	case chain.HasBuilderExited(info):
		if uint64(currentEpoch) >= info.WithdrawableEpoch {
			return StatusExited
		}

		return StatusExiting
	case chain.IsBuilderActive(info, finalizedEpoch):
		return StatusActive
	default:
		return StatusPending
	}
}

// effectiveBalance applies the local adjustment and pending payments to the
// snapshot balance, flooring at zero.
func effectiveBalance(state *State) uint64 {
	live := max(int64(state.Balance)+state.BalanceAdjustment, 0) //nolint:gosec // gwei balances stay far below int64

	balance := uint64(live)
	if state.PendingPayments >= balance {
		return 0
	}

	return balance - state.PendingPayments
}

// rebuildBuilderIndex refreshes the builder-index lookup used to resolve the key
// behind a block's winning bid.
func (r *Registry) rebuildBuilderIndex() {
	r.mu.Lock()
	defer r.mu.Unlock()

	byIndex := make(map[uint64]*Key, len(r.runtimes))

	for _, runtime := range r.runtimes {
		if builderIndex, ok := runtime.key.BuilderIndex(); ok {
			byIndex[builderIndex] = runtime.key
		}
	}

	r.byBuilderIndex = byIndex
}

// Key returns the derived key at the given internal index, deriving it if
// needed.
func (r *Registry) Key(keyIndex uint64) (*Key, error) {
	if keyIndex > r.maxIndex() {
		return nil, fmt.Errorf("builder key index %d exceeds the derivation cap %d", keyIndex, r.maxIndex())
	}

	return r.derive(keyIndex)
}

// Keys returns every tracked key, key-index ascending.
func (r *Registry) Keys() []*Key {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]*Key, 0, len(r.order))
	for _, keyIndex := range r.order {
		keys = append(keys, r.runtimes[keyIndex].key)
	}

	return keys
}

// Primary returns the key that stands in wherever a single builder identity is
// required (the pre-Gloas Builder API, the legacy lifecycle endpoints): the
// lowest-index active key, or the entry key when none is active.
func (r *Registry) Primary() *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var fallback *Key

	for _, keyIndex := range r.order {
		key := r.runtimes[keyIndex].key
		if key.Status() == StatusActive {
			return key
		}

		if fallback == nil {
			fallback = key
		}
	}

	return fallback
}

// ByPubkey returns the key with the given public key, or nil.
func (r *Registry) ByPubkey(pubkey phase0.BLSPubKey) *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.byPubkey[pubkey]
}

// ByBuilderIndex returns the key registered under the given on-chain builder
// index, or nil when the index is not ours. This is how a won block's bid is
// traced back to the key that must sign the reveal.
func (r *Registry) ByBuilderIndex(builderIndex uint64) *Key {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.byBuilderIndex[builderIndex]
}

// State returns the state snapshot of one derived key, or nil when the index has
// not been derived.
func (r *Registry) State(keyIndex uint64) *State {
	r.mu.RLock()
	runtime, ok := r.runtimes[keyIndex]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	return runtime.key.State()
}

// States returns a snapshot of every derived key's state, key-index ascending.
func (r *Registry) States() []*State {
	keys := r.Keys()

	states := make([]*State, 0, len(keys))
	for _, key := range keys {
		states = append(states, key.State())
	}

	return states
}

// Aggregate summarises the key set for the dashboard.
func (r *Registry) Aggregate() Aggregate {
	aggregate := Aggregate{Target: r.cfg.BuilderKeys.EffectiveTargetCount()}

	for _, state := range r.States() {
		switch state.Status {
		case StatusUnused:
			aggregate.Unused++
		case StatusDepositing:
			aggregate.Depositing++
		case StatusPending:
			aggregate.Pending++
		case StatusActive:
			aggregate.Active++
		case StatusExiting:
			aggregate.Exiting++
		case StatusExited:
			aggregate.Exited++
		case StatusWithdrawn:
			aggregate.Withdrawn++
		}

		if state.Status.Managed() {
			aggregate.Managed++
		}

		if state.HasBuilderIndex {
			aggregate.TotalBalance += state.Balance
			aggregate.TotalPendingPayments += state.PendingPayments
			aggregate.TotalEffective += state.EffectiveBalance
		}
	}

	return aggregate
}

// AnyActive reports whether at least one key is active on chain — the fleet-wide
// availability gate that replaces the single-builder registration check.
func (r *Registry) AnyActive() bool {
	for _, key := range r.Keys() {
		if key.Status() == StatusActive {
			return true
		}
	}

	return false
}

// SubscribeChanges subscribes to key set changes.
func (r *Registry) SubscribeChanges(capacity int, blocking bool) *utils.Subscription[*ChangeEvent] {
	return r.changes.Subscribe(capacity, blocking)
}

// RecordBid counts a submitted bid against a key (selection input + UI).
func (r *Registry) RecordBid(keyIndex uint64) {
	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.bidsSubmitted++
	}

	r.mu.Unlock()
}

// RecordWin counts a won slot against a key.
func (r *Registry) RecordWin(keyIndex uint64) {
	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.bidsWon++
	}

	r.mu.Unlock()
}
