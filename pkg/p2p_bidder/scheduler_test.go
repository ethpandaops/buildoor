package p2p_bidder

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

const (
	testBuilderPrivkey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	testBuilderIndex   = uint64(7)
	testSlot           = phase0.Slot(2000)
)

// stubChainService is the minimal chain.Service surface the scheduler, the
// bid creator, and the action plan service use in these tests.
type stubChainService struct {
	chain.Service

	spec        *chain.ChainSpec
	genesis     *beacon.Genesis
	currentSlot phase0.Slot
	fork        version.DataVersion
}

func newStubChainService() *stubChainService {
	return &stubChainService{
		spec: &chain.ChainSpec{
			SecondsPerSlot: 12 * time.Second,
			SlotsPerEpoch:  32,
			ForkSchedule: []chain.ForkSchedule{
				{Fork: version.DataVersionGloas, Version: phase0.Version{0x0a, 0x00, 0x00, 0x00}, Epoch: 0},
			},
		},
		genesis:     &beacon.Genesis{},
		currentSlot: 1000,
		fork:        version.DataVersionGloas,
	}
}

func (s *stubChainService) GetChainSpec() *chain.ChainSpec { return s.spec }
func (s *stubChainService) GetGenesis() *beacon.Genesis    { return s.genesis }
func (s *stubChainService) GetCurrentSlot() phase0.Slot    { return s.currentSlot }

func (s *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}

func (s *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion { return s.fork }

// mockBidSubmitter records submitted bids and can be told to fail.
type mockBidSubmitter struct {
	submitted []*eth2all.SignedExecutionPayloadBid
	err       error
}

func (m *mockBidSubmitter) SubmitExecutionPayloadBid(
	_ context.Context, bid *eth2all.SignedExecutionPayloadBid,
) error {
	if m.err != nil {
		return m.err
	}

	m.submitted = append(m.submitted, bid)

	return nil
}

// newSchedulerTestPayload builds a minimal Gloas payload sufficient for bid
// construction and signing.
func newSchedulerTestPayload(slot phase0.Slot, blockValueWei *big.Int) *payload_builder.Payload {
	blockHash := phase0.Hash32{0xbb}

	return &payload_builder.Payload{
		Attributes: &beacon.PayloadAttributesEvent{ProposalSlot: slot},
		ExecutionPayload: &eth2all.ExecutionPayload{
			Version:   version.DataVersionGloas,
			BlockHash: blockHash,
			GasLimit:  30_000_000,
		},
		BlockHash:  blockHash,
		BlockValue: blockValueWei,
		ReadyAt:    time.Now(),
	}
}

// gweiToWei converts a gwei amount to wei for test payload block values.
func gweiToWei(gwei uint64) *big.Int {
	return new(big.Int).Mul(new(big.Int).SetUint64(gwei), big.NewInt(1_000_000_000))
}

type harnessOptions struct {
	epbsEnabled    bool // cfg.EPBSEnabled, the freeze-time global flag
	serviceEnabled bool // the service status flag (must never affect bidding)
}

type schedulerHarness struct {
	chainSvc  *stubChainService
	cfg       *config.Config
	planSvc   *action_plan.PlanService
	submitter *mockBidSubmitter
	service   *Service
	scheduler *Scheduler
	cache     *payload_builder.PayloadCache
	prefs     *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	events    *utils.Subscription[*BidSubmissionEvent]
}

func newSchedulerHarness(t *testing.T, opts harnessOptions) *schedulerHarness {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	chainSvc := newStubChainService()

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = opts.epbsEnabled
	cfg.EPBS.BidStartTime = 0
	cfg.EPBS.BidEndTime = 4000
	cfg.EPBS.BidInterval = 0
	cfg.EPBS.BidMinAmount = 0
	cfg.EPBS.BidIncrease = 0
	cfg.EPBS.BidSubsidy = 0
	cfg.EPBS.BidValueOverride = 0

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	blsSigner, err := signer.NewBLSSigner(testBuilderPrivkey)
	require.NoError(t, err)

	prefs := memstore.New[phase0.Slot, *gloasspec.SignedProposerPreferences]()

	svc, err := NewService(nil, chainSvc, blsSigner, prefs, planSvc, log)
	require.NoError(t, err)
	svc.SetEnabled(opts.serviceEnabled)

	submitter := &mockBidSubmitter{}
	bidCreator := NewBidCreator(payload_bidder.NewSigner(blsSigner), submitter, chainSvc, testBuilderIndex, log)
	bidTracker := NewBidTracker(testBuilderIndex, log)
	cache := payload_builder.NewPayloadCache(8)

	scheduler := NewScheduler(chainSvc, bidCreator, bidTracker,
		cache, svc, blsSigner, prefs, planSvc, log)

	events := svc.SubscribeBidSubmissions(16, false)
	t.Cleanup(events.Unsubscribe)

	return &schedulerHarness{
		chainSvc:  chainSvc,
		cfg:       cfg,
		planSvc:   planSvc,
		submitter: submitter,
		service:   svc,
		scheduler: scheduler,
		cache:     cache,
		prefs:     prefs,
		events:    events,
	}
}

// preparePayload stores a payload for the slot and (unless noPrefs) a cached
// proposer preference so the prefs gate passes.
func (h *schedulerHarness) preparePayload(slot phase0.Slot, blockValueGwei uint64, noPrefs bool) {
	h.cache.Store(newSchedulerTestPayload(slot, gweiToWei(blockValueGwei)))

	if !noPrefs {
		h.prefs.Put(slot, &gloasspec.SignedProposerPreferences{})
	}
}

func (h *schedulerHarness) applyBidPlan(t *testing.T, slot phase0.Slot, bidJSON string) {
	t.Helper()

	_, err := h.planSvc.ApplyUpdates([]*action_plan.PlanUpdate{{
		Slots: []uint64{uint64(slot)},
		Bid:   json.RawMessage(bidJSON),
	}}, "test")
	require.NoError(t, err)
}

// nextEvent returns the next buffered bid submission event or nil.
func (h *schedulerHarness) nextEvent() *BidSubmissionEvent {
	select {
	case event := <-h.events.Channel():
		return event
	default:
		return nil
	}
}

func TestSchedulerSuppressedSlotSkips(t *testing.T) {
	tests := []struct {
		name        string
		epbsEnabled bool
		bidPlan     string // empty = no plan
	}{
		{
			name:        "plan disables globally enabled bidding",
			epbsEnabled: true,
			bidPlan:     `{"mode":"disabled"}`,
		},
		{
			name:        "global disable without plan override",
			epbsEnabled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// serviceEnabled=true proves the scheduler never consults the
			// service status flag — the frozen plan alone decides.
			h := newSchedulerHarness(t, harnessOptions{
				epbsEnabled:    tt.epbsEnabled,
				serviceEnabled: true,
			})

			if tt.bidPlan != "" {
				h.applyBidPlan(t, testSlot, tt.bidPlan)
			}

			h.preparePayload(testSlot, 100, false)
			h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

			assert.Empty(t, h.submitter.submitted, "no bid must be submitted for a suppressed slot")
			assert.Nil(t, h.nextEvent(), "no event must fire for a suppressed slot")
			assert.True(t, h.planSvc.IsFrozen(testSlot), "first evaluation must freeze the slot")
		})
	}
}

