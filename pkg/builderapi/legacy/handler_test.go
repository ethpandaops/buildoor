package legacy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/api"
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
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

// stubProposalSubmitter records the last SubmitProposal call for tests.
type stubProposalSubmitter struct {
	lastProposal *api.VersionedSignedProposal
	err          error
}

func (p *stubProposalSubmitter) SubmitProposal(_ context.Context, opts *api.SubmitProposalOpts) error {
	p.lastProposal = opts.Proposal
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

// TestHandleGetHeader_Success serves the bid as JSON by default and as SSZ
// when the Accept header prefers application/octet-stream, with identical
// bid contents in both representations.
func TestHandleGetHeader_Success(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	store := memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
	h := NewHandler(&config.BuilderAPIConfig{}, logrus.New(),
		&stubChainService{currentFork: version.DataVersionFulu},
		payload_builder.NewPayloadCache(10), store, blsSigner)
	h.SetEnabled(true)

	pk := blsSigner.PublicKey()
	store.Put(pk, &apiv1.SignedValidatorRegistration{})
	seedPayload(h, big.NewInt(1_000_000_000))

	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	newGetHeaderRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet,
			"/eth/v1/builder/header/1/"+zeroHash+"/0x"+hex.EncodeToString(pk[:]), nil)
		return mux.SetURLVars(req, map[string]string{
			"slot":        "1",
			"parent_hash": zeroHash,
			"pubkey":      "0x" + hex.EncodeToString(pk[:]),
		})
	}

	// Default (no Accept header): JSON envelope.
	rec := httptest.NewRecorder()
	h.HandleGetHeader(rec, newGetHeaderRequest())

	require.Equal(t, http.StatusOK, rec.Code, "getHeader should return 200")
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "fulu", rec.Header().Get("Eth-Consensus-Version"))

	var envelope struct {
		Version string          `json:"version"`
		Data    json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "fulu", envelope.Version)

	jsonBid := &legacytypes.SignedBuilderBid{Version: version.DataVersionFulu}
	require.NoError(t, json.Unmarshal(envelope.Data, jsonBid))
	require.NotNil(t, jsonBid.Message)

	// Accept: application/octet-stream → SSZ body.
	rec = httptest.NewRecorder()
	req := newGetHeaderRequest()
	req.Header.Set("Accept", "application/octet-stream;q=1.0,application/json;q=0.9")
	h.HandleGetHeader(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "getHeader (SSZ) should return 200")
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "fulu", rec.Header().Get("Eth-Consensus-Version"))

	sszBid := &legacytypes.SignedBuilderBid{Version: version.DataVersionFulu}
	require.NoError(t, sszBid.UnmarshalSSZ(rec.Body.Bytes()))
	require.NotNil(t, sszBid.Message)

	// Both representations must carry the same bid.
	jsonRoot, err := jsonBid.Message.HashTreeRoot()
	require.NoError(t, err)
	sszRoot, err := sszBid.Message.HashTreeRoot()
	require.NoError(t, err)
	assert.Equal(t, jsonRoot, sszRoot, "JSON and SSZ bids must be identical")
	assert.Equal(t, jsonBid.Signature, sszBid.Signature)
	assert.Equal(t, pk, sszBid.Message.Pubkey)
}

// TestPreferSSZ exercises the Accept-header content negotiation.
func TestPreferSSZ(t *testing.T) {
	tests := []struct {
		accept string
		ssz    bool
	}{
		{accept: "", ssz: false},
		{accept: "application/json", ssz: false},
		{accept: "application/octet-stream", ssz: true},
		{accept: "*/*", ssz: false},
		{accept: "application/octet-stream;q=1.0,application/json;q=0.9", ssz: true},
		{accept: "application/octet-stream;q=0.5,application/json", ssz: false},
		{accept: "application/octet-stream, */*;q=0.1", ssz: true},
		{accept: "application/json;q=0.9, application/octet-stream;q=0.9", ssz: false},
	}
	for _, test := range tests {
		assert.Equal(t, test.ssz, preferSSZ(test.accept), "accept=%q", test.accept)
	}
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

	submitter := &stubProposalSubmitter{}
	h.SetCLClient(submitter)

	blockValue := new(big.Int).SetUint64(1_500_000_000_000_000) // 0.0015 ETH in wei
	seedPayload(h, blockValue)

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "submit blinded block should return 202 Accepted")
	require.NotNil(t, submitter.lastProposal, "CL client should be called with the unblinded proposal")
	assert.Equal(t, version.DataVersionFulu, submitter.lastProposal.Version,
		"proposal version should match the active fork")
	require.NotNil(t, submitter.lastProposal.Fulu, "proposal should carry Fulu block contents")
	assert.Equal(t, phase0.Slot(1), submitter.lastProposal.Fulu.SignedBlock.Message.Slot)
	assert.Equal(t, uint64(1), h.BlocksPublished())
}

