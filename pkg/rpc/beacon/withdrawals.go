package beacon

import (
	"fmt"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/electra"
	"github.com/attestantio/go-eth2-client/spec/fulu"
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// Withdrawal constants from the consensus spec.
const (
	// MAX_WITHDRAWALS_PER_PAYLOAD is the maximum number of withdrawals per payload.
	MaxWithdrawalsPerPayload = 16

	// MAX_VALIDATORS_PER_WITHDRAWALS_SWEEP is the max validators checked per sweep.
	MaxValidatorsPerWithdrawalsSweep = 16384

	// MAX_PENDING_PARTIALS_PER_WITHDRAWALS_SWEEP for partial withdrawals.
	MaxPendingPartialsPerWithdrawalsSweep = 8

	// MAX_BUILDERS_PER_WITHDRAWALS_SWEEP for builder sweep (Gloas).
	MaxBuildersPerWithdrawalsSweep = 16384

	// MIN_ACTIVATION_BALANCE is 32 ETH in gwei.
	MinActivationBalance = 32_000_000_000

	// MAX_EFFECTIVE_BALANCE_ELECTRA is 2048 ETH in gwei.
	MaxEffectiveBalanceElectra = 2_048_000_000_000

	// FAR_FUTURE_EPOCH represents a far future epoch.
	FarFutureEpoch = phase0.Epoch(0xFFFFFFFFFFFFFFFF)

	// ETH1_ADDRESS_WITHDRAWAL_PREFIX is 0x01.
	ETH1AddressWithdrawalPrefix = 0x01

	// COMPOUNDING_WITHDRAWAL_PREFIX is 0x02.
	CompoundingWithdrawalPrefix = 0x02
)

// ExpectedWithdrawals holds the calculated withdrawals and processing counts.
type ExpectedWithdrawals struct {
	Withdrawals                      []*capella.Withdrawal
	ProcessedBuilderWithdrawalsCount uint64 // Gloas only
	ProcessedPartialWithdrawalsCount uint64
	ProcessedBuildersSweepCount      uint64 // Gloas only
	ProcessedValidatorsSweepCount    uint64
}

// WithdrawalCalculator calculates expected withdrawals from beacon state.
type WithdrawalCalculator struct {
	slotsPerEpoch uint64
}

// NewWithdrawalCalculator creates a new withdrawal calculator.
func NewWithdrawalCalculator(slotsPerEpoch uint64) *WithdrawalCalculator {
	return &WithdrawalCalculator{
		slotsPerEpoch: slotsPerEpoch,
	}
}

// GetExpectedWithdrawals calculates the expected withdrawals for a new block.
// It uses the state from the parent block to determine what withdrawals should be in the next block.
// For Gloas, it also checks if the parent block had a full payload (affects withdrawal processing).
func (c *WithdrawalCalculator) GetExpectedWithdrawals(
	state *spec.VersionedBeaconState,
) (*ExpectedWithdrawals, error) {
	switch state.Version {
	case spec.DataVersionElectra:
		if state.Electra == nil {
			return nil, fmt.Errorf("electra state is nil")
		}

		return c.getExpectedWithdrawalsElectra(state.Electra)

	case spec.DataVersionFulu:
		if state.Fulu == nil {
			return nil, fmt.Errorf("fulu state is nil")
		}

		return c.getExpectedWithdrawalsFulu(state.Fulu)

	case spec.DataVersionGloas:
		if state.Gloas == nil {
			return nil, fmt.Errorf("gloas state is nil")
		}

		return c.getExpectedWithdrawalsGloas(state.Gloas)

	default:
		return nil, fmt.Errorf("unsupported state version: %s", state.Version)
	}
}

// getExpectedWithdrawalsElectra calculates withdrawals for Electra state.
func (c *WithdrawalCalculator) getExpectedWithdrawalsElectra(
	state *electra.BeaconState,
) (*ExpectedWithdrawals, error) {
	withdrawalIndex := state.NextWithdrawalIndex
	withdrawals := make([]*capella.Withdrawal, 0, MaxWithdrawalsPerPayload)

	// Get partial withdrawals
	partialWithdrawals, withdrawalIndex, processedPartialCount := c.getPendingPartialWithdrawalsElectra(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, partialWithdrawals...)

	// Get validators sweep withdrawals
	validatorsSweepWithdrawals, _, processedValidatorsSweepCount := c.getValidatorsSweepWithdrawalsElectra(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, validatorsSweepWithdrawals...)

	return &ExpectedWithdrawals{
		Withdrawals:                      withdrawals,
		ProcessedPartialWithdrawalsCount: processedPartialCount,
		ProcessedValidatorsSweepCount:    processedValidatorsSweepCount,
	}, nil
}

// getExpectedWithdrawalsFulu calculates withdrawals for Fulu state.
// Fulu uses the same withdrawal logic as Electra.
func (c *WithdrawalCalculator) getExpectedWithdrawalsFulu(
	state *fulu.BeaconState,
) (*ExpectedWithdrawals, error) {
	withdrawalIndex := state.NextWithdrawalIndex
	withdrawals := make([]*capella.Withdrawal, 0, MaxWithdrawalsPerPayload)

	// Get partial withdrawals
	partialWithdrawals, withdrawalIndex, processedPartialCount := c.getPendingPartialWithdrawalsFulu(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, partialWithdrawals...)

	// Get validators sweep withdrawals
	validatorsSweepWithdrawals, _, processedValidatorsSweepCount := c.getValidatorsSweepWithdrawalsFulu(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, validatorsSweepWithdrawals...)

	return &ExpectedWithdrawals{
		Withdrawals:                      withdrawals,
		ProcessedPartialWithdrawalsCount: processedPartialCount,
		ProcessedValidatorsSweepCount:    processedValidatorsSweepCount,
	}, nil
}

// getExpectedWithdrawalsGloas calculates withdrawals for Gloas state.
// Note: For Gloas, withdrawals only process if the parent block had a full payload.
// If the parent payload was missed, we use the PayloadExpectedWithdrawals from state
// (those were prepared for the missed payload and should go into the next block).
func (c *WithdrawalCalculator) getExpectedWithdrawalsGloas(
	state *gloas.BeaconState,
) (*ExpectedWithdrawals, error) {
	// Check if parent block was full (had payload delivered)
	// If not, use the PayloadExpectedWithdrawals from state - those were prepared
	// for the missed payload and should now go into this block
	if !c.isParentBlockFull(state) {
		// Use the expected withdrawals from state - they were prepared for the
		// missed parent payload and should be included in this block instead
		return &ExpectedWithdrawals{
			Withdrawals: state.PayloadExpectedWithdrawals,
		}, nil
	}

	withdrawalIndex := state.NextWithdrawalIndex
	withdrawals := make([]*capella.Withdrawal, 0, MaxWithdrawalsPerPayload)

	// Get builder withdrawals (Gloas-specific)
	builderWithdrawals, withdrawalIndex, processedBuilderCount := c.getBuilderWithdrawals(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, builderWithdrawals...)

	// Get partial withdrawals
	partialWithdrawals, withdrawalIndex, processedPartialCount := c.getPendingPartialWithdrawalsGloas(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, partialWithdrawals...)

	// Get builders sweep withdrawals (Gloas-specific)
	buildersSweepWithdrawals, withdrawalIndex, processedBuildersSweepCount := c.getBuildersSweepWithdrawals(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, buildersSweepWithdrawals...)

	// Get validators sweep withdrawals
	validatorsSweepWithdrawals, _, processedValidatorsSweepCount := c.getValidatorsSweepWithdrawalsGloas(
		state,
		withdrawalIndex,
		withdrawals,
	)
	withdrawals = append(withdrawals, validatorsSweepWithdrawals...)

	return &ExpectedWithdrawals{
		Withdrawals:                      withdrawals,
		ProcessedBuilderWithdrawalsCount: processedBuilderCount,
		ProcessedPartialWithdrawalsCount: processedPartialCount,
		ProcessedBuildersSweepCount:      processedBuildersSweepCount,
		ProcessedValidatorsSweepCount:    processedValidatorsSweepCount,
	}, nil
}

// isParentBlockFull checks if the parent block had a full payload.
// Returns true if the last committed payload bid was fulfilled with a payload.
func (c *WithdrawalCalculator) isParentBlockFull(state *gloas.BeaconState) bool {
	if state.LatestExecutionPayloadBid == nil {
		return false
	}

	return state.LatestExecutionPayloadBid.BlockHash == state.LatestBlockHash
}

// getPendingPartialWithdrawalsElectra processes pending partial withdrawals for Electra.
func (c *WithdrawalCalculator) getPendingPartialWithdrawalsElectra(
	state *electra.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	withdrawalsLimit := min(
		uint64(len(priorWithdrawals))+MaxPendingPartialsPerWithdrawalsSweep,
		MaxWithdrawalsPerPayload-1,
	)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)

	for _, pendingWithdrawal := range state.PendingPartialWithdrawals {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		isWithdrawable := pendingWithdrawal.WithdrawableEpoch <= epoch
		hasReachedLimit := uint64(len(allWithdrawals)) >= withdrawalsLimit

		if !isWithdrawable || hasReachedLimit {
			break
		}

		validatorIndex := pendingWithdrawal.ValidatorIndex
		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		if c.isEligibleForPartialWithdrawals(validator, balance) {
			withdrawalAmount := min(uint64(balance)-MinActivationBalance, uint64(pendingWithdrawal.Amount))

			var address bellatrix.ExecutionAddress

			copy(address[:], validator.WithdrawalCredentials[12:])

			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         phase0.Gwei(withdrawalAmount),
			})
			withdrawalIndex++
		}

		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getPendingPartialWithdrawalsFulu processes pending partial withdrawals for Fulu.
