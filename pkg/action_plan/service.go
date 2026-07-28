package action_plan

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

// Namespace is the kv_store namespace holding the persisted slot plans.
const Namespace = "slot_plans"

// MaxSlotsPerUpdate bounds the unique slots one ApplyUpdates call may target
// (200 epochs at 32 slots each).
const MaxSlotsPerUpdate = 6400

// ErrSlotLocked is returned (wrapped) when an update targets a slot that is
// in the past or whose plan is already frozen. API handlers map it to
// 409 Conflict.
var ErrSlotLocked = errors.New("slot is in the past or already frozen")

// PlanChangeEvent describes one committed ApplyUpdates call: the authoritative
// normalized result, not merely a count. Plans is index-aligned with Slots;
// a nil entry means the slot's plan was deleted.
type PlanChangeEvent struct {
	Slots []uint64    `json:"slots"`
	Plans []*SlotPlan `json:"plans"`
}

// RuleChangeEvent carries the authoritative rule set after a committed change.
type RuleChangeEvent struct {
	Rules []*SlotRule `json:"rules"`
}

// PlanService owns the sparse per-slot action plan store, the recurring rules
// that fill the slots it does not cover, their freeze state and their
// persistence. It is the single writer; all reads return deep copies.
type PlanService struct {
	cfg      *config.Config
	chainSvc chain.Service
	store    *memstore.Store[phase0.Slot, *SlotPlan]
	rules    *memstore.Store[string, *SlotRule]

	// mu guards frozen and slotsBuilt, and additionally serializes recurring
	// rule swaps against Freeze: both stores have their own locks, but only
	// this mutex guarantees that no slot freezes against a half-replaced rule
	// set. Do not drop it from SetRules.
	mu     sync.Mutex
	frozen map[phase0.Slot]*FrozenPlan

	// slotsBuilt counts schedule-consuming builds for next_n mode (forced
	// builds are exempt). Guarded by mu.
	slotsBuilt uint64

	changes     utils.Dispatcher[*PlanChangeEvent]
	ruleChanges utils.Dispatcher[*RuleChangeEvent]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewPlanService creates the plan service. The config pointer is the shared
// live config; enable flags and default settings are read from it at freeze
// time.
func NewPlanService(cfg *config.Config, chainSvc chain.Service, log logrus.FieldLogger) *PlanService {
	return &PlanService{
		cfg:      cfg,
		chainSvc: chainSvc,
		store:    memstore.New[phase0.Slot, *SlotPlan](),
		rules:    memstore.New[string, *SlotRule](),
		frozen:   make(map[phase0.Slot]*FrozenPlan, 64),
		log:      log.WithField("component", "action-plan"),
	}
}

// SetPersistence attaches the state-db backed persistence (kv_store namespaces
// "slot_plans" and "slot_rules") and rehydrates previously stored plans and
// recurring rules.
func (s *PlanService) SetPersistence(ctx context.Context, stateDB *db.Database) {
	s.store.SetPersistence(ctx, db.NewKVPersistence(stateDB, Namespace, PlanCodec{}), s.log)
	s.rules.SetPersistence(ctx, db.NewKVPersistence(stateDB, RulesNamespace, RuleCodec{}), s.log)
}

// Start launches the pruning loop (driven by epoch transitions).
func (s *PlanService) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	epochSub := s.chainSvc.SubscribeEpochStats()

	s.wg.Add(1)

	go s.run(epochSub)

	// Rehydrated rules are validated against the spec they were written under,
	// not the active one — a state-db carried to a network with a different
	// SLOTS_PER_EPOCH would otherwise leave them silently dead.
	for _, id := range s.unmatchableRules() {
		s.log.WithFields(logrus.Fields{
			"rule":            id,
			"slots_per_epoch": s.slotsPerEpoch(),
		}).Warn("Recurring rule can never match: slot index out of range for the active chain spec")
	}

	s.log.Info("Action plan service started")

	return nil
}

// unmatchableRules returns the ids of rules carrying a slot index the active
// chain spec can never produce. Writes are validated against the spec, so this
// only ever fires for rules rehydrated from another network's state-db.
func (s *PlanService) unmatchableRules() []string {
	rules := s.sortedRules()
	if len(rules) == 0 {
		return nil
	}

	slotsPerEpoch := s.slotsPerEpoch()
	if slotsPerEpoch == 0 {
		s.log.Warn("Cannot verify recurring rules: chain spec unavailable")

		return nil
	}

	var unmatchable []string

	for _, rule := range rules {
		for _, index := range rule.SlotsInEpoch {
			if index >= slotsPerEpoch {
				unmatchable = append(unmatchable, rule.ID)

				break
			}
		}
	}

	return unmatchable
}

