package chain

import (
	"context"
	"fmt"

	eth2client "github.com/attestantio/go-eth2-client"
	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

const farFutureEpoch = uint64(0xFFFFFFFFFFFFFFFF)

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

	resp, err := provider.BeaconState(ctx, &api.BeaconStateOpts{
		State: stateID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get beacon state: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("beacon state response is nil")
	}

	return s.computeEpochStats(resp.Data, epoch)
}

// computeEpochStats extracts relevant statistics from the beacon state and computes duties.
func (s *service) computeEpochStats(state *spec.VersionedBeaconState, epoch phase0.Epoch) (*EpochStats, error) {
	stats := &EpochStats{
		Epoch: epoch,
	}

	// Extract common data based on state version
	var (
		validators        []*phase0.Validator
		randaoMixes       []phase0.Root
		proposerLookahead []phase0.ValidatorIndex
		isGloas           bool
	)

	switch state.Version {
	case spec.DataVersionPhase0:
		if state.Phase0 == nil {
			return nil, fmt.Errorf("phase0 state is nil")
		}

		stats.StateSlot = state.Phase0.Slot
		validators = state.Phase0.Validators
		randaoMixes = state.Phase0.RANDAOMixes

	case spec.DataVersionAltair:
		if state.Altair == nil {
			return nil, fmt.Errorf("altair state is nil")
		}

		stats.StateSlot = state.Altair.Slot
		validators = state.Altair.Validators
		randaoMixes = state.Altair.RANDAOMixes

	case spec.DataVersionBellatrix:
		if state.Bellatrix == nil {
			return nil, fmt.Errorf("bellatrix state is nil")
		}

		stats.StateSlot = state.Bellatrix.Slot
		validators = state.Bellatrix.Validators
		randaoMixes = state.Bellatrix.RANDAOMixes

	case spec.DataVersionCapella:
		if state.Capella == nil {
			return nil, fmt.Errorf("capella state is nil")
		}

		stats.StateSlot = state.Capella.Slot
		validators = state.Capella.Validators
		randaoMixes = state.Capella.RANDAOMixes

	case spec.DataVersionDeneb:
		if state.Deneb == nil {
			return nil, fmt.Errorf("deneb state is nil")
		}

		stats.StateSlot = state.Deneb.Slot
		validators = state.Deneb.Validators
		randaoMixes = state.Deneb.RANDAOMixes

	case spec.DataVersionElectra:
		if state.Electra == nil {
			return nil, fmt.Errorf("electra state is nil")
		}

		stats.StateSlot = state.Electra.Slot
		validators = state.Electra.Validators
		randaoMixes = state.Electra.RANDAOMixes

	case spec.DataVersionFulu:
		if state.Fulu == nil {
			return nil, fmt.Errorf("fulu state is nil")
		}

		stats.StateSlot = state.Fulu.Slot
		validators = state.Fulu.Validators
		randaoMixes = state.Fulu.RANDAOMixes
		proposerLookahead = state.Fulu.ProposerLookahead

	case spec.DataVersionGloas:
		if state.Gloas == nil {
			return nil, fmt.Errorf("gloas state is nil")
		}

		stats.StateSlot = state.Gloas.Slot
		validators = state.Gloas.Validators
		randaoMixes = state.Gloas.RANDAOMixes
		proposerLookahead = state.Gloas.ProposerLookahead
		isGloas = true
		stats.IsGloas = true

		stats.Builders = extractBuilders(state.Gloas.Builders)
		stats.BuildersLoaded = true

	default:
		return nil, fmt.Errorf("unsupported state version: %s", state.Version)
	}

	// Build active indices and effective balances
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

	if len(proposerLookahead) > 0 {
		stateEpoch := phase0.Epoch(uint64(stats.StateSlot) / s.chainSpec.SlotsPerEpoch)
		offset := uint64(epoch-stateEpoch) * s.chainSpec.SlotsPerEpoch

		if offset+s.chainSpec.SlotsPerEpoch <= uint64(len(proposerLookahead)) {
			stats.ProposerDuties = proposerLookahead[offset : offset+s.chainSpec.SlotsPerEpoch]
		}
	}

	// Compute attester duties
	attesterDuties, err := getAttesterDuties(s.chainSpec, dutyState, epoch)
	if err != nil {
		s.log.WithError(err).WithField("epoch", epoch).Warn("Failed to compute attester duties")
	}

	stats.AttesterDuties = attesterDuties

	// Compute PTC duties (Gloas only)
	if isGloas && s.chainSpec.PtcSize > 0 && attesterDuties != nil {
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
	info.Active = info.WithdrawableEpoch == farFutureEpoch

	return info
}
