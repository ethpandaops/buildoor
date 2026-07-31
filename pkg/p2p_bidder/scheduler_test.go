package p2p_bidder

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sync"
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
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
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

func (s *stubChainService) GetHeadTracker() *chain.HeadTracker { return nil }

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
	mu        sync.Mutex
	submitted []*eth2all.SignedExecutionPayloadBid
	err       error
	inFlight  int

	// beforeSubmit, when set, runs while a submission is in flight — it models
	// the network call the scheduler's tick can race against.
	beforeSubmit func()
}

func (m *mockBidSubmitter) SubmitExecutionPayloadBid(
	_ context.Context, bid *eth2all.SignedExecutionPayloadBid,
) error {
	m.mu.Lock()
	hook := m.beforeSubmit
	m.inFlight++
	m.mu.Unlock()

	if hook != nil {
		hook()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.inFlight--

	if m.err != nil {
		return m.err
	}

	m.submitted = append(m.submitted, bid)

	return nil
}

// count returns how many bids were submitted successfully.
func (m *mockBidSubmitter) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.submitted)
}

// pending returns how many submissions are currently in flight.
func (m *mockBidSubmitter) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.inFlight
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

// agePayloadBid moves a payload's last bid past any interval so the next
// evaluation re-bids it.
func (h *schedulerHarness) agePayloadBid(blockHash phase0.Hash32) {
	h.scheduler.mu.Lock()
	defer h.scheduler.mu.Unlock()

	state := h.scheduler.getSlotState(testSlot)
	state.LastBidTime = time.Now().Add(-time.Second)

	if bidState, ok := state.PayloadBids[blockHash]; ok {
		bidState.lastBid = time.Now().Add(-time.Second)
	}
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

	// Several keys by default: a key is spent once it has bid a slot, so tests
	// that re-bid or bid several candidates need more than one.
	registry := newTestKeyRegistry(t, testBuilderIndex, testBuilderIndex+1,
		testBuilderIndex+2, testBuilderIndex+3)

	prefs := memstore.New[phase0.Slot, *gloasspec.SignedProposerPreferences]()

	svc, err := NewService(nil, chainSvc, registry, prefs, planSvc, log)
	require.NoError(t, err)
	svc.SetEnabled(opts.serviceEnabled)

	submitter := &mockBidSubmitter{}
	bidCreator := NewBidCreator(submitter, chainSvc, log)
	bidTracker := NewBidTracker(registry, log)
	cache := payload_builder.NewPayloadCache(8)

	scheduler := NewScheduler(chainSvc, bidCreator, bidTracker,
		cache, svc, registry, prefs, planSvc, cfg, log)

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
	h.agePayloadBid(phase0.Hash32{0xbb})

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1100)

	event = h.nextEvent()
	require.NotNil(t, event)
	assert.Equal(t, uint64(110), event.Value, "re-bid adds the payload's bid count * increase")
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
	h.agePayloadBid(phase0.Hash32{0xbb})

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

	registry := newTestKeyRegistry(t, testBuilderIndex)

	key, err := registry.Key(0)
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
			creator := NewBidCreator(submitter, chainSvc, log)

			payload := newSchedulerTestPayload(testSlot, gweiToWei(100))

			signedBid, err := creator.CreateAndSubmitBid(context.Background(), key, payload, 42, "")
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

// newCandidatePayload builds a test payload classified as the given candidate
// with a distinct parent tuple and block hash.
func newCandidatePayload(slot phase0.Slot, key chain.CandidateKey, marker byte) *payload_builder.Payload {
	return &payload_builder.Payload{
		Attributes: &beacon.PayloadAttributesEvent{
			ProposalSlot:    slot,
			ParentBlockRoot: phase0.Root{marker},
			ParentBlockHash: phase0.Hash32{marker},
		},
		Candidate: key,
		ExecutionPayload: &eth2all.ExecutionPayload{
			Version:   version.DataVersionGloas,
			BlockHash: phase0.Hash32{marker, 0xbb},
			GasLimit:  30_000_000,
		},
		BlockHash:  phase0.Hash32{marker, 0xbb},
		BlockValue: gweiToWei(1000),
		ReadyAt:    time.Now(),
	}
}