func TestSchedulerForcedSlotBidsWhileDisabled(t *testing.T) {
	// ePBS globally disabled AND the service enable flag off: a custom plan
	// must still activate bidding for the slot.
	h := newSchedulerHarness(t, harnessOptions{
		epbsEnabled:    false,
		serviceEnabled: false,
	})

	h.applyBidPlan(t, testSlot, `{"mode":"custom"}`)
	h.preparePayload(testSlot, 100, false)
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	require.Len(t, h.submitter.submitted, 1, "forced slot must bid despite global disable")

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.True(t, event.Success)
	assert.Equal(t, BidStatusSubmitted, event.Status)
	require.NotNil(t, event.SignedBid)
	assert.Equal(t, testSlot, event.SignedBid.Message.Slot)

	// The cached frozen snapshot must mark the forced activation.
	h.scheduler.mu.Lock()
	frozen := h.scheduler.slotStates[testSlot].Frozen
	h.scheduler.mu.Unlock()

	require.NotNil(t, frozen)
	require.NotNil(t, frozen.Bid)
	assert.True(t, frozen.Bid.Forced)
}

func TestSchedulerAbsoluteBidValue(t *testing.T) {
	tests := []struct {
		name           string
		bidPlan        string
		blockValueGwei uint64
		minAmountGwei  uint64
		subsidyGwei    uint64
		wantValue      uint64
	}{
		{
			name:           "absolute value below block value and min amount",
			bidPlan:        `{"mode":"custom","bid_value_gwei":5}`,
			blockValueGwei: 100,
			minAmountGwei:  50,
			subsidyGwei:    9,
			wantValue:      5, // subsidy and floor do not apply to the absolute base
		},
		{
			name:           "formula applies without absolute value",
			bidPlan:        `{"mode":"custom"}`,
			blockValueGwei: 100,
			minAmountGwei:  50,
			subsidyGwei:    9,
			wantValue:      109, // max(100, 50) + 9
		},
		{
			name:           "min amount floors low block value",
			bidPlan:        `{"mode":"custom"}`,
			blockValueGwei: 10,
			minAmountGwei:  50,
			subsidyGwei:    0,
			wantValue:      50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSchedulerHarness(t, harnessOptions{
				epbsEnabled: true,
			})
			h.cfg.EPBS.BidMinAmount = tt.minAmountGwei
			h.cfg.EPBS.BidSubsidy = tt.subsidyGwei

			h.applyBidPlan(t, testSlot, tt.bidPlan)
			h.preparePayload(testSlot, tt.blockValueGwei, false)
			h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

			require.Len(t, h.submitter.submitted, 1)
			assert.Equal(t, phase0.Gwei(tt.wantValue), h.submitter.submitted[0].Message.Value)

			event := h.nextEvent()
			require.NotNil(t, event)
			assert.Equal(t, tt.wantValue, event.Value)
		})
	}
}