// Stop terminates the pruning loop and flushes the store. Must be called
// before the state-db closes.
func (s *PlanService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()
	s.store.Stop()
	s.rules.Stop()

	s.log.Info("Action plan service stopped")
}

func (s *PlanService) run(epochSub *utils.Subscription[*chain.EpochStats]) {
	defer s.wg.Done()
	defer epochSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return
		case epochStats, ok := <-epochSub.Channel():
			if !ok {
				return
			}

			s.pruneForEpoch(epochStats.Epoch)
		}
	}
}

// Get returns a deep copy of the slot's explicitly stored plan, or nil when
// none exists. Recurring rules are deliberately not consulted — use
// PlanForSlot for the effective plan.
func (s *PlanService) Get(slot phase0.Slot) *SlotPlan {
	plan, ok := s.store.Get(slot)
	if !ok {
		return nil
	}

	return plan.Clone()
}

// PlanForSlot returns the effective plan for a slot: the explicitly stored
// plan if there is one, otherwise the first matching recurring rule
// materialized for the slot (marked with its rule id). Precedence is
// wholesale — an explicit plan is never merged with a rule.
func (s *PlanService) PlanForSlot(slot phase0.Slot) *SlotPlan {
	if plan, ok := s.store.Get(slot); ok {
		return plan.Clone()
	}

	rule := matchRule(s.sortedRules(), slot, s.slotsPerEpoch())
	if rule == nil {
		return nil
	}

	return rule.planFor(slot)
}

// GetRange returns deep copies of all effective plans within
// [minSlot, maxSlot], slot-ascending: every explicitly stored plan plus the
// plans recurring rules contribute to the still-open slots of the range.
func (s *PlanService) GetRange(minSlot, maxSlot phase0.Slot) []*SlotPlan {
	entries := s.store.Entries()
	plans := make([]*SlotPlan, 0, len(entries))

	for slot, plan := range entries {
		if slot >= minSlot && slot <= maxSlot {
			plans = append(plans, plan.Clone())
		}
	}

	plans = append(plans, s.rulePlansInRange(minSlot, maxSlot, entries)...)

	sortPlansBySlot(plans)

	return plans
}

// rulePlansInRange materializes the rule-derived plans of a range. Only slots
// that can still execute under the CURRENT rule set are covered: past and
// already frozen slots ran under whatever was configured back then, and their
// slot result carries the authoritative applied plan.
func (s *PlanService) rulePlansInRange(minSlot, maxSlot phase0.Slot,
	explicit map[phase0.Slot]*SlotPlan) []*SlotPlan {
	rules := s.sortedRules()
	slotsPerEpoch := s.slotsPerEpoch()

	if len(rules) == 0 || slotsPerEpoch == 0 {
		return nil
	}

	if firstOpen := s.chainSvc.GetCurrentSlot() + 1; firstOpen > minSlot {
		minSlot = firstOpen
	}

	if maxSlot < minSlot {
		return nil
	}

	frozen := s.frozenSlotsInRange(minSlot, maxSlot)

	var plans []*SlotPlan

	for slot := minSlot; ; slot++ {
		_, hasPlan := explicit[slot]
		_, isFrozen := frozen[slot]

		if !hasPlan && !isFrozen {
			if rule := matchRule(rules, slot, slotsPerEpoch); rule != nil {
				plans = append(plans, rule.planFor(slot))
			}
		}

		if slot == maxSlot {
			break
		}
	}

	return plans
}

// frozenSlotsInRange snapshots the freeze markers within the range under one
// lock; the marker map is pruned to roughly an epoch, so this stays cheap.
func (s *PlanService) frozenSlotsInRange(minSlot, maxSlot phase0.Slot) map[phase0.Slot]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	frozen := make(map[phase0.Slot]struct{}, len(s.frozen))

	for slot := range s.frozen {
		if slot >= minSlot && slot <= maxSlot {
			frozen[slot] = struct{}{}
		}
	}

	return frozen
}

// Freeze resolves and records the slot's immutable execution snapshot. The
// first caller wins; every later caller (and every other decision point of
// the same slot) receives the identical snapshot. From this moment on the
// slot's plan can no longer be edited, and later rule changes no longer
// affect it.
func (s *PlanService) Freeze(slot phase0.Slot) *FrozenPlan {
	s.mu.Lock()
	defer s.mu.Unlock()

	if frozen, ok := s.frozen[slot]; ok {
		return frozen
	}

	// Rule resolution reads the plan/rule stores only — it must never take s.mu.
	plan := s.PlanForSlot(slot)

	fork := s.chainSvc.ActiveForkAtEpoch(s.chainSvc.GetEpochOfSlot(slot))
	frozen := resolveFrozenPlan(slot, plan, s.cfg, fork, time.Now(), s.slotsBuilt)
	s.frozen[slot] = frozen

	return frozen
}

