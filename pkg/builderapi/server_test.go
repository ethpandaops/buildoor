package builderapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	apiv1fulu "github.com/attestantio/go-eth2-client/api/v1/fulu"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// mockPayloadCacheProvider provides a payload cache for tests without full builder deps.
type mockPayloadCacheProvider struct {
	cache *builder.PayloadCache
}

func (m *mockPayloadCacheProvider) GetPayloadCache() *builder.PayloadCache {
	return m.cache
}

func TestRegisterValidators_BuilderSpecsExample(t *testing.T) {
	// Uses the official builder-specs example from validators/testdata/signed_validator_registrations.json
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(validators.BuilderSpecsExampleJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	// Builder-specs example uses a placeholder signature; verification fails → 400.
	assert.Equal(t, http.StatusBadRequest, rec.Code, "POST /eth/v1/builder/validators with builder-specs example (invalid sig) should return 400")
}

func TestRegisterValidators_EmptyArray(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("[]")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, srv.validatorsStore.Len())
}

func TestRegisterValidators_InvalidJSON(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegisterValidators_ValidSignature(t *testing.T) {
	// Use a test BLS key to create a valid signed registration.
	// Generated minimal key that herumi accepts (all zeros except last byte for valid scalar).
	testPrivkey := "0x0000000000000000000000000000000000000000000000000000000000000001"
	blsSigner, err := signer.NewBLSSigner(testPrivkey)
	require.NoError(t, err)

	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i)
	}
	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     30_000_000,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       blsSigner.PublicKey(),
	}

	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])

	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	reg := &apiv1.SignedValidatorRegistration{
		Message:   msg,
		Signature: sig,
	}
	require.True(t, validators.VerifyRegistration(reg), "test registration must verify")

	body, err := json.Marshal([]*apiv1.SignedValidatorRegistration{reg})
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, srv.validatorsStore.Len())
	stored := srv.validatorsStore.Get(blsSigner.PublicKey())
	require.NotNil(t, stored)
	assert.Equal(t, msg.GasLimit, stored.Message.GasLimit)
}

func TestRegisterValidators_MissingContentType(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("[]")))
	// no Content-Type
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

