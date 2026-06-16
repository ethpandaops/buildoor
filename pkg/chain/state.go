package chain

import (
	"context"
	"fmt"

	eth2client "github.com/ethpandaops/go-eth2-client"
	"github.com/ethpandaops/go-eth2-client/api"
	"github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// EpochStats holds cached statistics for an epoch computed from beacon state.
type EpochStats struct {
	Version        version.DataVersion
	Epoch          phase0.Epoch
	FinalizedEpoch phase0.Epoch
	StateSlot      phase0.Slot

	// Validator data
	ActiveValidators  uint64
	ValidatorCount    uint64
	ActiveIndices     []phase0.ValidatorIndex
	EffectiveBalances []uint32 // Effective balance in full ETH units

	// Builder data (for lifecycle management, Gloas only).
	// Populated (possibly empty) for any Gloas+ state; nil for pre-Gloas epochs.
	Builders []*BuilderInfo

	// Pre-computed duties
	RandaoMix      phase0.Hash32
	NextRandaoMix  phase0.Hash32
	ProposerDuties []phase0.ValidatorIndex // [slot_index] -> validator index
	AttesterDuties [][][]ActiveIndiceIndex // [slot_index][committee_index][member] -> active indice index
	PtcDuties      [][]ActiveIndiceIndex   // [slot_index][ptc_member] -> active indice index (Gloas only)
}

// BuilderInfo represents information about a builder from the beacon state.
type BuilderInfo struct {
	Index             uint64
	Pubkey            phase0.BLSPubKey
	Balance           uint64
	Active            bool
	DepositEpoch      uint64
	WithdrawableEpoch uint64
	PendingPayments   uint64 // Sum of pending payments from BuilderPendingPayments in state
}

// fetchEpochStats fetches the beacon state and computes epoch statistics.
func (s *service) fetchEpochStats(
	ctx context.Context,
	stateID string,
	epoch phase0.Epoch,
) (*EpochStats, error) {
	provider, ok := s.clClient.GetRawClient().(eth2client.BeaconStateProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support beacon state provider")
	}

	resp, err := provider.AgnosticBeaconState(ctx, &api.BeaconStateOpts{
		State: stateID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get beacon state: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("beacon state response is nil")
	}

	validatorPubkeys := s.validatorIndexCache
	if cap(validatorPubkeys) < len(resp.Data.Validators) {
		validatorPubkeys = make([]phase0.BLSPubKey, len(resp.Data.Validators))
	} else {
		validatorPubkeys = validatorPubkeys[:len(resp.Data.Validators)]
	}

	for i, v := range resp.Data.Validators {
		validatorPubkeys[i] = v.PublicKey
	}

	s.validatorIndexCache = validatorPubkeys

	return s.computeEpochStats(resp.Data, epoch)
}

// computeEpochStats extracts relevant statistics from the beacon state and computes duties.
func (s *service) computeEpochStats(state *all.BeaconState, epoch phase0.Epoch) (*EpochStats, error) {
	stats := &EpochStats{
		Version:   state.Version,
		Epoch:     epoch,
		StateSlot: state.Slot,
	}

	if state.FinalizedCheckpoint != nil {
		stats.FinalizedEpoch = state.FinalizedCheckpoint.Epoch
	}

	// Build active indices and effective balances
	validators := state.Validators
	stats.ValidatorCount = uint64(len(validators))
	stats.ActiveIndices = make([]phase0.ValidatorIndex, 0, len(validators))
	stats.EffectiveBalances = make([]uint32, 0, len(validators))

	for i, v := range validators {
		if isActiveValidator(v, epoch) {
			stats.ActiveIndices = append(stats.ActiveIndices, phase0.ValidatorIndex(i))
			stats.EffectiveBalances = append(stats.EffectiveBalances, uint32(v.EffectiveBalance/EtherGweiFactor))
		}
	}

	stats.ActiveValidators = uint64(len(stats.ActiveIndices))

	// Create DutyState for duty calculations
	randaoMixes := state.RANDAOMixes
	dutyState := &DutyState{
		GetRandaoMixes: func() []phase0.Root {
			return randaoMixes
		},
		GetActiveCount: func() uint64 {
			return stats.ActiveValidators
		},
		GetEffectiveBalance: func(index ActiveIndiceIndex) phase0.Gwei {
			return phase0.Gwei(stats.EffectiveBalances[index]) * EtherGweiFactor
		},
	}

	// Extract proposer duties from state lookahead (Fulu+ only).
	// Proposer selection depends on parent epoch effective balances which are not
	// available in the post-transition state, so we rely on the pre-computed lookahead.
	firstSlot := phase0.Slot(uint64(epoch) * s.chainSpec.SlotsPerEpoch)

	if len(state.ProposerLookahead) > 0 {
		stats.ProposerDuties = state.ProposerLookahead[:s.chainSpec.SlotsPerEpoch]
	}

	// Compute attester duties
	attesterDuties, err := getAttesterDuties(s.chainSpec, dutyState, epoch)
	if err != nil {
		s.log.WithError(err).WithField("epoch", epoch).Warn("Failed to compute attester duties")
	}

	stats.AttesterDuties = attesterDuties

	// Compute PTC duties (Gloas only)
	if state.Version >= version.DataVersionGloas && s.chainSpec.PtcSize > 0 && attesterDuties != nil {
		stats.PtcDuties = make([][]ActiveIndiceIndex, s.chainSpec.SlotsPerEpoch)

		for slotIndex := uint64(0); slotIndex < s.chainSpec.SlotsPerEpoch; slotIndex++ {
			slot := firstSlot + phase0.Slot(slotIndex)

			ptc, ptcErr := getPtcDuties(s.chainSpec, dutyState, attesterDuties[slotIndex], slot)
			if ptcErr != nil {
				s.log.WithError(ptcErr).WithField("slot", slot).Warn("Failed to compute PTC duties")
			}

			stats.PtcDuties[slotIndex] = ptc
		}
	}

	// Store cached RandaoMix values
	if dutyState.RandaoMix != nil {
		stats.RandaoMix = *dutyState.RandaoMix
	}

	if dutyState.NextRandaoMix != nil {
		stats.NextRandaoMix = *dutyState.NextRandaoMix
	}

	if state.Version >= version.DataVersionGloas {
		stats.Builders = extractBuilders(state.Builders)

		// Sum pending payments per builder from state
		applyPendingPayments(stats.Builders, state.BuilderPendingPayments)
	}

	return stats, nil
}

// isActiveValidator checks if a validator is active at the given epoch.
func isActiveValidator(validator *phase0.Validator, epoch phase0.Epoch) bool {
	return validator.ActivationEpoch <= epoch && epoch < validator.ExitEpoch
}

// extractBuilders extracts builder information from the beacon state.
func extractBuilders(builders []*gloas.Builder) []*BuilderInfo {
	if builders == nil {
		return nil
	}

	result := make([]*BuilderInfo, len(builders))
	for i, builder := range builders {
		result[i] = builderToInfo(uint64(i), builder)
	}

	return result
}

// builderToInfo converts a gloas.Builder to BuilderInfo.
func builderToInfo(index uint64, builder *gloas.Builder) *BuilderInfo {
	if builder == nil {
		return &BuilderInfo{Index: index}
	}

	info := &BuilderInfo{
		Index:             index,
		Pubkey:            builder.PublicKey,
		Balance:           uint64(builder.Balance),
		DepositEpoch:      uint64(builder.DepositEpoch),
		WithdrawableEpoch: uint64(builder.WithdrawableEpoch),
	}

	// Determine if active (not yet exited)
	info.Active = info.WithdrawableEpoch == FarFutureEpoch

	return info
}

// applyPendingPayments sums pending payment amounts from the beacon state per builder.
func applyPendingPayments(builders []*BuilderInfo, payments []*gloas.BuilderPendingPayment) {
	if len(payments) == 0 || len(builders) == 0 {
		return
	}

	for _, payment := range payments {
		if payment == nil || payment.Withdrawal == nil || payment.Withdrawal.Amount == 0 {
			continue
		}

		idx := uint64(payment.Withdrawal.BuilderIndex)
		if idx >= uint64(len(builders)) {
			continue
		}

		builders[idx].PendingPayments += uint64(payment.Withdrawal.Amount)
	}
}