func (c *WithdrawalCalculator) getPendingPartialWithdrawalsFulu(
	state *fulu.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	withdrawalsLimit := min(
		uint64(len(priorWithdrawals))+MaxPendingPartialsPerWithdrawalsSweep,
		MaxWithdrawalsPerPayload-1,
	)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)

	for _, pendingWithdrawal := range state.PendingPartialWithdrawals {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		isWithdrawable := pendingWithdrawal.WithdrawableEpoch <= epoch
		hasReachedLimit := uint64(len(allWithdrawals)) >= withdrawalsLimit

		if !isWithdrawable || hasReachedLimit {
			break
		}

		validatorIndex := pendingWithdrawal.ValidatorIndex
		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		if c.isEligibleForPartialWithdrawals(validator, balance) {
			withdrawalAmount := min(uint64(balance)-MinActivationBalance, uint64(pendingWithdrawal.Amount))

			var address bellatrix.ExecutionAddress

			copy(address[:], validator.WithdrawalCredentials[12:])

			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         phase0.Gwei(withdrawalAmount),
			})
			withdrawalIndex++
		}

		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getPendingPartialWithdrawalsGloas processes pending partial withdrawals for Gloas.
func (c *WithdrawalCalculator) getPendingPartialWithdrawalsGloas(
	state *gloas.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	withdrawalsLimit := min(
		uint64(len(priorWithdrawals))+MaxPendingPartialsPerWithdrawalsSweep,
		MaxWithdrawalsPerPayload-1,
	)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)

	for _, pendingWithdrawal := range state.PendingPartialWithdrawals {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		isWithdrawable := pendingWithdrawal.WithdrawableEpoch <= epoch
		hasReachedLimit := uint64(len(allWithdrawals)) >= withdrawalsLimit

		if !isWithdrawable || hasReachedLimit {
			break
		}

		validatorIndex := pendingWithdrawal.ValidatorIndex
		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		if c.isEligibleForPartialWithdrawals(validator, balance) {
			withdrawalAmount := min(uint64(balance)-MinActivationBalance, uint64(pendingWithdrawal.Amount))

			var address bellatrix.ExecutionAddress

			copy(address[:], validator.WithdrawalCredentials[12:])

			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         phase0.Gwei(withdrawalAmount),
			})
			withdrawalIndex++
		}

		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getValidatorsSweepWithdrawalsElectra sweeps through validators for withdrawals (Electra).