// TestHandleSubmitBlindedBlock_ConsensusVersionHeader decodes the blinded
// block with the fork version from the Eth-Consensus-Version header instead
// of the chain's current fork.
func TestHandleSubmitBlindedBlock_ConsensusVersionHeader(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)

	submitter := &stubProposalSubmitter{}
	h.SetCLClient(submitter)

	seedPayload(h, new(big.Int).SetUint64(1_500_000_000_000_000))

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "electra")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "submit blinded block should return 202 Accepted")
	require.NotNil(t, submitter.lastProposal, "CL client should be called with the unblinded proposal")
	assert.Equal(t, version.DataVersionElectra, submitter.lastProposal.Version,
		"proposal version should follow the Eth-Consensus-Version header")
	require.NotNil(t, submitter.lastProposal.Electra, "proposal should carry Electra block contents")
}

// TestHandleSubmitBlindedBlock_SSZBody accepts an SSZ-encoded blinded block
// (Content-Type: application/octet-stream) with the version taken from the
// Eth-Consensus-Version header, per builder-specs.
func TestHandleSubmitBlindedBlock_SSZBody(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)

	submitter := &stubProposalSubmitter{}
	h.SetCLClient(submitter)

	seedPayload(h, new(big.Int).SetUint64(1_500_000_000_000_000))

	blinded := &apiv1all.SignedBlindedBeaconBlock{Version: version.DataVersionFulu}
	require.NoError(t, json.Unmarshal([]byte(blindedBlockJSON()), blinded))

	body, err := blinded.MarshalSSZ()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Eth-Consensus-Version", "fulu")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlock(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "SSZ submit should return 202 Accepted")
	require.NotNil(t, submitter.lastProposal, "CL client should be called with the unblinded proposal")
	assert.Equal(t, version.DataVersionFulu, submitter.lastProposal.Version)
	require.NotNil(t, submitter.lastProposal.Fulu)
	assert.Equal(t, phase0.Slot(1), submitter.lastProposal.Fulu.SignedBlock.Message.Slot)
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

// TestWriteUnblindedPayloadResponse_ForkShapes verifies the v1 submit response
// data shape per fork: the bare execution payload pre-Deneb, and an
// execution_payload + blobs_bundle wrapper (empty bundle when blobless) from
// Deneb onwards.
func TestWriteUnblindedPayloadResponse_ForkShapes(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionCapella}, nil)

	newEvent := func(fork version.DataVersion) *payload_builder.Payload {
		return &payload_builder.Payload{
			Attributes: &beacon.PayloadAttributesEvent{ProposalSlot: 1},
			ExecutionPayload: &eth2all.ExecutionPayload{
				Version:       fork,
				BaseFeePerGas: uint256.NewInt(7),
				BlockHash:     phase0.Hash32{0xab},
			},
			BlockHash: phase0.Hash32{0xab},
		}
	}

	// Capella: data is the bare execution payload (no blobs bundle pre-Deneb).
	rec := httptest.NewRecorder()
	h.writeUnblindedPayloadResponse(rec, logrus.New(), version.DataVersionCapella,
		newEvent(version.DataVersionCapella))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "capella", rec.Header().Get("Eth-Consensus-Version"))

	var capellaResp struct {
		Version string `json:"version"`
		Data    struct {
			BlockHash   string           `json:"block_hash"`
			BlobsBundle *json.RawMessage `json:"blobs_bundle"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &capellaResp))
	assert.Equal(t, "capella", capellaResp.Version)
	assert.NotEmpty(t, capellaResp.Data.BlockHash, "data must be the bare execution payload")
	assert.Nil(t, capellaResp.Data.BlobsBundle, "pre-Deneb response must not carry a blobs bundle")

	// Deneb+: data wraps the payload and a (possibly empty) blobs bundle.
	rec = httptest.NewRecorder()
	h.writeUnblindedPayloadResponse(rec, logrus.New(), version.DataVersionDeneb,
		newEvent(version.DataVersionDeneb))

	require.Equal(t, http.StatusOK, rec.Code)

	var denebResp struct {
		Version string `json:"version"`
		Data    struct {
			ExecutionPayload struct {
				BlockHash string `json:"block_hash"`
			} `json:"execution_payload"`
			BlobsBundle *struct {
				Commitments []string `json:"commitments"`
			} `json:"blobs_bundle"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &denebResp))
	assert.Equal(t, "deneb", denebResp.Version)
	assert.NotEmpty(t, denebResp.Data.ExecutionPayload.BlockHash)
	require.NotNil(t, denebResp.Data.BlobsBundle, "Deneb+ response must carry a blobs bundle")
	assert.Empty(t, denebResp.Data.BlobsBundle.Commitments)
}