// TestGetHeader_NoPayload returns 204 when builderSvc is nil (no payload cache).
func TestGetHeader_NoPayload(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	srv := NewServer(cfg, log, nil, blsSigner, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/header/1/0x0000000000000000000000000000000000000000000000000000000000000000/0x" + hex.EncodeToString(pk[:])
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// TestGetHeader_InvalidSlot returns 400 for non-numeric slot when builder and signer are set.
func TestGetHeader_InvalidSlot(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	blsSigner, _ := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	mock := &mockPayloadCacheProvider{cache: builder.NewPayloadCache(10)}
	srv := NewServer(cfg, log, mock, blsSigner, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/header/not_a_number/0x0000000000000000000000000000000000000000000000000000000000000000/0x" + hex.EncodeToString(pk[:])
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestGetHeader_SubsidyInBidValue returns 200 with bid value = block_value + BlockValueSubsidyGwei.
func TestGetHeader_SubsidyInBidValue(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	pk := blsSigner.PublicKey()

	// Create valid registration so getHeader does not return 204 for unregistered proposer
	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i)
	}
	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     30_000_000,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       pk,
	}
	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, phase0.Version{}, phase0.Root{})
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)
	reg := &apiv1.SignedValidatorRegistration{Message: msg, Signature: sig}
	require.True(t, validators.VerifyRegistration(reg), "test registration must verify")
	regs, err := json.Marshal([]*apiv1.SignedValidatorRegistration{reg})
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{Port: 0, BlockValueSubsidyGwei: 1_000_000}
	log := logrus.New()
	cache := builder.NewPayloadCache(10)
	parentHash := phase0.Hash32(common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"))
	payload := &engine.ExecutionPayload{
		ParentHash:    common.Hash(parentHash),
		FeeRecipient:  common.Address{},
		StateRoot:     common.Hash{},
		ReceiptsRoot:  common.Hash{},
		BlockNumber:   1,
		GasLimit:      30_000_000,
		GasUsed:       0,
		Timestamp:     1,
		BlockHash:     common.HexToHash("0xab00000000000000000000000000000000000000000000000000000000000000"),
		Transactions:  nil,
		Withdrawals:   nil,
		BlobGasUsed:   0,
		ExcessBlobGas: 0,
	}
	event := &builder.PayloadReadyEvent{
		Slot:            1,
		ParentBlockHash: parentHash,
		BlockHash:       phase0.Hash32(payload.BlockHash),
		Payload:         payload,
		BlockValue:      500_000, // 0.0005 ETH in Gwei
	}
	cache.Store(event)
	mock := &mockPayloadCacheProvider{cache: cache}
	srv := NewServer(cfg, log, mock, blsSigner, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(regs))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "register validators")

	url := "/eth/v1/builder/header/1/0x0000000000000000000000000000000000000000000000000000000000000001/0x" + hex.EncodeToString(pk[:])
	req = httptest.NewRequest(http.MethodGet, url, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "getHeader should return 200")
	var resp struct {
		Data struct {
			Message struct {
				Value string `json:"value"`
			} `json:"message"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	// block_value 500_000 + subsidy 1_000_000 = 1_500_000
	assert.Equal(t, "1500000", resp.Data.Message.Value, "bid value should be block_value + subsidy")
}

// TestSubmitBlindedBlockV2_InvalidJSON returns 400 for invalid JSON body.
func TestSubmitBlindedBlockV2_InvalidJSON(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSubmitBlindedBlockV2_MissingContentType returns 415 when Content-Type is not application/json.
func TestSubmitBlindedBlockV2_MissingContentType(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

// mockFuluPublisher records the last SubmitFuluBlock call for tests.
type mockFuluPublisher struct {
	lastContents *apiv1fulu.SignedBlockContents
	lastErr      error
}

func (m *mockFuluPublisher) SubmitFuluBlock(_ context.Context, contents *apiv1fulu.SignedBlockContents) error {
	m.lastContents = contents
	m.lastErr = nil
	return nil
}

// TestSubmitBlindedBlockV2_NoMatchingPayload returns 400 when no payload in cache matches block_hash.
func TestSubmitBlindedBlockV2_NoMatchingPayload(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	mock := &mockPayloadCacheProvider{cache: builder.NewPayloadCache(10)}
	srv := NewServer(cfg, log, mock, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	// Minimal Fulu (Electra-shaped) blinded block body: message.body.execution_payload_header.block_hash that won't be in cache
	body := `{"message":{"slot":"1","proposer_index":"0","parent_root":"0x0000000000000000000000000000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","body":{"randao_reveal":"0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000","eth1_data":{"deposit_root":"0x0000000000000000000000000000000000000000000000000000000000000000","deposit_count":"0","block_hash":"0x0000000000000000000000000000000000000000000000000000000000000000"},"graffiti":"0x0000000000000000000000000000000000000000000000000000000000000000","execution_payload_header":{"parent_hash":"0x0000000000000000000000000000000000000000000000000000000000000000","fee_recipient":"0x0000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","receipts_root":"0x0000000000000000000000000000000000000000000000000000000000000000","logs_bloom":"0x00","prev_randao":"0x0000000000000000000000000000000000000000000000000000000000000000","block_number":"0","gas_limit":"0","gas_used":"0","timestamp":"0","extra_data":"0x","base_fee_per_gas":"0","block_hash":"0xffff000000000000000000000000000000000000000000000000000000000000","transactions_root":"0x0000000000000000000000000000000000000000000000000000000000000000","withdrawals_root":"0x0000000000000000000000000000000000000000000000000000000000000000","blob_gas_used":"0","excess_blob_gas":"0"}},"signature":"0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"}`
	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// blockHashFromBuilderSpecsFulu matches the execution_payload.block_hash in builder-specs examples/fulu/.
var blockHashFromBuilderSpecsFulu = common.HexToHash("0xcf8e0d4e9587369b2301d0790347320302cc0943d5a1884560367e8208d920f2")

// logsBloom256Hex is 256 bytes (512 hex chars) for execution_payload_header.logs_bloom.
const logsBloom256Hex = "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// randaoReveal96Hex is 96 bytes (192 hex chars) for body.randao_reveal (BLS signature size).
const randaoReveal96Hex = "0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

// TestSubmitBlindedBlockV2_Success_UnblindAndPublish returns 202 and calls the publisher with unblinded contents
// when the cache has a matching payload (block_hash from builder-specs Fulu examples).
func TestSubmitBlindedBlockV2_Success_UnblindAndPublish(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	cache := builder.NewPayloadCache(10)
	mockCache := &mockPayloadCacheProvider{cache: cache}
	publisher := &mockFuluPublisher{}
	srv := NewServer(cfg, log, mockCache, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetFuluPublisher(publisher)

	// Seed cache with a payload matching builder-specs Fulu example block_hash.
	payload := &engine.ExecutionPayload{
		ParentHash:    blockHashFromBuilderSpecsFulu,
		FeeRecipient:  common.Address{},
		StateRoot:     common.Hash{},
		ReceiptsRoot:  common.Hash{},
		BlockNumber:   1,
		GasLimit:      1,
		GasUsed:       1,
		Timestamp:     1,
		ExtraData:     nil,
		BaseFeePerGas: nil,
		BlockHash:     blockHashFromBuilderSpecsFulu,
		Transactions:  nil,
		Withdrawals:   nil,
		BlobGasUsed:   0,
		ExcessBlobGas: 0,
	}
	event := &builder.PayloadReadyEvent{
		Slot:        1,
		BlockHash:   phase0.Hash32(blockHashFromBuilderSpecsFulu),
		Payload:     payload,
		BlobsBundle: nil,
	}
	cache.Store(event)

	// Blinded block JSON: Electra body with required arrays; block_hash matching builder-specs Fulu example.
	body := `{"message":{"slot":"1","proposer_index":"0","parent_root":"0x0000000000000000000000000000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","body":{"randao_reveal":"` + randaoReveal96Hex + `","eth1_data":{"deposit_root":"0x0000000000000000000000000000000000000000000000000000000000000000","deposit_count":"0","block_hash":"0x0000000000000000000000000000000000000000000000000000000000000000"},"graffiti":"0x0000000000000000000000000000000000000000000000000000000000000000","proposer_slashings":[],"attester_slashings":[],"attestations":[],"deposits":[],"voluntary_exits":[],"sync_aggregate":{"sync_committee_bits":"0x","sync_committee_signature":"` + randaoReveal96Hex + `"},"execution_payload_header":{"parent_hash":"0x0000000000000000000000000000000000000000000000000000000000000000","fee_recipient":"0x0000000000000000000000000000000000000000","state_root":"0x0000000000000000000000000000000000000000000000000000000000000000","receipts_root":"0x0000000000000000000000000000000000000000000000000000000000000000","logs_bloom":"` + logsBloom256Hex + `","prev_randao":"0x0000000000000000000000000000000000000000000000000000000000000000","block_number":"0","gas_limit":"0","gas_used":"0","timestamp":"0","extra_data":"0x","base_fee_per_gas":"0","block_hash":"0xcf8e0d4e9587369b2301d0790347320302cc0943d5a1884560367e8208d920f2","transactions_root":"0x0000000000000000000000000000000000000000000000000000000000000000","withdrawals_root":"0x0000000000000000000000000000000000000000000000000000000000000000","blob_gas_used":"0","excess_blob_gas":"0"},"bls_to_execution_changes":[],"blob_kzg_commitments":[],"execution_requests":{"deposits":[],"withdrawals":[],"consolidations":[]}}},"signature":"` + randaoReveal96Hex + `"}`
	req := httptest.NewRequest(http.MethodPost, "/eth/v2/builder/blinded_blocks", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code, "submit blinded block should return 202 Accepted")
	require.NotNil(t, publisher.lastContents, "publisher should be called with unblinded contents")
	require.NotNil(t, publisher.lastContents.SignedBlock, "unblinded contents should have SignedBlock")
	require.NotNil(t, publisher.lastContents.SignedBlock.Message, "SignedBlock should have Message")
	assert.Equal(t, phase0.Slot(1), publisher.lastContents.SignedBlock.Message.Slot)
	assert.Equal(t, blockHashFromBuilderSpecsFulu, common.Hash(publisher.lastContents.SignedBlock.Message.Body.ExecutionPayload.BlockHash), "unblinded block should have matching block hash")
}
