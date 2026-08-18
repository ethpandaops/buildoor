package payload_builder

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// buildTarget is one payload build the slot's build pass will run: which
// candidate it is (empty when unclassified), the attributes to build from,
// and whether those attributes were synthesized locally.
type buildTarget struct {
	candidate chain.CandidateKey
	attrs     *beacon.PayloadAttributesEvent
	derived   bool
}

// candidateBuildOrder lists candidate keys in the order a sequential build
// pass runs them: the canonical candidate first, so it starts at exactly the
// configured build start time and is never delayed by speculative builds
// (payload attributes typically arrive barely one build ahead of the slot).
// The speculative builds follow; the EL head they leave behind is transient —
// the beacon node's own forkchoiceUpdated calls drive it back to the
// canonical chain, and the next slot's canonical build targets it again.
var candidateBuildOrder = []chain.CandidateKey{
	chain.CandidateParentFull,
	chain.CandidateParentEmpty,
	chain.CandidateGrandparentFull,
	chain.CandidateGrandparentEmpty,
}

// resolveBuildTargets assembles the slot's build list from the configured
// candidate policy, the chain view's candidate tuples, the received
// attribute variants and local synthesis. CL-received variants whose parent
// tuple matches no known candidate are appended unclassified (the beacon
// node's own suggestion is always honored).
func (s *Service) resolveBuildTargets(slot phase0.Slot) []*buildTarget {
	variants := make([]*beacon.PayloadAttributesEvent, 0, 4)
	for _, event := range s.clClient.Events().GetPayloadAttributesVariants(slot) {
		variants = append(variants, s.sanitizeAttributes(event))
	}

	candidates := s.resolveChainCandidates(slot)

	candidateByKey := make(map[chain.CandidateKey]*chain.CandidateParent, len(candidates))
	candidateByTuple := make(map[beacon.AttrParentKey]*chain.CandidateParent, len(candidates))

	for _, candidate := range candidates {
		tuple := beacon.AttrParentKey{Root: candidate.ParentBlockRoot, Hash: candidate.ParentBlockHash}
		candidateByKey[candidate.Key] = candidate
		candidateByTuple[tuple] = candidate
	}

	variantByTuple := make(map[beacon.AttrParentKey]*beacon.PayloadAttributesEvent, len(variants))
	for _, event := range variants {
		variantByTuple[beacon.AttrParentKeyOf(event)] = event
	}

	targets := make([]*buildTarget, 0, len(candidateBuildOrder)+len(variants))
	covered := make(map[beacon.AttrParentKey]bool, len(candidateBuildOrder)+len(variants))

	for _, key := range candidateBuildOrder {
		candidate := candidateByKey[key]
		if candidate == nil {
			continue
		}

		mode := s.candidateMode(slot, key)
		if mode == config.CandidateModeNever || mode == "" {
			continue
		}

		if mode == config.CandidateModeAuto && !s.candidateAutoSignal(candidate) {
			continue
		}

		tuple := beacon.AttrParentKey{Root: candidate.ParentBlockRoot, Hash: candidate.ParentBlockHash}

		attrs := variantByTuple[tuple]
		derived := false

		if attrs == nil {
			synthesized, err := s.synthesizeCandidateAttributes(slot, candidate)
			if err != nil {
				s.log.WithError(err).WithFields(logrus.Fields{
					"slot":      slot,
					"candidate": key,
				}).Info("Cannot synthesize candidate attributes, skipping candidate")

				continue
			}

			attrs = synthesized
			derived = true
		}

		targets = append(targets, &buildTarget{candidate: key, attrs: attrs, derived: derived})
		covered[tuple] = true
	}

	// CL variants outside the covered set: build unclassified ones as-is
	// (unknown branches, head races — the beacon node's own suggestion is
	// honored), but variants classifying to a policy-suppressed candidate
	// stay suppressed.
	for _, event := range variants {
		tuple := beacon.AttrParentKeyOf(event)
		if covered[tuple] {
			continue
		}

		target := &buildTarget{attrs: event}
		if candidate := candidateByTuple[tuple]; candidate != nil {
			if s.candidateMode(slot, candidate.Key) == config.CandidateModeNever {
				s.log.WithFields(logrus.Fields{
					"slot":      slot,
					"candidate": candidate.Key,
				}).Debug("Suppressing received attribute variant (policy: never)")

				continue
			}

			target.candidate = candidate.Key
		}

		targets = append(targets, target)
		covered[tuple] = true
	}

	return targets
}