func TestSchedulerBidCandidateSelection(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})

	full := newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01)
	empty := newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02)
	h.cache.Store(full)
	h.cache.Store(empty)
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	// A forced candidate key bids exactly that payload.
	h.cfg.EPBS.BidCandidate = "parent_empty"
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.EqualValues(t, empty.BlockHash, event.BlockHash, "forced candidate must be bid")
	require.Nil(t, h.nextEvent(), "only one candidate must be bid")
}

func TestSchedulerBidCandidateAll(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})

	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01))
	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02))
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	h.cfg.EPBS.BidCandidate = "all"
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	require.NotNil(t, h.nextEvent(), "first candidate bid expected")
	require.NotNil(t, h.nextEvent(), "second candidate bid expected")
	require.Nil(t, h.nextEvent())

	// The single-bid dedup is per payload: a second tick bids nothing new.
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1100)
	require.Nil(t, h.nextEvent())
}

func TestSchedulerAutoCandidateSticky(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})

	empty := newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02)
	h.cache.Store(empty)
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	// Auto (no head tracker in the stub): primary payload wins and the
	// choice sticks on the slot state.
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	event := h.nextEvent()
	require.NotNil(t, event)
	assert.EqualValues(t, empty.BlockHash, event.BlockHash)

	h.scheduler.mu.Lock()
	state := h.scheduler.getSlotState(testSlot)
	h.scheduler.mu.Unlock()
	assert.True(t, state.BidCandidateSet)
	assert.Equal(t, chain.CandidateParentEmpty, state.BidCandidate)
}

func TestSchedulerBidAllIntervalPerPayload(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})

	// Interval mode: candidates must not throttle each other in one tick.
	h.applyBidPlan(t, testSlot, `{"mode":"custom","bid_interval":500,"bid_candidate":"all"}`)

	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01))
	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02))
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	require.NotNil(t, h.nextEvent(), "first candidate bid expected")
	require.NotNil(t, h.nextEvent(),
		"second candidate must bid in the same tick despite the interval")
	require.Nil(t, h.nextEvent())

	// Within the interval neither payload re-bids.
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1100)
	require.Nil(t, h.nextEvent())
}

// With several managed keys, each built candidate is bid from a DIFFERENT key.
// That is what makes the bids propagate: the gossip rules ignore every bid after
// a builder's first for a slot, so one key can only ever land one of them.
func TestSchedulerAssignsDistinctKeysPerCandidate(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})
	h.scheduler.registry = newTestKeyRegistry(t, 11, 12, 13)

	full := newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01)
	empty := newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02)
	h.cache.Store(full)
	h.cache.Store(empty)
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	h.cfg.EPBS.BidCandidate = "all"
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	first := h.nextEvent()
	require.NotNil(t, first)
	second := h.nextEvent()
	require.NotNil(t, second)
	require.Nil(t, h.nextEvent())

	require.NotEqual(t, first.SignedBid.Message.BuilderIndex, second.SignedBid.Message.BuilderIndex,
		"each candidate must be bid from a distinct builder key")

	// Both keys are now spent for the slot: the third key is what an escalated
	// re-bid has to use, because a spent key's next bid is ignored by gossip.
	h.scheduler.mu.Lock()
	spent := len(h.scheduler.getSlotState(testSlot).UsedKeys)
	h.scheduler.mu.Unlock()

	require.Equal(t, 2, spent)
}

