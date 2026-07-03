package epbs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/OffchainLabs/go-bitfield"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/api"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/altair"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService is a minimal chain.Service for tests with controllable
// slot timing, current fork, and genesis (shape copied from
// pkg/payload_bidder/mockchain_test.go).
type stubChainService struct {
	genesisTime  time.Time
	slotDuration time.Duration
	currentFork  version.DataVersion
	genesis      beacon.Genesis
}

var _ chain.Service = (*stubChainService)(nil)

func (m *stubChainService) Start(context.Context) error { return nil }
func (m *stubChainService) Stop() error                 { return nil }

func (m *stubChainService) GetChainSpec() *chain.ChainSpec {
	return &chain.ChainSpec{SecondsPerSlot: m.slotDuration, SlotsPerEpoch: 32}
}
func (m *stubChainService) GetGenesis() *beacon.Genesis { return &m.genesis }

func (m *stubChainService) SlotToTime(slot phase0.Slot) time.Time {
	return m.genesisTime.Add(time.Duration(slot) * m.slotDuration) //nolint:gosec // test helper
}

func (m *stubChainService) TimeToSlot(t time.Time) phase0.Slot {
	return phase0.Slot(t.Sub(m.genesisTime) / m.slotDuration) //nolint:gosec // test helper
}

func (m *stubChainService) GetCurrentEpoch() phase0.Epoch { return 0 }
func (m *stubChainService) GetCurrentSlot() phase0.Slot   { return 0 }

func (m *stubChainService) GetCurrentFork() version.DataVersion { return m.currentFork }
func (m *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return m.currentFork
}
func (m *stubChainService) GetForkVersion() (phase0.Version, error) { return phase0.Version{}, nil }

func (m *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / 32)
}

func (m *stubChainService) GetCurrentEpochStats() *chain.EpochStats      { return nil }
func (m *stubChainService) GetEpochStats(phase0.Epoch) *chain.EpochStats { return nil }

func (m *stubChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] { return nil }
func (m *stubChainService) GetHeadVoteTracker() *chain.HeadVoteTracker                  { return nil }
func (m *stubChainService) GetFinalizedEpoch() phase0.Epoch                             { return 0 }

func (m *stubChainService) GetBuilderByIndex(uint64) *chain.BuilderInfo            { return nil }
func (m *stubChainService) GetBuilderByPubkey(phase0.BLSPubKey) *chain.BuilderInfo { return nil }
func (m *stubChainService) GetBuilders() []*chain.BuilderInfo                      { return nil }

func (m *stubChainService) GetValidatorPubkeyByIndex(phase0.ValidatorIndex) *phase0.BLSPubKey {
	return nil
}

func (m *stubChainService) RefreshBuilders(context.Context) error { return nil }

// stubBlockBroadcaster records SubmitProposal calls and can be set to fail.
type stubBlockBroadcaster struct {
	mu       sync.Mutex
	calls    int
	lastOpts *api.SubmitProposalOpts
	err      error
}

func (b *stubBlockBroadcaster) SubmitProposal(_ context.Context, opts *api.SubmitProposalOpts) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.calls++
	b.lastOpts = opts

	return b.err
}

func (b *stubBlockBroadcaster) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.calls
}

func (b *stubBlockBroadcaster) lastProposal() *api.VersionedSignedProposal {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.lastOpts == nil {
		return nil
	}

	return b.lastOpts.Proposal
}

// stubEnvelopePublisher records envelope publishes (the reveal side effect).
type stubEnvelopePublisher struct {
	mu    sync.Mutex
	calls int
}

func (p *stubEnvelopePublisher) SubmitExecutionPayloadEnvelope(
	_ context.Context, _ *eth2all.SignedExecutionPayloadEnvelope, _ [][]byte, _ [][]byte,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++

	return nil
}

func (p *stubEnvelopePublisher) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

// beaconBlockTestEnv bundles the wiring shared by the submitBeaconBlock tests.
type beaconBlockTestEnv struct {
	handler     *Handler
	chainSvc    *stubChainService
	broadcaster *stubBlockBroadcaster
	publisher   *stubEnvelopePublisher
	revealSvc   *payload_bidder.RevealService
}