// candidateMode returns the slot's effective build mode for a candidate key:
// the frozen plan's resolved candidate policy (global config merged with the
// slot's plan overrides), falling back to the live config when the frozen
// snapshot carries none.
func (s *Service) candidateMode(slot phase0.Slot, key chain.CandidateKey) string {
	if frozen := s.planSvc.Freeze(slot); frozen.Build != nil && frozen.Build.CandidateModes != nil {
		if mode, ok := frozen.Build.CandidateModes[string(key)]; ok {
			return mode
		}
	}

	return s.cfgSvc.Current().Build.CandidateMode(string(key))
}

// resolveChainCandidates fetches the chain view's build-parent candidates for
// the slot (nil when the chain view has no usable head yet).
func (s *Service) resolveChainCandidates(slot phase0.Slot) []*chain.CandidateParent {
	headTracker := s.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(s.ctx, attrSanitizeTimeout)
	defer cancel()

	candidates, err := headTracker.ResolveCandidates(ctx, slot)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Debug("Cannot resolve build candidates")
		return nil
	}

	return candidates
}

// classifyCandidate returns the candidate key matching the event's parent
// tuple relative to the current chain view ("" when unknown).
func (s *Service) classifyCandidate(event *beacon.PayloadAttributesEvent) chain.CandidateKey {
	for _, candidate := range s.resolveChainCandidates(event.ProposalSlot) {
		if candidate.ParentBlockRoot == event.ParentBlockRoot &&
			candidate.ParentBlockHash == event.ParentBlockHash {
			return candidate.Key
		}
	}

	return ""
}

// candidateAutoSignal decides whether an auto-mode candidate should build,
// from live chain signals: the parent payload's reveal status for the
// full/empty choice, and head-vote weakness of the head block for the reorg
// candidates.
func (s *Service) candidateAutoSignal(candidate *chain.CandidateParent) bool {
	switch candidate.Key {
	case chain.CandidateParentFull:
		// Pointless only when the parent payload is known withheld.
		return candidate.ParentPayloadStatus != chain.PayloadStatusEmpty

	case chain.CandidateParentEmpty:
		// Needed unless the parent payload is confirmed revealed.
		return candidate.ParentPayloadStatus != chain.PayloadStatusRevealed

	case chain.CandidateGrandparentFull:
		return s.headContested()

	case chain.CandidateGrandparentEmpty:
		return s.headContested() && candidate.ParentPayloadStatus != chain.PayloadStatusRevealed

	default:
		return false
	}
}

// headContested reports whether the current head block's attestation
// participation is below the configured weak-head threshold — the signal that
// the next proposer may reorg it out.
func (s *Service) headContested() bool {
	threshold := s.cfgSvc.Current().Build.AutoWeakHeadPct
	if threshold == 0 {
		return false
	}

	headTracker := s.chainSvc.GetHeadTracker()
	voteTracker := s.chainSvc.GetHeadVoteTracker()

	if headTracker == nil || voteTracker == nil {
		return false
	}

	head := headTracker.CurrentHead()
	if head == nil {
		return false
	}

	update, ok := voteTracker.GetParticipation(head.Slot, head.Root)
	if !ok {
		return false
	}

	return update.ParticipationPct < float64(threshold)
}