// capellaBlindedBlockJSON builds a minimal capella SignedBlindedBeaconBlock
// (no blob commitments, no execution requests) whose
// execution_payload_header.block_hash matches the builder-specs example hash.
func capellaBlindedBlockJSON() string {
	return `{"message":{"slot":"1","proposer_index":"0","parent_root":"0x0000000000000000000000000000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","body":{"randao_reveal":"` + randaoReveal96Hex + `","eth1_data":{"deposit_root":"0x0000000000000000000000000000000000000000000000000000000000000000","deposit_count":"0","block_hash":"0x0000000000000000000000000000000000000000000000000000000000000000"},"graffiti":"0x0000000000000000000000000000000000000000000000000000000000000000","proposer_slashings":[],"attester_slashings":[],"attestations":[],"deposits":[],"voluntary_exits":[],"sync_aggregate":{"sync_committee_bits":"0x","sync_committee_signature":"` + randaoReveal96Hex + `"},"execution_payload_header":{"parent_hash":"0x0000000000000000000000000000000000000000000000000000000000000000","fee_recipient":"0x0000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","receipts_root":"0x0000000000000000000000000000000000000000000000000000000000000000","logs_bloom":"` + logsBloom256Hex + `","prev_randao":"0x0000000000000000000000000000000000000000000000000000000000000000","block_number":"0","gas_limit":"0","gas_used":"0","timestamp":"0","extra_data":"0x","base_fee_per_gas":"0","block_hash":"0xcf8e0d4e9587369b2301d0790347320302cc0943d5a1884560367e8208d920f2","transactions_root":"0x0000000000000000000000000000000000000000000000000000000000000000","withdrawals_root":"0x0000000000000000000000000000000000000000000000000000000000000000"},"bls_to_execution_changes":[]}},"signature":"` + randaoReveal96Hex + `"}`
}

// TestHandleSubmitBlindedBlockV1_Capella unblinds a pre-Deneb (capella)
// blinded block and publishes it as a bare SignedBeaconBlock proposal —
// SignedBlockContents is Deneb+ and must not be used there.
func TestHandleSubmitBlindedBlockV1_Capella(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionCapella}, nil)
	h.SetEnabled(true)

	submitter := &stubProposalSubmitter{}
	h.SetCLClient(submitter)

	payload := &eth2all.ExecutionPayload{
		Version:       version.DataVersionCapella,
		ParentHash:    phase0.Hash32(blockHashFromBuilderSpecsFulu),
		BlockNumber:   1,
		GasLimit:      1,
		GasUsed:       1,
		Timestamp:     1,
		BaseFeePerGas: uint256.NewInt(7),
		BlockHash:     phase0.Hash32(blockHashFromBuilderSpecsFulu),
	}
	event := &payload_builder.Payload{
		Attributes:       &beacon.PayloadAttributesEvent{ProposalSlot: 1},
		ExecutionPayload: payload,
		BlockHash:        phase0.Hash32(blockHashFromBuilderSpecsFulu),
		BlockValue:       big.NewInt(1_000_000_000),
	}
	h.payloadCache.Store(event)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/blinded_blocks",
		bytes.NewReader([]byte(capellaBlindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandleSubmitBlindedBlockV1(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "v1 capella submit should return 200: %s", rec.Body.String())
	require.NotNil(t, submitter.lastProposal, "CL client should be called with the unblinded proposal")
	assert.Equal(t, version.DataVersionCapella, submitter.lastProposal.Version)
	require.NotNil(t, submitter.lastProposal.Capella, "pre-Deneb proposal must be the bare signed block")
	assert.Equal(t, phase0.Slot(1), submitter.lastProposal.Capella.Message.Slot)
	assert.Equal(t, phase0.Hash32(blockHashFromBuilderSpecsFulu),
		submitter.lastProposal.Capella.Message.Body.ExecutionPayload.BlockHash,
		"unblinded block must carry the full execution payload")

	// v1 response body: the bare execution payload, no blobs bundle pre-Deneb.
	var resp struct {
		Version string `json:"version"`
		Data    struct {
			BlockHash   string           `json:"block_hash"`
			BlobsBundle *json.RawMessage `json:"blobs_bundle"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "capella", resp.Version)
	assert.NotEmpty(t, resp.Data.BlockHash)
	assert.Nil(t, resp.Data.BlobsBundle)
}