// newBeaconBlockTestEnv creates an enabled post-Gloas handler wired to a real
// RevealService (with a stub envelope publisher). Slot 1 starts "now"; the
// reveal is due revealTimeMs into the slot.
func newBeaconBlockTestEnv(t *testing.T, slotDuration time.Duration, revealTimeMs int64) *beaconBlockTestEnv {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	cfg := &config.Config{}
	cfg.EPBS.RevealTime = revealTimeMs

	chainSvc := &stubChainService{
		genesisTime:  time.Now().Add(-slotDuration), // slot 1 starts now
		slotDuration: slotDuration,
		currentFork:  version.DataVersionGloas,
	}

	builderSvc, err := payload_builder.NewService(&config.Config{}, nil, chainSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	publisher := &stubEnvelopePublisher{}
	revealSvc := payload_bidder.NewRevealService(
		cfg, payload_bidder.NewSigner(blsSigner), publisher, chainSvc, builderSvc, nil, log)

	broadcaster := &stubBlockBroadcaster{}

	h := NewHandler(&cfg.BuilderAPI, log, chainSvc, payload_builder.NewPayloadCache(10), blsSigner)
	h.SetBlockBroadcaster(broadcaster)
	h.SetRevealService(revealSvc)
	h.SetEnabled(true)

	return &beaconBlockTestEnv{
		handler:     h,
		chainSvc:    chainSvc,
		broadcaster: broadcaster,
		publisher:   publisher,
		revealSvc:   revealSvc,
	}
}

// seedGloasPayload stores a minimal Gloas payload (sufficient for envelope
// signing) in the handler's payload cache and returns it.
func seedGloasPayload(h *Handler, slot phase0.Slot, blockHash phase0.Hash32) *payload_builder.Payload {
	payload := &payload_builder.Payload{
		Attributes: &beacon.PayloadAttributesEvent{ProposalSlot: slot},
		ExecutionPayload: &eth2all.ExecutionPayload{
			Version:       version.DataVersionGloas,
			BaseFeePerGas: uint256.NewInt(7),
			BlockHash:     blockHash,
		},
		BlockHash:  blockHash,
		BlockValue: big.NewInt(2_000_000_000_000),
		ReadyAt:    time.Now(),
	}
	h.payloadCache.Store(payload)

	return payload
}

// signedBeaconBlockJSON builds a fully populated Gloas SignedBeaconBlock whose
// bid commits to blockHash, and returns its JSON encoding.
func signedBeaconBlockJSON(t *testing.T, slot phase0.Slot, blockHash phase0.Hash32) []byte {
	t.Helper()

	block := &gloasspec.SignedBeaconBlock{
		Message: &gloasspec.BeaconBlock{
			Slot:       slot,
			ParentRoot: phase0.Root{0x22},
			StateRoot:  phase0.Root{0x33},
			Body: &gloasspec.BeaconBlockBody{
				ETH1Data: &phase0.ETH1Data{
					BlockHash: make([]byte, 32),
				},
				ProposerSlashings: []*phase0.ProposerSlashing{},
				AttesterSlashings: []*electra.AttesterSlashing{},
				Attestations:      []*electra.Attestation{},
				Deposits:          []*phase0.Deposit{},
				VoluntaryExits:    []*phase0.SignedVoluntaryExit{},
				SyncAggregate: &altair.SyncAggregate{
					SyncCommitteeBits: bitfield.NewBitvector512(),
				},
				BLSToExecutionChanges: []*capella.SignedBLSToExecutionChange{},
				SignedExecutionPayloadBid: &gloasspec.SignedExecutionPayloadBid{
					Message: &gloasspec.ExecutionPayloadBid{
						BlockHash:          blockHash,
						Slot:               slot,
						BlobKZGCommitments: []deneb.KZGCommitment{},
					},
				},
				PayloadAttestations:     []*gloasspec.PayloadAttestation{},
				ParentExecutionRequests: &gloasspec.ExecutionRequests{},
			},
		},
	}

	body, err := json.Marshal(block)
	require.NoError(t, err)

	return body
}

func postBeaconBlock(h *Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/beacon_block", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBeaconBlock(rec, req)

	return rec
}

// TestHandleSubmitBeaconBlock_Success broadcasts the block immediately,
// returns 202 without publishing anything during the request, and reveals the
// envelope exactly once via the RevealService after the reveal time.
func TestHandleSubmitBeaconBlock_Success(t *testing.T) {
	env := newBeaconBlockTestEnv(t, 4*time.Second, 500)

	require.NoError(t, env.revealSvc.Start(context.Background()))
	defer env.revealSvc.Stop()

	slot := phase0.Slot(1)
	blockHash := phase0.Hash32{0xab}
	payload := seedGloasPayload(env.handler, slot, blockHash)

	rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, slot, blockHash))

	require.Equal(t, http.StatusAccepted, rec.Code, "submitBeaconBlock should return 202")
	assert.Equal(t, 1, env.broadcaster.callCount(), "beacon block must be broadcast exactly once")
	assert.Equal(t, 0, env.publisher.callCount(), "nothing may be published during the request")
	assert.Equal(t, uint64(1), env.handler.BlocksAccepted())

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 3*time.Second, 10*time.Millisecond, "expected exactly one envelope publish after the reveal time")

	// Give a duplicate publish a chance to (wrongly) happen.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, env.publisher.callCount(), "envelope must be published exactly once")

	reveal := payload.Reveal()
	require.NotNil(t, reveal, "payload must be marked revealed by the reveal service")
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, reveal.Transport)
}