// synthesizeCandidateAttributes derives buildable attributes for a candidate
// no CL variant covers, from the cached attribute variants of this and the
// parent block's slot:
//
//   - parent_empty takes a same-beacon-parent variant of this slot as base,
//     swaps the execution parent to the candidate's, and sources the
//     withdrawals from the parent block's own slot attributes (the expected
//     withdrawals list is unchanged when the parent payload is withheld).
//   - grandparent candidates re-target the parent block's slot attributes
//     (whose parent already is the grandparent) at this slot: proposal slot,
//     timestamp and proposer advance, everything else carries over. For the
//     empty variant the execution parent and withdrawals are swapped like
//     above. Synthesis across an epoch boundary is refused (the carried
//     prev_randao would be stale).
//   - parent_full cannot be synthesized: it requires a freshly computed
//     withdrawals list only the beacon node has.
func (s *Service) synthesizeCandidateAttributes(
	slot phase0.Slot, candidate *chain.CandidateParent,
) (*beacon.PayloadAttributesEvent, error) {
	events := s.clClient.Events()

	switch candidate.Key {
	case chain.CandidateParentEmpty:
		base := s.findVariantWithParentRoot(slot, candidate.ParentBlockRoot)
		if base == nil {
			return nil, fmt.Errorf("no attribute variant with the same beacon parent")
		}

		withdrawalsSrc := events.GetLatestPayloadAttributes(candidate.ParentSlot)
		if withdrawalsSrc == nil {
			return nil, fmt.Errorf("no attributes for the parent block's slot %d "+
				"(withdrawals source)", candidate.ParentSlot)
		}

		synthesized := *base
		synthesized.ParentBlockHash = candidate.ParentBlockHash
		synthesized.ParentBlockNumber = candidate.ELParentNumber
		synthesized.Withdrawals = withdrawalsSrc.Withdrawals

		return &synthesized, nil

	case chain.CandidateGrandparentFull, chain.CandidateGrandparentEmpty:
		// The parent block's slot attributes already build on the
		// grandparent; the base must match the candidate's beacon parent.
		base := s.findVariantWithParentRoot(candidate.ParentSlot+1, candidate.ParentBlockRoot)
		if base == nil {
			base = s.findLatestVariantWithParentRoot(candidate.ParentBlockRoot, slot)
		}

		if base == nil {
			return nil, fmt.Errorf("no attribute variant building on the grandparent")
		}

		spec := s.chainSvc.GetChainSpec()
		if uint64(slot)/spec.SlotsPerEpoch != uint64(base.ProposalSlot)/spec.SlotsPerEpoch {
			return nil, fmt.Errorf("base attributes from slot %d cross an epoch boundary "+
				"(stale prev_randao)", base.ProposalSlot)
		}

		synthesized := *base
		synthesized.ProposalSlot = slot
		synthesized.Timestamp = base.Timestamp +
			uint64(slot-base.ProposalSlot)*uint64(spec.SecondsPerSlot.Seconds())

		if proposer, ok := s.lookupProposer(slot); ok {
			synthesized.ProposerIndex = proposer
		}

		if candidate.Key == chain.CandidateGrandparentEmpty {
			withdrawalsSrc := events.GetLatestPayloadAttributes(candidate.ParentSlot)
			if withdrawalsSrc == nil {
				return nil, fmt.Errorf("no attributes for the grandparent block's slot %d "+
					"(withdrawals source)", candidate.ParentSlot)
			}

			synthesized.ParentBlockHash = candidate.ParentBlockHash
			synthesized.ParentBlockNumber = candidate.ELParentNumber
			synthesized.Withdrawals = withdrawalsSrc.Withdrawals
		}

		return &synthesized, nil

	default:
		return nil, fmt.Errorf("candidate %s requires beacon-node attributes", candidate.Key)
	}
}

// findVariantWithParentRoot returns a cached attribute variant of the given
// slot whose beacon parent matches root (any execution parent), or nil.
func (s *Service) findVariantWithParentRoot(
	slot phase0.Slot, root phase0.Root,
) *beacon.PayloadAttributesEvent {
	for _, event := range s.clClient.Events().GetPayloadAttributesVariants(slot) {
		if event.ParentBlockRoot == root {
			return s.sanitizeAttributes(event)
		}
	}

	return nil
}