// OnSlotBuilt records a successfully built slot for the next_n schedule
// accounting. Forced (plan-activated) builds never consume the budget.
func (s *PlanService) OnSlotBuilt(slot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if frozen, ok := s.frozen[slot]; ok && frozen.Build != nil && frozen.Build.Forced {
		return
	}

	s.slotsBuilt++
}

// UpdateConfig reacts to global settings changes. Switching to next_n mode
// resets the built-slot counter so the budget restarts (mirroring the
// previous slot-manager behavior).
func (s *PlanService) UpdateConfig() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.Schedule.Mode == config.ScheduleModeNextN {
		s.slotsBuilt = 0
	}
}

// GetSlotsBuilt returns the number of schedule-consuming builds so far.
func (s *PlanService) GetSlotsBuilt() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.slotsBuilt
}

// GetSlotsRemaining returns the remaining next_n build budget, or -1 when
// the schedule is unlimited.
func (s *PlanService) GetSlotsRemaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.Schedule.Mode != config.ScheduleModeNextN {
		return -1
	}

	if s.slotsBuilt >= s.cfg.Schedule.NextN {
		return 0
	}

	return int(s.cfg.Schedule.NextN - s.slotsBuilt)
}

// IsFrozen reports whether the slot's plan has been frozen already.
func (s *PlanService) IsFrozen(slot phase0.Slot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.frozen[slot]

	return ok
}

// ApplyUpdates validates and applies a bulk plan mutation atomically: either
// every targeted slot is updated or none is. Overlapping updates are applied
// in request order. The returned event is the authoritative normalized result
// (also fired to change subscribers).
func (s *PlanService) ApplyUpdates(updates []*PlanUpdate, actor string) (*PlanChangeEvent, error) {
	if len(updates) == 0 {
		return nil, errors.New("no updates provided")
	}

	secondsPerSlot := s.chainSvc.GetChainSpec().SecondsPerSlot
	currentSlot := s.chainSvc.GetCurrentSlot()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Stage everything first; commit only when every update validated.
	staged := make(map[phase0.Slot]*SlotPlan, 64)
	targeted := make(map[phase0.Slot]struct{}, 64)

	for i, update := range updates {
		targets, err := update.TargetSlots()
		if err != nil {
			return nil, fmt.Errorf("update %d: %w", i, err)
		}

		for _, slot := range targets {
			if slot <= currentSlot {
				return nil, fmt.Errorf("update %d: slot %d: %w (current slot %d)",
					i, slot, ErrSlotLocked, currentSlot)
			}

			if _, frozen := s.frozen[slot]; frozen {
				return nil, fmt.Errorf("update %d: slot %d: %w", i, slot, ErrSlotLocked)
			}

			targeted[slot] = struct{}{}
			if len(targeted) > MaxSlotsPerUpdate {
				return nil, fmt.Errorf("request targets more than %d unique slots", MaxSlotsPerUpdate)
			}

			existing, wasStaged := staged[slot]
			if !wasStaged {
				if stored, ok := s.store.Get(slot); ok {
					existing = stored.Clone()
				}
			}

			result, err := ApplyUpdateToPlan(existing, update)
			if err != nil {
				return nil, fmt.Errorf("update %d: slot %d: %w", i, slot, err)
			}

			if result != nil {
				result.Slot = slot
				result.UpdatedAt = now
				result.UpdatedBy = actor

				if err := result.Validate(secondsPerSlot); err != nil {
					return nil, fmt.Errorf("update %d: slot %d: %w", i, slot, err)
				}
			}

			staged[slot] = result
		}
	}

	// Commit.
	event := &PlanChangeEvent{
		Slots: make([]uint64, 0, len(staged)),
		Plans: make([]*SlotPlan, 0, len(staged)),
	}

	for slot, plan := range staged {
		if plan == nil {
			s.store.Delete(slot)
		} else {
			s.store.Put(slot, plan)
		}

		event.Slots = append(event.Slots, uint64(slot))
		event.Plans = append(event.Plans, plan.Clone())
	}

	sortChangeEvent(event)

	s.log.WithFields(logrus.Fields{
		"slots": len(event.Slots),
		"actor": actor,
	}).Info("Applied action plan updates")

	s.changes.Fire(event)

	return event, nil
}

// SubscribeChanges subscribes to committed plan mutations (non-blocking
// delivery; intended for the SSE bridge).
func (s *PlanService) SubscribeChanges(capacity int) *utils.Subscription[*PlanChangeEvent] {
	return s.changes.Subscribe(capacity, false)
}

// SubscribeRuleChanges subscribes to committed recurring-rule mutations
// (non-blocking delivery; intended for the SSE bridge).
func (s *PlanService) SubscribeRuleChanges(capacity int) *utils.Subscription[*RuleChangeEvent] {
	return s.ruleChanges.Subscribe(capacity, false)
}

