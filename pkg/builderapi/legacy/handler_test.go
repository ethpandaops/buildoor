package legacy

import (
	"bytes"
	"context"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService is a minimal chain.Service for tests. It returns the
// configured genesis, fork version, and current fork; all other accessors
// return zero values.
type stubChainService struct {
	genesis       beacon.Genesis
	forkVersion   phase0.Version
	currentFork   version.DataVersion
	pubkeyByIndex map[phase0.ValidatorIndex]phase0.BLSPubKey
}

var _ chain.Service = (*stubChainService)(nil)

func (m *stubChainService) Start(context.Context) error { return nil }
func (m *stubChainService) Stop() error                 { return nil }

func (m *stubChainService) GetChainSpec() *chain.ChainSpec { return nil }
func (m *stubChainService) GetGenesis() *beacon.Genesis    { return &m.genesis }

func (m *stubChainService) SlotToTime(phase0.Slot) time.Time { return time.Time{} }
func (m *stubChainService) TimeToSlot(time.Time) phase0.Slot { return 0 }
func (m *stubChainService) GetCurrentEpoch() phase0.Epoch    { return 0 }
func (m *stubChainService) GetCurrentSlot() phase0.Slot      { return 0 }

func (m *stubChainService) GetCurrentFork() version.DataVersion { return m.currentFork }
func (m *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return m.currentFork
}
func (m *stubChainService) GetForkVersion() (phase0.Version, error)      { return m.forkVersion, nil }
func (m *stubChainService) GetEpochOfSlot(phase0.Slot) phase0.Epoch      { return 0 }
func (m *stubChainService) GetCurrentEpochStats() *chain.EpochStats      { return nil }
func (m *stubChainService) GetEpochStats(phase0.Epoch) *chain.EpochStats { return nil }

func (m *stubChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] { return nil }
func (m *stubChainService) GetHeadVoteTracker() *chain.HeadVoteTracker                  { return nil }
func (m *stubChainService) GetFinalizedEpoch() phase0.Epoch                             { return 0 }

func (m *stubChainService) GetBuilderByIndex(uint64) *chain.BuilderInfo            { return nil }
func (m *stubChainService) GetBuilderByPubkey(phase0.BLSPubKey) *chain.BuilderInfo { return nil }
func (m *stubChainService) GetBuilders() []*chain.BuilderInfo                      { return nil }

func (m *stubChainService) GetValidatorPubkeyByIndex(index phase0.ValidatorIndex) *phase0.BLSPubKey {
	if pubkey, ok := m.pubkeyByIndex[index]; ok {
		return &pubkey
	}

	return nil
}

func (m *stubChainService) RefreshBuilders(context.Context) error { return nil }

// stubBlockPublisher records the last SubmitLegacyBlock call for tests.
type stubBlockPublisher struct {
	lastContents *apiv1all.SignedBlockContents
	err          error
}

func (p *stubBlockPublisher) SubmitLegacyBlock(_ context.Context, contents *apiv1all.SignedBlockContents) error {
	p.lastContents = contents
	return p.err
}

func newTestHandler(chainSvc chain.Service, blsSigner *signer.BLSSigner) *Handler {
	return NewHandler(&config.BuilderAPIConfig{}, logrus.New(), chainSvc,
		payload_builder.NewPayloadCache(10),
		memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration](), blsSigner)
}

// TestHandleGetHeader_PostGloasForkGuard returns 204 once the chain has
// activated Gloas, even when the handler is enabled and fully configured.
func TestHandleGetHeader_PostGloasForkGuard(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	h := newTestHandler(&stubChainService{currentFork: version.DataVersionGloas}, blsSigner)
	h.SetEnabled(true)

	pk := blsSigner.PublicKey()
	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	req := httptest.NewRequest(http.MethodGet, "/eth/v1/builder/header/1/"+zeroHash+"/0x"+hex.EncodeToString(pk[:]), nil)
	req = mux.SetURLVars(req, map[string]string{
		"slot":        "1",
		"parent_hash": zeroHash,
		"pubkey":      "0x" + hex.EncodeToString(pk[:]),
	})
	rec := httptest.NewRecorder()

	h.HandleGetHeader(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code, "getHeader post-Gloas should return 204")
	assert.Equal(t, uint64(0), h.HeadersRequested(), "fork guard should reject before counting the request")
}

// blockHashFromBuilderSpecsFulu matches the execution_payload.block_hash in builder-specs examples/fulu/.
var blockHashFromBuilderSpecsFulu = common.HexToHash("0xcf8e0d4e9587369b2301d0790347320302cc0943d5a1884560367e8208d920f2")