// findLatestVariantWithParentRoot searches backwards from beforeSlot for the
// newest cached variant building on the given beacon parent.
func (s *Service) findLatestVariantWithParentRoot(
	root phase0.Root, beforeSlot phase0.Slot,
) *beacon.PayloadAttributesEvent {
	for lookback := phase0.Slot(1); lookback <= attrFallbackLookback && lookback <= beforeSlot; lookback++ {
		if event := s.findVariantWithParentRoot(beforeSlot-lookback, root); event != nil {
			return event
		}
	}

	return nil
}

// onDemandPollInterval paces waiting for an in-flight build when an
// on-demand request races the slot's regular build pass.
const onDemandPollInterval = 50 * time.Millisecond

// BuildCandidateOnDemand returns the slot's payload for the given parent
// tuple, building it on the fly when no candidate covers it yet (used by the
// Builder API to serve a proposer requesting a legal but unbuilt parent).
// Bounded by ctx; a tuple whose attributes cannot be resolved (unknown to the
// chain view and no received variant) fails.
func (s *Service) BuildCandidateOnDemand(
	ctx context.Context, slot phase0.Slot, parentRoot phase0.Root, parentHash phase0.Hash32,
) (*Payload, error) {
	tuple := beacon.AttrParentKey{Root: parentRoot, Hash: parentHash}

	if payload := s.payloadCache.GetVariant(slot, tuple); payload != nil {
		return payload, nil
	}

	target := s.resolveOnDemandTarget(slot, tuple)
	if target == nil {
		return nil, fmt.Errorf("cannot resolve build attributes for parent %#x/%#x", parentRoot, parentHash)
	}

	// Synchronous build; a concurrent build of the same tuple (regular pass)
	// makes this a no-op and the wait below picks up its result.
	s.executeCandidateBuild(slot, target)

	for {
		if payload := s.payloadCache.GetVariant(slot, tuple); payload != nil {
			return payload, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("on-demand build did not complete: %w", ctx.Err())
		case <-time.After(onDemandPollInterval):
		}
	}
}

// resolveOnDemandTarget constructs the build target for an explicitly
// requested parent tuple: a received attribute variant when present,
// otherwise synthesis for a chain-view candidate matching the tuple.
func (s *Service) resolveOnDemandTarget(slot phase0.Slot, tuple beacon.AttrParentKey) *buildTarget {
	if variant := s.clClient.Events().GetPayloadAttributesVariant(slot, tuple); variant != nil {
		sanitized := s.sanitizeAttributes(variant)
		if beacon.AttrParentKeyOf(sanitized) == tuple {
			return &buildTarget{candidate: s.classifyCandidate(sanitized), attrs: sanitized}
		}
	}

	for _, candidate := range s.resolveChainCandidates(slot) {
		if candidate.ParentBlockRoot != tuple.Root || candidate.ParentBlockHash != tuple.Hash {
			continue
		}

		attrs, err := s.synthesizeCandidateAttributes(slot, candidate)
		if err != nil {
			s.log.WithError(err).WithFields(logrus.Fields{
				"slot":      slot,
				"candidate": candidate.Key,
			}).Info("Cannot synthesize attributes for on-demand build")

			return nil
		}

		return &buildTarget{candidate: candidate.Key, attrs: attrs, derived: true}
	}

	return nil
}

// candidateBuildTime returns the EL build time for a target. Parallel builds
// all run inside the same designated build window, so every candidate gets
// the full build time; only serialized builds shorten the speculative ones to
// fit them alongside the canonical build.
func (s *Service) candidateBuildTime(target *buildTarget) uint64 {
	cfg := s.cfgSvc.Current()

	if cfg.Build.Parallel {
		return cfg.PayloadBuildTime
	}

	if target.candidate != chain.CandidateParentFull && target.candidate != "" &&
		cfg.Build.SpeculativeBuildTimeMs != 0 {
		return cfg.Build.SpeculativeBuildTimeMs
	}

	return cfg.PayloadBuildTime
}

// slotEndTime returns the wall-clock end of a slot (the bound for late
// candidate activation).
func (s *Service) slotEndTime(slot phase0.Slot) time.Time {
	return s.chainSvc.SlotToTime(slot + 1)
}
