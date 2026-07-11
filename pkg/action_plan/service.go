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

// PlanService owns the sparse per-slot action plan store, its freeze state and
// its persistence. It is the single writer; all reads return deep copies.
type PlanService struct {
	cfg      *config.Config
	chainSvc chain.Service
	store    *memstore.Store[phase0.Slot, *SlotPlan]

	mu     sync.Mutex
	frozen map[phase0.Slot]*FrozenPlan

	changes utils.Dispatcher[*PlanChangeEvent]

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
		frozen:   make(map[phase0.Slot]*FrozenPlan, 64),
		log:      log.WithField("component", "action-plan"),
	}
}

// SetPersistence attaches the state-db backed persistence (kv_store namespace
// "slot_plans") and rehydrates previously stored plans.
func (s *PlanService) SetPersistence(ctx context.Context, stateDB *db.Database) {
	s.store.SetPersistence(ctx, db.NewKVPersistence(stateDB, Namespace, PlanCodec{}), s.log)
}

// Start launches the pruning loop (driven by epoch transitions).
func (s *PlanService) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	epochSub := s.chainSvc.SubscribeEpochStats()

	s.wg.Add(1)

	go s.run(epochSub)

	s.log.Info("Action plan service started")

	return nil
}

// Stop terminates the pruning loop and flushes the store. Must be called
// before the state-db closes.
func (s *PlanService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()
	s.store.Stop()

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

// Get returns a deep copy of the slot's plan, or nil when none exists.
func (s *PlanService) Get(slot phase0.Slot) *SlotPlan {
	plan, ok := s.store.Get(slot)
	if !ok {
		return nil
	}

	return plan.Clone()
}

// GetRange returns deep copies of all plans within [minSlot, maxSlot],
// slot-ascending.
func (s *PlanService) GetRange(minSlot, maxSlot phase0.Slot) []*SlotPlan {
	entries := s.store.Entries()
	plans := make([]*SlotPlan, 0, len(entries))

	for slot, plan := range entries {
		if slot >= minSlot && slot <= maxSlot {
			plans = append(plans, plan.Clone())
		}
	}

	sortPlansBySlot(plans)

	return plans
}

// Freeze resolves and records the slot's immutable execution snapshot. The
// first caller wins; every later caller (and every other decision point of
// the same slot) receives the identical snapshot. From this moment on the
// slot's plan can no longer be edited.
func (s *PlanService) Freeze(slot phase0.Slot) *FrozenPlan {
	s.mu.Lock()
	defer s.mu.Unlock()

	if frozen, ok := s.frozen[slot]; ok {
		return frozen
	}

	var plan *SlotPlan
	if stored, ok := s.store.Get(slot); ok {
		plan = stored.Clone()
	}

	fork := s.chainSvc.ActiveForkAtEpoch(s.chainSvc.GetEpochOfSlot(slot))
	frozen := resolveFrozenPlan(slot, plan, s.cfg, fork, time.Now())
	s.frozen[slot] = frozen

	return frozen
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