func (c *WithdrawalCalculator) getValidatorsSweepWithdrawalsElectra(
	state *electra.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	validatorsLimit := min(uint64(len(state.Validators)), MaxValidatorsPerWithdrawalsSweep)
	withdrawalsLimit := uint64(MaxWithdrawalsPerPayload)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)
	validatorIndex := state.NextWithdrawalValidatorIndex

	for range validatorsLimit {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		if uint64(len(allWithdrawals)) >= withdrawalsLimit {
			break
		}

		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			validatorIndex = 0
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		var address bellatrix.ExecutionAddress

		copy(address[:], validator.WithdrawalCredentials[12:])

		if c.isFullyWithdrawableValidator(validator, balance, epoch) {
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance,
			})
			withdrawalIndex++
		} else if c.isPartiallyWithdrawableValidator(validator, balance) {
			maxEffectiveBalance := c.getMaxEffectiveBalance(validator)
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance - maxEffectiveBalance,
			})
			withdrawalIndex++
		}

		validatorIndex = phase0.ValidatorIndex((uint64(validatorIndex) + 1) % uint64(len(state.Validators)))
		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getValidatorsSweepWithdrawalsFulu sweeps through validators for withdrawals (Fulu).
func (c *WithdrawalCalculator) getValidatorsSweepWithdrawalsFulu(
	state *fulu.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	validatorsLimit := min(uint64(len(state.Validators)), MaxValidatorsPerWithdrawalsSweep)
	withdrawalsLimit := uint64(MaxWithdrawalsPerPayload)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)
	validatorIndex := state.NextWithdrawalValidatorIndex

	for range validatorsLimit {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		if uint64(len(allWithdrawals)) >= withdrawalsLimit {
			break
		}

		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			validatorIndex = 0
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		var address bellatrix.ExecutionAddress

		copy(address[:], validator.WithdrawalCredentials[12:])

		if c.isFullyWithdrawableValidator(validator, balance, epoch) {
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance,
			})
			withdrawalIndex++
		} else if c.isPartiallyWithdrawableValidator(validator, balance) {
			maxEffectiveBalance := c.getMaxEffectiveBalance(validator)
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance - maxEffectiveBalance,
			})
			withdrawalIndex++
		}

		validatorIndex = phase0.ValidatorIndex((uint64(validatorIndex) + 1) % uint64(len(state.Validators)))
		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getValidatorsSweepWithdrawalsGloas sweeps through validators for withdrawals (Gloas).