func TestSchedulerSignedNegativeWindow(t *testing.T) {
	const bidPlan = `{"mode":"custom","bid_start_time":-2000,"bid_end_time":1000}`

	tests := []struct {
		name             string
		msRelativeToSlot int64
		wantBid          bool
	}{
		{name: "inside negative pre-slot window", msRelativeToSlot: -1500, wantBid: true},
		{name: "before window start", msRelativeToSlot: -2500, wantBid: false},
		{name: "at window end (exclusive)", msRelativeToSlot: 1000, wantBid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSchedulerHarness(t, harnessOptions{
				epbsEnabled: true,
			})

			h.applyBidPlan(t, testSlot, bidPlan)
			h.preparePayload(testSlot, 100, false)
			h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), tt.msRelativeToSlot)

			if tt.wantBid {
				assert.Len(t, h.submitter.submitted, 1)
			} else {
				assert.Empty(t, h.submitter.submitted)
			}
		})
	}
}

func TestSchedulerPrefsGate(t *testing.T) {
	t.Run("missing prefs skip bids and warn once", func(t *testing.T) {
		h := newSchedulerHarness(t, harnessOptions{
			epbsEnabled: true,
		})

		h.preparePayload(testSlot, 100, true)
		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

		assert.Empty(t, h.submitter.submitted)

		event := h.nextEvent()
		require.NotNil(t, event, "the prefs skip must be reported")
		assert.False(t, event.Success)
		assert.Contains(t, event.Warning, "no proposer preferences")
		assert.Nil(t, event.SignedBid)
		assert.Empty(t, event.Status, "pre-construction skips carry no submission status")

		// The skip is reported once per slot, not on every tick.
		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1010)
		assert.Nil(t, h.nextEvent())
	})

	t.Run("ignore_missing_prefs bypasses the gate", func(t *testing.T) {
		h := newSchedulerHarness(t, harnessOptions{
			epbsEnabled: true,
		})

		h.applyBidPlan(t, testSlot, `{"mode":"custom","ignore_missing_prefs":true}`)
		h.preparePayload(testSlot, 100, true)
		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

		require.Len(t, h.submitter.submitted, 1, "bypass must bid without preferences")

		event := h.nextEvent()
		require.NotNil(t, event)
		assert.True(t, event.Success)
		assert.Equal(t, BidStatusSubmitted, event.Status)
		assert.Contains(t, event.Warning, "ignore_missing_prefs",
			"the bypass must be recorded on the submission event")
	})
}

func TestSchedulerConstructedEventOnSubmitFailure(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{
		epbsEnabled: true,
	})
	h.submitter.err = errors.New("gossip rejected")

	h.applyBidPlan(t, testSlot, `{"mode":"custom"}`)
	h.preparePayload(testSlot, 100, false)
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.False(t, event.Success)
	assert.Equal(t, BidStatusConstructed, event.Status,
		"a built bid with a failed submission is 'constructed'")
	require.NotNil(t, event.SignedBid, "the constructed bid must be carried on the event")
	assert.Contains(t, event.Error, "gossip rejected")
}

func TestSchedulerGlobalDefaultsWithoutPlan(t *testing.T) {
	// Globally enabled bidding with no per-slot plan: the freeze resolves the
	// global config into the snapshot and the slot is bid on with those
	// values — regardless of the service status flag.
	h := newSchedulerHarness(t, harnessOptions{
		epbsEnabled:    true,
		serviceEnabled: false,
	})
	h.cfg.EPBS.BidMinAmount = 50
	h.cfg.EPBS.BidSubsidy = 7

	h.preparePayload(testSlot, 100, false)
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	require.Len(t, h.submitter.submitted, 1)
	assert.Equal(t, phase0.Gwei(107), h.submitter.submitted[0].Message.Value,
		"default value = max(blockValue, min) + subsidy")

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, BidStatusSubmitted, event.Status)
}

