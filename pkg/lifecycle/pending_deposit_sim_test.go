package lifecycle

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

const gwei = uint64(1_000_000_000)

func TestActivationExitChurnLimit(t *testing.T) {
	tests := []struct {
		name        string
		totalActive uint64
		minChurn    uint64
		quotient    uint64
		maxChurn    uint64
		increment   uint64
		want        uint64
	}{
		{
			name:        "below floor clamps to min",
			totalActive: 1_000 * gwei,
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        64 * gwei, // 1000e9/65536 ~ 0.015 ETH < 64 ETH floor
		},
		{
			name:        "scales with active balance, floored to increment",
			totalActive: 65536 * 100 * gwei, // /65536 = 100 ETH
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        100 * gwei,
		},
		{
			name:        "clamps to max",
			totalActive: 65536 * 1000 * gwei, // /65536 = 1000 ETH
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        256 * gwei,
		},
		{
			name:        "unknown quotient yields zero",
			totalActive: 100 * gwei,
			minChurn:    64 * gwei,
			quotient:    0,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activationExitChurnLimit(tt.totalActive, tt.minChurn, tt.quotient, tt.maxChurn, tt.increment)
			assert.Equal(t, tt.want, got)
		})
	}
}

func repeatAmounts(n int, amount uint64) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = amount
	}

	return out
}

// simEpoch is the queue snapshot epoch of the simulation tests; far enough from
// genesis that slot-0 entries are always finalized.
const simEpoch = phase0.Epoch(100)

// finalizedDeposits turns amounts into queue entries at slot 0, which finality
// never holds back.
func finalizedDeposits(amounts []uint64) []simDeposit {
	out := make([]simDeposit, len(amounts))
	for i, amount := range amounts {
		out[i] = simDeposit{amount: amount}
	}

	return out
}

