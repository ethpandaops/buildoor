package txpool

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// EIP-1559 parameters (mainnet defaults; devnets keep them).
const (
	elasticityMultiplier     = 2
	baseFeeChangeDenominator = 8
	// baseFeeReferenceSlotSeconds is the block spacing at which the
	// slot-spaced rule (EIP-8198) moves the base fee by the classic 1/8.
	baseFeeReferenceSlotSeconds = 12
)

// baseFeeSpacing is the spacing of a block and its parent that the
// slot-spaced base fee rule (EIP-8198, from Bogota/Heze) reads. The zero
// value selects the classic EIP-1559 rule.
type baseFeeSpacing struct {
	seconds uint64
	slots   uint64
}

// NextBaseFee returns the base fee of the block of slot on parent (slot 0 =
// the slot after the parent's).
func (p *Pool) NextBaseFee(parent *types.Header, slot phase0.Slot) *big.Int {
	return p.nextBaseFee(parent, slot)
}

func (p *Pool) nextBaseFee(parent *types.Header, slot phase0.Slot) *big.Int {
	return calcNextBaseFee(parent, p.baseFeeSpacing(parent, slot))
}

// baseFeeSpacing resolves the block spacing of a block of slot on parent from
// the chain's slot timeline; the classic rule applies before Heze, whose EL
// fork (Bogota) introduced the slot-spaced rule.
func (p *Pool) baseFeeSpacing(parent *types.Header, slot phase0.Slot) baseFeeSpacing {
	if p.chainSvc == nil || parent == nil {
		return baseFeeSpacing{}
	}

	if slot > 0 && !p.slotSpacedBaseFee(slot) {
		return baseFeeSpacing{}
	}

	parentSlot := p.chainSvc.TimeToSlot(time.Unix(int64(parent.Time), 0)) //nolint:gosec // block timestamps fit int64
	if slot == 0 {
		slot = parentSlot + 1
		if !p.slotSpacedBaseFee(slot) {
			return baseFeeSpacing{}
		}
	}

	if slot <= parentSlot {
		return baseFeeSpacing{}
	}

	childTime := uint64(p.chainSvc.SlotToTime(slot).Unix()) //nolint:gosec // slot times are after 1970
	if childTime <= parent.Time {
		return baseFeeSpacing{}
	}

	return baseFeeSpacing{seconds: childTime - parent.Time, slots: uint64(slot - parentSlot)}
}

// slotSpacedBaseFee reports whether the block of slot follows the
// slot-spaced base fee rule.
func (p *Pool) slotSpacedBaseFee(slot phase0.Slot) bool {
	return p.chainSvc.ActiveForkAtEpoch(p.chainSvc.GetEpochOfSlot(slot)) >= version.DataVersionHeze
}

// calcNextBaseFee computes the base fee of the block after parent per
// EIP-1559: unchanged at the gas target, otherwise moved in proportion to the
// deviation from the target, by up to 1/8 — or, under the slot-spaced rule,
// by up to seconds / (8 · 12 · slots), capped at 1/8 per slot (3/32 for 9s
// slots). Both rules agree at a 12s spacing.
func calcNextBaseFee(parent *types.Header, spacing baseFeeSpacing) *big.Int {
	baseFee := parent.BaseFee
	if baseFee == nil {
		return new(big.Int)
	}

	gasTarget := parent.GasLimit / elasticityMultiplier
	if gasTarget == 0 {
		return new(big.Int).Set(baseFee)
	}

	numerator, denominator := big.NewInt(1), big.NewInt(baseFeeChangeDenominator)
	if spacing.slots > 0 {
		seconds := min(spacing.seconds, baseFeeReferenceSlotSeconds*spacing.slots)
		numerator.SetUint64(seconds)
		denominator.SetUint64(baseFeeChangeDenominator * baseFeeReferenceSlotSeconds * spacing.slots)
	}

	switch {
	case parent.GasUsed == gasTarget:
		return new(big.Int).Set(baseFee)

	case parent.GasUsed > gasTarget:
		delta := new(big.Int).SetUint64(parent.GasUsed - gasTarget)
		delta.Mul(delta, baseFee)
		delta.Div(delta, new(big.Int).SetUint64(gasTarget))
		delta.Mul(delta, numerator)
		delta.Div(delta, denominator)

		if delta.Sign() == 0 {
			delta.SetInt64(1)
		}

		return delta.Add(baseFee, delta)

	default:
		delta := new(big.Int).SetUint64(gasTarget - parent.GasUsed)
		delta.Mul(delta, baseFee)
		delta.Div(delta, new(big.Int).SetUint64(gasTarget))
		delta.Mul(delta, numerator)
		delta.Div(delta, denominator)

		next := new(big.Int).Sub(baseFee, delta)
		if next.Sign() < 0 {
			next.SetInt64(0)
		}

		return next
	}
}