// logsBloom256Hex is 256 bytes (512 hex chars) for execution_payload_header.logs_bloom.
const logsBloom256Hex = "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// randaoReveal96Hex is 96 bytes (192 hex chars) for body.randao_reveal (BLS signature size).
const randaoReveal96Hex = "0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// blindedBlockJSON builds a minimal Fulu (Electra-shaped) SignedBlindedBeaconBlock
// whose execution_payload_header.block_hash matches the builder-specs Fulu example.
func blindedBlockJSON() string {
	return `{"message":{"slot":"1","proposer_index":"0","parent_root":"0x0000000000000000000000000000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","body":{"randao_reveal":"` + randaoReveal96Hex + `","eth1_data":{"deposit_root":"0x0000000000000000000000000000000000000000000000000000000000000000","deposit_count":"0","block_hash":"0x0000000000000000000000000000000000000000000000000000000000000000"},"graffiti":"0x0000000000000000000000000000000000000000000000000000000000000000","proposer_slashings":[],"attester_slashings":[],"attestations":[],"deposits":[],"voluntary_exits":[],"sync_aggregate":{"sync_committee_bits":"0x","sync_committee_signature":"` + randaoReveal96Hex + `"},"execution_payload_header":{"parent_hash":"0x0000000000000000000000000000000000000000000000000000000000000000","fee_recipient":"0x0000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","receipts_root":"0x0000000000000000000000000000000000000000000000000000000000000000","logs_bloom":"` + logsBloom256Hex + `","prev_randao":"0x0000000000000000000000000000000000000000000000000000000000000000","block_number":"0","gas_limit":"0","gas_used":"0","timestamp":"0","extra_data":"0x","base_fee_per_gas":"0","block_hash":"0xcf8e0d4e9587369b2301d0790347320302cc0943d5a1884560367e8208d920f2","transactions_root":"0x0000000000000000000000000000000000000000000000000000000000000000","withdrawals_root":"0x0000000000000000000000000000000000000000000000000000000000000000","blob_gas_used":"0","excess_blob_gas":"0"},"bls_to_execution_changes":[],"blob_kzg_commitments":[],"execution_requests":{"deposits":[],"withdrawals":[],"consolidations":[]}}},"signature":"` + randaoReveal96Hex + `"}`
}

// seedPayload stores a payload matching the builder-specs Fulu example block hash
// in the handler's payload cache and returns it.
func seedPayload(h *Handler, blockValue *big.Int) *payload_builder.Payload {
	payload := &eth2all.ExecutionPayload{
		Version:     version.DataVersionFulu,
		ParentHash:  phase0.Hash32(blockHashFromBuilderSpecsFulu),
		BlockNumber: 1,
		GasLimit:    1,
		GasUsed:     1,
		Timestamp:   1,
		BlockHash:   phase0.Hash32(blockHashFromBuilderSpecsFulu),
	}
	event := &payload_builder.Payload{
		Attributes:       &beacon.PayloadAttributesEvent{ProposalSlot: 1},
		ExecutionPayload: payload,
		BlockHash:        phase0.Hash32(blockHashFromBuilderSpecsFulu),
		BlockValue:       blockValue,
	}
	h.payloadCache.Store(event)

	return event
}

// TestHandleSubmitBlindedBlock_Success returns 202 and publishes the
// unblinded block.
func TestHandleSubmitBlindedBlock_Success(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)

	publisher := &stubBlockPublisher{}
	h.SetBlockPublisher(publisher)

	blockValue := new(big.Int).SetUint64(1_500_000_000_000_000) // 0.0015 ETH in wei
	seedPayload(h, blockValue)

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "submit blinded block should return 202 Accepted")
	require.NotNil(t, publisher.lastContents, "publisher should be called with unblinded contents")
	require.NotNil(t, publisher.lastContents.SignedBlock, "unblinded contents should have SignedBlock")
	assert.Equal(t, version.DataVersionFulu, publisher.lastContents.Version,
		"contents version should match the active fork")
	assert.Equal(t, phase0.Slot(1), publisher.lastContents.SignedBlock.Message.Slot)
	assert.Equal(t, uint64(1), h.BlocksPublished())
}

// TestHandleSubmitBlindedBlock_ConsensusVersionHeader decodes the blinded
// block with the fork version from the Eth-Consensus-Version header instead
// of the chain's current fork.
func TestHandleSubmitBlindedBlock_ConsensusVersionHeader(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)

	publisher := &stubBlockPublisher{}
	h.SetBlockPublisher(publisher)

	seedPayload(h, new(big.Int).SetUint64(1_500_000_000_000_000))

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "electra")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "submit blinded block should return 202 Accepted")
	require.NotNil(t, publisher.lastContents, "publisher should be called with unblinded contents")
	assert.Equal(t, version.DataVersionElectra, publisher.lastContents.Version,
		"contents version should follow the Eth-Consensus-Version header")
}

// TestHandleSubmitBlindedBlock_InvalidConsensusVersionHeader returns 400 for
// an unrecognised Eth-Consensus-Version header.
func TestHandleSubmitBlindedBlock_InvalidConsensusVersionHeader(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "notafork")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "invalid consensus version header should return 400")
}

// TestHandleSubmitBlindedBlock_Disabled returns 503 when the dialect is disabled.
func TestHandleSubmitBlindedBlock_Disabled(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	// not enabled

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "disabled submit should return 503")
}

// TestHandleSubmitBlindedBlock_PostGloasForkGuard returns 400 once the chain
// has activated Gloas.
func TestHandleSubmitBlindedBlock_PostGloasForkGuard(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionGloas}, nil)
	h.SetEnabled(true)

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "post-Gloas submit should return 400")
}