func TestSimulateDepositSurvives(t *testing.T) {
	const (
		bigChurn  = 1_000_000 * gwei
		deposit32 = 32 * gwei
		maxPer    = uint64(16)
		noCarry   = uint64(0)
	)

	tests := []struct {
		name        string
		queue       []uint64
		ourAmount   uint64
		dbtc        uint64
		churn       uint64
		maxPerEpoch uint64
		transitions uint64
		want        bool
	}{
		{
			name:        "empty queue drains our deposit immediately",
			queue:       nil,
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 4,
			want:        false,
		},
		{
			name:        "long queue shields our deposit (churn-limited)",
			queue:       repeatAmounts(100, deposit32),
			ourAmount:   deposit32,
			churn:       64 * gwei, // ~2 deposits/epoch
			maxPerEpoch: maxPer,
			transitions: 4, // ~8 drained, far short of 100 ahead
			want:        true,
		},
		{
			name:        "short queue fully drains and reaches our deposit",
			queue:       repeatAmounts(4, deposit32),
			ourAmount:   deposit32,
			churn:       bigChurn, // count-limited only
			maxPerEpoch: maxPer,
			transitions: 1, // up to 16 drained > 4 ahead -> reaches ours
			want:        false,
		},
		{
			name:        "count cap shields our deposit across few transitions",
			queue:       repeatAmounts(100, 1), // tiny amounts, churn never binds
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 4, // 4*16 = 64 < 100 ahead
			want:        true,
		},
		{
			name:        "count cap eventually reaches our deposit over many transitions",
			queue:       repeatAmounts(100, 1),
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 10, // 10*16 = 160 > 100 ahead -> reaches ours
			want:        false,
		},
		{
			name:        "zero per-epoch cap is undecidable -> false",
			queue:       repeatAmounts(100, deposit32),
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: 0,
			transitions: 4,
			want:        false,
		},
		{
			name:        "carried deposit balance adds to first-epoch budget",
			queue:       repeatAmounts(3, deposit32),
			ourAmount:   deposit32,
			dbtc:        96 * gwei, // +3 deposits of headroom on top of churn
			churn:       deposit32, // 1 deposit/epoch from churn alone
			maxPerEpoch: maxPer,
			transitions: 1, // budget = 96+32 = 128 -> 4 deposits -> drains 3 ahead + ours
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := simulateDepositSurvives(finalizedDeposits(tt.queue), finalizedDeposits([]uint64{tt.ourAmount}),
				tt.dbtc, tt.churn, tt.maxPerEpoch, simEpoch, simEpoch+phase0.Epoch(tt.transitions), 32)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Onboarding several keys appends several entries: the batch survives to the
// fork only when its FIRST entry does — a partly drained batch onboards the
// keys behind the drain and turns the drained ones into validators.
func TestSimulateDepositSurvives_Batch(t *testing.T) {
	const (
		deposit32 = 32 * gwei
		maxPer    = 16
	)

	tests := []struct {
		name       string
		ourAmounts []uint64
		churn      uint64
		want       bool
	}{
		{
			name:       "empty batch trivially survives",
			ourAmounts: nil,
			churn:      deposit32,
			want:       true,
		},
		{
			name:       "batch smaller than the drain budget is consumed",
			ourAmounts: repeatAmounts(2, deposit32),
			churn:      4 * deposit32,
			want:       false,
		},
		{
			name:       "batch larger than the drain budget is partly consumed",
			ourAmounts: repeatAmounts(6, deposit32),
			churn:      4 * deposit32,
			want:       false,
		},
		{
			name:       "batch whose first entry stays out of the drain survives",
			ourAmounts: repeatAmounts(4, deposit32),
			churn:      0,
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := simulateDepositSurvives(nil, finalizedDeposits(tt.ourAmounts), 0, tt.churn, maxPer, simEpoch, simEpoch+1, 32)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSimulateDepositSurvives_Finality holds back deposits newer than the best
// possible finalized checkpoint (epoch k-2 at the transition into epoch k).
func TestSimulateDepositSurvives_Finality(t *testing.T) {
	const (
		slotsPerEpoch = 32
		fork          = phase0.Epoch(110)
		bigChurn      = 1_000_000 * gwei
		maxPer        = 16
	)

	forkMinus2Start := phase0.Slot(uint64(fork-2) * slotsPerEpoch)

	tests := []struct {
		name    string
		queue   []simDeposit
		landing phase0.Slot
		want    bool
	}{
		{
			name:    "landing in the first slot of fork-2 is finalized at the last transition",
			landing: forkMinus2Start,
			want:    false,
		},
		{
			name:    "landing after the first slot of fork-2 is never finalized before the fork",
			landing: forkMinus2Start + 1,
			want:    true,
		},
		{
			name:    "landing earlier with an empty queue is drained",
			landing: phase0.Slot(uint64(fork-10) * slotsPerEpoch),
			want:    false,
		},
		{
			name: "a recent entry ahead holds the rest of the queue back",
			queue: []simDeposit{
				{amount: 32 * gwei, slot: forkMinus2Start + 1},
			},
			landing: phase0.Slot(uint64(fork-10) * slotsPerEpoch),
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := simulateDepositSurvives(tt.queue, []simDeposit{{amount: 32 * gwei, slot: tt.landing}},
				0, bigChurn, maxPer, simEpoch, fork, slotsPerEpoch)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSimulateDepositSurvives_NoChurn lets deposits for exited validators pass
// without consuming churn, so they do not shield the entries behind them.
func TestSimulateDepositSurvives_NoChurn(t *testing.T) {
	queue := []simDeposit{
		{amount: 2048 * gwei, noChurn: true},
		{amount: 2048 * gwei, noChurn: true},
	}

	got := simulateDepositSurvives(queue, finalizedDeposits([]uint64{32 * gwei}),
		0, 256*gwei, 16, simEpoch, simEpoch+1, 32)
	assert.False(t, got, "no-churn entries must not shield our deposit")

	queue[0].noChurn = false
	queue[1].noChurn = false

	got = simulateDepositSurvives(queue, finalizedDeposits([]uint64{32 * gwei}),
		0, 256*gwei, 16, simEpoch, simEpoch+1, 32)
	assert.True(t, got, "churn-consuming entries above the budget shield our deposit")
}

// TestQueueDrainWindow checks the drain-time prediction against the survival
// check: with the queue's length held by new arrivals, a deposit landing once the
// fork is within the drain time survives, one landing earlier does not.
func TestQueueDrainWindow(t *testing.T) {
	spec := &chain.ChainSpec{
		SlotsPerEpoch:                       32,
		MaxPendingDepositsPerEpoch:          16,
		ChurnLimitQuotient:                  65536,
		MinPerEpochChurnLimit:               128 * gwei,
		MaxPerEpochActivationExitChurnLimit: 256 * gwei,
		EffectiveBalanceIncrement:           gwei,
	}

	queue := make([]chain.PendingDepositInfo, 0, 300)
	for range 300 {
		queue = append(queue, chain.PendingDepositInfo{Amount: gwei})
	}

	stats := &chain.EpochStats{
		Epoch:              simEpoch,
		StateSlot:          phase0.Slot(uint64(simEpoch) * 32),
		TotalActiveBalance: 1_000_000 * 32 * gwei,
		PendingDeposits:    queue,
	}

	drain, ok := queueDrainEpochs(stats, spec)
	require.True(t, ok)
	assert.Equal(t, uint64(19), drain, "300 deposits at 16 per epoch")

	landing := phase0.Slot(uint64(simEpoch)*32 + 1)

	// The snapshot queue outlasts a fork closer than the drain time; the last
	// drain transition has spare capacity for our deposit.
	assert.True(t, depositsSurviveUntilFork(stats, nil, spec, 50*gwei, 1, landing, simEpoch+phase0.Epoch(drain)-1))
	assert.False(t, depositsSurviveUntilFork(stats, nil, spec, 50*gwei, 1, landing, simEpoch+phase0.Epoch(drain)))

	// Arrivals since the snapshot extend the shield.
	arrivals := make([]chain.PendingDepositInfo, 0, 64)
	for range 64 {
		arrivals = append(arrivals, chain.PendingDepositInfo{Amount: gwei, Slot: stats.StateSlot + 1})
	}

	assert.True(t, depositsSurviveUntilFork(stats, arrivals, spec, 50*gwei, 1, landing, simEpoch+phase0.Epoch(drain)+2))
	assert.True(t, nearEarlyOnboardWindow(stats, spec, simEpoch+phase0.Epoch(drain)+earlyArrivalsWindowEpochs))
	assert.False(t, nearEarlyOnboardWindow(stats, spec, simEpoch+phase0.Epoch(drain)+earlyArrivalsWindowEpochs+1))
}