func (c *WithdrawalCalculator) getValidatorsSweepWithdrawalsGloas(
	state *gloas.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	validatorsLimit := min(uint64(len(state.Validators)), MaxValidatorsPerWithdrawalsSweep)
	withdrawalsLimit := uint64(MaxWithdrawalsPerPayload)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)
	validatorIndex := state.NextWithdrawalValidatorIndex

	for range validatorsLimit {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		if uint64(len(allWithdrawals)) >= withdrawalsLimit {
			break
		}

		if uint64(validatorIndex) >= uint64(len(state.Validators)) {
			validatorIndex = 0
			processedCount++

			continue
		}

		validator := state.Validators[validatorIndex]
		balance := c.getBalanceAfterWithdrawals(state.Balances, validatorIndex, allWithdrawals)

		var address bellatrix.ExecutionAddress

		copy(address[:], validator.WithdrawalCredentials[12:])

		if c.isFullyWithdrawableValidator(validator, balance, epoch) {
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance,
			})
			withdrawalIndex++
		} else if c.isPartiallyWithdrawableValidator(validator, balance) {
			maxEffectiveBalance := c.getMaxEffectiveBalance(validator)
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: validatorIndex,
				Address:        address,
				Amount:         balance - maxEffectiveBalance,
			})
			withdrawalIndex++
		}

		validatorIndex = phase0.ValidatorIndex((uint64(validatorIndex) + 1) % uint64(len(state.Validators)))
		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getBuilderWithdrawals processes pending builder withdrawals (Gloas-specific).
func (c *WithdrawalCalculator) getBuilderWithdrawals(
	state *gloas.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	withdrawalsLimit := min(
		uint64(len(priorWithdrawals))+MaxPendingPartialsPerWithdrawalsSweep,
		MaxWithdrawalsPerPayload-1,
	)

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)

	for _, pendingWithdrawal := range state.BuilderPendingWithdrawals {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		if uint64(len(allWithdrawals)) >= withdrawalsLimit {
			break
		}

		builderIndex := pendingWithdrawal.BuilderIndex
		if uint64(builderIndex) >= uint64(len(state.Builders)) {
			processedCount++

			continue
		}

		builder := state.Builders[builderIndex]

		// Check if builder has sufficient balance
		if uint64(builder.Balance) < uint64(pendingWithdrawal.Amount) {
			processedCount++

			continue
		}

		withdrawals = append(withdrawals, &capella.Withdrawal{
			Index:          withdrawalIndex,
			ValidatorIndex: phase0.ValidatorIndex(builderIndex), // Use builder index
			Address:        pendingWithdrawal.FeeRecipient,
			Amount:         pendingWithdrawal.Amount,
		})
		withdrawalIndex++
		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// getBuildersSweepWithdrawals sweeps through builders for withdrawals (Gloas-specific).
func (c *WithdrawalCalculator) getBuildersSweepWithdrawals(
	state *gloas.BeaconState,
	withdrawalIndex capella.WithdrawalIndex,
	priorWithdrawals []*capella.Withdrawal,
) ([]*capella.Withdrawal, capella.WithdrawalIndex, uint64) {
	epoch := c.getCurrentEpoch(state.Slot)
	buildersLimit := min(uint64(len(state.Builders)), MaxBuildersPerWithdrawalsSweep)
	withdrawalsLimit := uint64(MaxWithdrawalsPerPayload) - 1 // Reserve space for validator withdrawals

	var processedCount uint64

	withdrawals := make([]*capella.Withdrawal, 0)
	builderIndex := state.NextWithdrawalBuilderIndex

	for range buildersLimit {
		allWithdrawals := append(priorWithdrawals, withdrawals...)
		if uint64(len(allWithdrawals)) >= withdrawalsLimit {
			break
		}

		if uint64(builderIndex) >= uint64(len(state.Builders)) {
			builderIndex = 0
			processedCount++

			continue
		}

		builder := state.Builders[builderIndex]

		// Check if builder is fully withdrawable (exited and past withdrawable epoch)
		if builder.WithdrawableEpoch <= epoch && builder.Balance > 0 {
			withdrawals = append(withdrawals, &capella.Withdrawal{
				Index:          withdrawalIndex,
				ValidatorIndex: phase0.ValidatorIndex(builderIndex),
				Address:        builder.ExecutionAddress,
				Amount:         builder.Balance,
			})
			withdrawalIndex++
		}

		builderIndex = gloas.BuilderIndex((uint64(builderIndex) + 1) % uint64(len(state.Builders)))
		processedCount++
	}

	return withdrawals, withdrawalIndex, processedCount
}

// Helper functions

func (c *WithdrawalCalculator) getCurrentEpoch(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / c.slotsPerEpoch)
}