// Rules returns deep copies of all recurring rules, id-ascending — which is
// also the order in which they are matched.
func (s *PlanService) Rules() []*SlotRule {
	stored := s.sortedRules()
	rules := make([]*SlotRule, len(stored))

	for i, rule := range stored {
		rules[i] = rule.Clone()
	}

	return rules
}

// SetRules atomically replaces the whole recurring rule set: either every rule
// validates and is committed, or nothing changes. Already frozen slots keep
// the plan they froze with; every later slot resolves against the new set.
func (s *PlanService) SetRules(rules []*SlotRule, actor string) ([]*SlotRule, error) {
	if len(rules) > MaxRules {
		return nil, fmt.Errorf("too many rules: %d (max %d)", len(rules), MaxRules)
	}

	spec := s.chainSvc.GetChainSpec()
	now := time.Now()

	staged := make(map[string]*SlotRule, len(rules))

	for i, rule := range rules {
		if rule == nil {
			return nil, fmt.Errorf("rule %d: must not be null", i)
		}

		if err := rule.Validate(spec.SlotsPerEpoch, spec.SecondsPerSlot); err != nil {
			return nil, err
		}

		if _, dup := staged[rule.ID]; dup {
			return nil, fmt.Errorf("duplicate rule id %q", rule.ID)
		}

		stored := rule.Clone()
		stored.UpdatedAt = now
		stored.UpdatedBy = actor
		staged[rule.ID] = stored
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id := range s.rules.Entries() {
		if _, keep := staged[id]; !keep {
			s.rules.Delete(id)
		}
	}

	for id, rule := range staged {
		s.rules.Put(id, rule)
	}

	event := &RuleChangeEvent{Rules: s.Rules()}

	s.log.WithFields(logrus.Fields{
		"rules": len(event.Rules),
		"actor": actor,
	}).Info("Updated action plan rules")

	s.ruleChanges.Fire(event)

	return event.Rules, nil
}

// sortedRules returns the stored rules in match order (id-ascending). The
// returned values are the stored pointers — package-internal read-only use.
func (s *PlanService) sortedRules() []*SlotRule {
	rules := s.rules.Values()

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].ID < rules[j].ID
	})

	return rules
}

func (s *PlanService) slotsPerEpoch() uint64 {
	spec := s.chainSvc.GetChainSpec()
	if spec == nil {
		return 0
	}

	return spec.SlotsPerEpoch
}

// matchRule returns the first rule of the (id-ordered) set that applies to the
// slot, so the id decides which rule wins when several match.
func matchRule(rules []*SlotRule, slot phase0.Slot, slotsPerEpoch uint64) *SlotRule {
	for _, rule := range rules {
		if rule.Matches(slot, slotsPerEpoch) {
			return rule
		}
	}

	return nil
}

// pruneForEpoch drops past plans outside the retention window and stale
// freeze markers. Future plans never match the cutoff and are never pruned.
func (s *PlanService) pruneForEpoch(epoch phase0.Epoch) {
	retention := s.cfg.SlotResultRetentionEpochs // live read; mutable setting
	if retention == 0 || uint64(epoch) <= retention {
		return
	}

	slotsPerEpoch := s.chainSvc.GetChainSpec().SlotsPerEpoch
	cutoff := phase0.Slot((uint64(epoch) - retention) * slotsPerEpoch)

	pruned := s.store.Prune(func(slot phase0.Slot) bool {
		return slot < cutoff
	})
	if pruned > 0 {
		s.log.WithFields(logrus.Fields{
			"epoch":  epoch,
			"cutoff": cutoff,
			"pruned": pruned,
		}).Debug("Pruned past slot plans")
	}

	// Freeze markers only matter around the current slot: edits are already
	// rejected for slots <= currentSlot, so markers for past slots are dead
	// weight. Keep the previous epoch as a safety margin.
	markerCutoff := phase0.Slot(uint64(epoch-1) * slotsPerEpoch)

	s.mu.Lock()
	defer s.mu.Unlock()

	for slot := range s.frozen {
		if slot < markerCutoff {
			delete(s.frozen, slot)
		}
	}
}

func sortPlansBySlot(plans []*SlotPlan) {
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].Slot < plans[j].Slot
	})
}

func sortChangeEvent(event *PlanChangeEvent) {
	order := make([]int, len(event.Slots))
	for i := range order {
		order[i] = i
	}

	sort.Slice(order, func(i, j int) bool {
		return event.Slots[order[i]] < event.Slots[order[j]]
	})

	slots := make([]uint64, len(event.Slots))
	plans := make([]*SlotPlan, len(event.Plans))

	for target, source := range order {
		slots[target] = event.Slots[source]
		plans[target] = event.Plans[source]
	}

	event.Slots = slots
	event.Plans = plans
}