// An escalated re-bid of the SAME payload must come from a key that has not bid
// this slot yet: the gossip rules ignore a builder's later bids, so re-bidding
// from the same key means the higher value never reaches the network.
func TestSchedulerEscalatesOntoUnusedKeys(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})
	h.scheduler.registry = newTestKeyRegistry(t, 11, 12, 13)

	h.applyBidPlan(t, testSlot, `{"mode":"custom","bid_interval":50,"bid_increase":10}`)

	payload := newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01)
	h.cache.Store(payload)
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	seen := make(map[uint64]struct{}, 3)

	for range 3 {
		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

		event := h.nextEvent()
		require.NotNil(t, event)
		require.NotNil(t, event.SignedBid)

		builderIndex := uint64(event.SignedBid.Message.BuilderIndex)
		_, repeated := seen[builderIndex]
		require.False(t, repeated, "builder %d bid the slot twice", builderIndex)

		seen[builderIndex] = struct{}{}

		h.agePayloadBid(payload.BlockHash)
	}

	// The fleet is exhausted: a fourth attempt has no key left that could
	// propagate, so it bids nothing rather than repeating a spent key.
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)
	require.Nil(t, h.nextEvent())
}

// bid_keys_per_slot caps how many distinct keys bid a slot, so an operator can
// keep the single-bid behaviour even with several candidates built.
func TestSchedulerBidKeysPerSlotCap(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})
	h.scheduler.registry = newTestKeyRegistry(t, 11, 12, 13)

	h.applyBidPlan(t, testSlot,
		`{"mode":"custom","bid_candidate":"all","bid_keys_per_slot":1}`)

	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01))
	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentEmpty, 0x02))
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)

	require.NotNil(t, h.nextEvent(), "first candidate bid expected")
	require.Nil(t, h.nextEvent(), "the key cap must stop the second candidate")
}

// The scheduler ticks every 10ms while a bid submission is a network call taking
// tens of milliseconds. A payload's bid slot must therefore be claimed before the
// submission, not after it: otherwise the ticks that land mid-flight pass the
// interval check and gossip the same bid again — which the beacon node rejects as
// already known and which burns the key's one bid for the slot.
func TestSchedulerDoesNotReBidDuringSubmission(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})

	// Interval mode, so only the in-flight claim can stop a second submission.
	h.applyBidPlan(t, testSlot, `{"mode":"custom","bid_interval":500}`)

	payload := newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01)
	h.cache.Store(payload)
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	// Block the submission so a concurrent tick runs while it is in flight.
	release := make(chan struct{})
	h.submitter.beforeSubmit = func() { <-release }

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)
	}()

	// Give the first submission time to reach the blocked submitter, then tick
	// again exactly as the 10ms scheduler would.
	require.Eventually(t, func() bool { return h.submitter.pending() > 0 }, time.Second, 5*time.Millisecond)

	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1010)
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1020)

	close(release)
	wg.Wait()

	assert.Equal(t, 1, h.submitter.count(), "only one bid may be submitted while one is in flight")
}

// A tick that finds nothing due must not consume a key. The scheduler ticks
// every 10ms while the bid interval is far longer, so claiming a key before the
// interval gate spends the whole fleet within a few ticks and leaves the slot
// unable to bid at all.
func TestSchedulerDoesNotSpendKeysOnIdleTicks(t *testing.T) {
	h := newSchedulerHarness(t, harnessOptions{epbsEnabled: true, serviceEnabled: true})
	h.scheduler.registry = newTestKeyRegistry(t, 11, 12, 13)

	h.applyBidPlan(t, testSlot, `{"mode":"custom","bid_interval":5000}`)

	h.cache.Store(newCandidatePayload(testSlot, chain.CandidateParentFull, 0x01))
	h.prefs.Put(testSlot, &gloasspec.SignedProposerPreferences{})

	// One bid, then many ticks inside the interval.
	h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1000)
	require.NotNil(t, h.nextEvent())

	for range 10 {
		h.scheduler.checkSlotForBidding(context.Background(), testSlot, time.Now(), 1010)
	}

	require.Nil(t, h.nextEvent(), "ticks inside the interval must not bid")

	h.scheduler.mu.Lock()
	spent := len(h.scheduler.getSlotState(testSlot).UsedKeys)
	h.scheduler.mu.Unlock()

	assert.Equal(t, 1, spent, "only the tick that actually bid may spend a key")
}