func (c *WithdrawalCalculator) getBalanceAfterWithdrawals(
	balances []phase0.Gwei,
	validatorIndex phase0.ValidatorIndex,
	withdrawals []*capella.Withdrawal,
) phase0.Gwei {
	if uint64(validatorIndex) >= uint64(len(balances)) {
		return 0
	}

	balance := balances[validatorIndex]

	for _, w := range withdrawals {
		if w.ValidatorIndex == validatorIndex {
			if w.Amount >= balance {
				return 0
			}

			balance -= w.Amount
		}
	}

	return balance
}

func (c *WithdrawalCalculator) isEligibleForPartialWithdrawals(
	validator *phase0.Validator,
	balance phase0.Gwei,
) bool {
	hasSufficientEffectiveBalance := validator.EffectiveBalance >= MinActivationBalance
	hasExcessBalance := uint64(balance) > MinActivationBalance

	return validator.ExitEpoch == FarFutureEpoch &&
		hasSufficientEffectiveBalance &&
		hasExcessBalance
}

func (c *WithdrawalCalculator) isFullyWithdrawableValidator(
	validator *phase0.Validator,
	balance phase0.Gwei,
	epoch phase0.Epoch,
) bool {
	return c.hasExecutionWithdrawalCredential(validator) &&
		validator.WithdrawableEpoch <= epoch &&
		balance > 0
}

func (c *WithdrawalCalculator) isPartiallyWithdrawableValidator(
	validator *phase0.Validator,
	balance phase0.Gwei,
) bool {
	maxEffectiveBalance := c.getMaxEffectiveBalance(validator)
	hasMaxEffectiveBalance := validator.EffectiveBalance == maxEffectiveBalance
	hasExcessBalance := balance > maxEffectiveBalance

	return c.hasExecutionWithdrawalCredential(validator) &&
		hasMaxEffectiveBalance &&
		hasExcessBalance
}

func (c *WithdrawalCalculator) getMaxEffectiveBalance(validator *phase0.Validator) phase0.Gwei {
	if c.hasCompoundingWithdrawalCredential(validator) {
		return MaxEffectiveBalanceElectra
	}

	return MinActivationBalance
}

func (c *WithdrawalCalculator) hasExecutionWithdrawalCredential(validator *phase0.Validator) bool {
	return c.hasETH1WithdrawalCredential(validator) ||
		c.hasCompoundingWithdrawalCredential(validator)
}

func (c *WithdrawalCalculator) hasETH1WithdrawalCredential(validator *phase0.Validator) bool {
	if len(validator.WithdrawalCredentials) == 0 {
		return false
	}

	return validator.WithdrawalCredentials[0] == ETH1AddressWithdrawalPrefix
}

func (c *WithdrawalCalculator) hasCompoundingWithdrawalCredential(validator *phase0.Validator) bool {
	if len(validator.WithdrawalCredentials) == 0 {
		return false
	}

	return validator.WithdrawalCredentials[0] == CompoundingWithdrawalPrefix
}