// TestHandleSubmitBeaconBlock_ProposalVersion verifies the broadcast proposal
// carries the fork-agnostic block in the fork's proposal field: the chain's
// current fork by default, or the Eth-Consensus-Version header's fork when
// supplied (Heze reuses the Gloas block schema).
func TestHandleSubmitBeaconBlock_ProposalVersion(t *testing.T) {
	env := newBeaconBlockTestEnv(t, 4*time.Second, 3500)

	require.NoError(t, env.revealSvc.Start(context.Background()))
	defer env.revealSvc.Stop()

	slot := phase0.Slot(1)
	blockHash := phase0.Hash32{0xab}
	seedGloasPayload(env.handler, slot, blockHash)

	// Default: the chain's current fork (Gloas).
	rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, slot, blockHash))
	require.Equal(t, http.StatusAccepted, rec.Code)

	proposal := env.broadcaster.lastProposal()
	require.NotNil(t, proposal)
	assert.Equal(t, version.DataVersionGloas, proposal.Version)
	require.NotNil(t, proposal.Gloas, "proposal must carry the Gloas block")
	assert.Nil(t, proposal.Heze)

	// Heze via the Eth-Consensus-Version header (same wire schema as Gloas).
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/beacon_block",
		bytes.NewReader(signedBeaconBlockJSON(t, slot, blockHash)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "heze")
	rec = httptest.NewRecorder()

	env.handler.HandleSubmitBeaconBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)

	proposal = env.broadcaster.lastProposal()
	require.NotNil(t, proposal)
	assert.Equal(t, version.DataVersionHeze, proposal.Version)
	require.NotNil(t, proposal.Heze, "proposal must carry the Heze block")
	assert.Nil(t, proposal.Gloas)
}

// TestHandleSubmitBeaconBlock_NoCachedPayload returns 400 and neither
// broadcasts the block nor requests a reveal.
func TestHandleSubmitBeaconBlock_NoCachedPayload(t *testing.T) {
	env := newBeaconBlockTestEnv(t, 500*time.Millisecond, 10)

	require.NoError(t, env.revealSvc.Start(context.Background()))
	defer env.revealSvc.Stop()

	// No payload seeded.
	rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, 1, phase0.Hash32{0xab}))

	assert.Equal(t, http.StatusBadRequest, rec.Code, "missing cached payload should return 400")
	assert.Equal(t, 0, env.broadcaster.callCount(), "block must not be broadcast")
	assert.Equal(t, uint64(0), env.handler.BlocksAccepted())

	// Wait past the reveal time — no reveal may have been requested.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, 0, env.publisher.callCount(), "no reveal may be requested")
}

// TestHandleSubmitBeaconBlock_BroadcastFailure returns 500 and does not
// request a reveal when the block broadcast fails.
func TestHandleSubmitBeaconBlock_BroadcastFailure(t *testing.T) {
	env := newBeaconBlockTestEnv(t, 500*time.Millisecond, 10)
	env.broadcaster.err = errors.New("broadcast failed")

	require.NoError(t, env.revealSvc.Start(context.Background()))
	defer env.revealSvc.Stop()

	slot := phase0.Slot(1)
	blockHash := phase0.Hash32{0xab}
	payload := seedGloasPayload(env.handler, slot, blockHash)

	rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, slot, blockHash))

	assert.Equal(t, http.StatusInternalServerError, rec.Code, "broadcast failure should return 500")
	assert.Equal(t, 1, env.broadcaster.callCount())
	assert.Equal(t, uint64(0), env.handler.BlocksAccepted())

	// Wait past the reveal time — no reveal may have been requested.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, 0, env.publisher.callCount(), "no reveal may be requested after a failed broadcast")
	assert.Nil(t, payload.Reveal())
}