func TestSchedulerIntervalIncreaseAndCompetitorHigh(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{
		epbsEnabled: true,
	})

	h.applyBidPlan(t, testSlot,
		`{"mode":"custom","bid_value_gwei":100,"bid_interval":10,"bid_increase":10}`)
	h.preparePayload(testSlot, 500, false)

	// Track a competitor and one of our own bids; only the competitor may be
	// reported as the high bid.
	h.scheduler.bidTracker.TrackBid(newTestBid(testSlot, 99, 500), false)
	h.scheduler.bidTracker.TrackBid(newTestBid(testSlot, testBuilderIndex, 1000), true)

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, uint64(100), event.Value, "first bid uses the absolute base")
	require.NotNil(t, event.CompetitorHighGwei)
	assert.Equal(t, uint64(500), *event.CompetitorHighGwei, "our own 1000 gwei bid must be excluded")

	// Age the last bid past the interval, then re-bid with the increase.
	h.scheduler.mu.Lock()
	h.scheduler.slotStates[testSlot].LastBidTime = time.Now().Add(-time.Second)
	h.scheduler.mu.Unlock()

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1100)

	event = h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, uint64(110), event.Value, "re-bid adds BidCount * increase")
	assert.Equal(t, 2, event.BidCount)
}

func TestSchedulerOverflowClampsInsteadOfWrapping(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{
		epbsEnabled: true,
	})

	h.applyBidPlan(t, testSlot,
		`{"mode":"custom","bid_value_gwei":18446744073709551615,"bid_interval":10,"bid_increase":10}`)
	h.preparePayload(testSlot, 100, false)

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)
	event := h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, uint64(math.MaxUint64), event.Value)

	// Re-bid: MaxUint64 + 1*10 must clamp, not wrap to 9.
	h.scheduler.mu.Lock()
	h.scheduler.slotStates[testSlot].LastBidTime = time.Now().Add(-time.Second)
	h.scheduler.mu.Unlock()

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1100)
	event = h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, uint64(math.MaxUint64), event.Value, "overflowing re-bid must clamp to MaxUint64")
}

func TestSchedulerValueClampHelpers(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{})

	assert.Equal(t, uint64(5), h.scheduler.addGweiClamped(1, 2, 3))
	assert.Equal(t, uint64(math.MaxUint64), h.scheduler.addGweiClamped(1, math.MaxUint64, 1))
	assert.Equal(t, uint64(6), h.scheduler.mulGweiClamped(1, 2, 3))
	assert.Equal(t, uint64(math.MaxUint64), h.scheduler.mulGweiClamped(1, 1<<32, 1<<33))

	assert.Equal(t, uint64(0), weiToGweiClamped(nil))
	assert.Equal(t, uint64(100), weiToGweiClamped(gweiToWei(100)))

	hugeWei := new(big.Int).Mul(gweiToWei(math.MaxUint64), big.NewInt(2))
	assert.Equal(t, uint64(math.MaxUint64), weiToGweiClamped(hugeWei))
}

func TestBidCreatorReturnsBidOnSubmitFailure(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	chainSvc := newStubChainService()

	blsSigner, err := signer.NewBLSSigner(testBuilderPrivkey)
	require.NoError(t, err)

	tests := []struct {
		name       string
		submitErr  error
		wantErr    bool
		wantSigned bool
	}{
		{name: "successful submission returns the bid", wantSigned: true},
		{
			name:       "failed submission still returns the constructed bid",
			submitErr:  errors.New("connection refused"),
			wantErr:    true,
			wantSigned: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			submitter := &mockBidSubmitter{err: tt.submitErr}
			creator := NewBidCreator(payload_bidder.NewSigner(blsSigner), submitter,
				chainSvc, testBuilderIndex, log)

			payload := newSchedulerTestPayload(testSlot, gweiToWei(100))

			signedBid, err := creator.CreateAndSubmitBid(context.Background(), payload, 42, "")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NotNil(t, signedBid)
			assert.Equal(t, testSlot, signedBid.Message.Slot)
			assert.Equal(t, phase0.Gwei(42), signedBid.Message.Value)
			assert.Equal(t, version.DataVersionGloas, signedBid.Version)
		})
	}
}
