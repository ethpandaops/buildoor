package legacy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// signedRegistration builds a validator registration signed with
// DOMAIN_APPLICATION_BUILDER at (0, 0), which VerifyRegistrationWithDomain
// accepts against the zero-genesis stub chain service.
func signedRegistration(t *testing.T, blsSigner *signer.BLSSigner,
	gasLimit uint64) *apiv1.SignedValidatorRegistration {
	t.Helper()

	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i)
	}

	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     gasLimit,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       blsSigner.PublicKey(),
	}

	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)

	var root phase0.Root
	copy(root[:], messageRoot[:])

	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, phase0.Version{}, phase0.Root{})
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	return &apiv1.SignedValidatorRegistration{Message: msg, Signature: sig}
}

// TestRegistrationCodecRoundTrip pins the persisted key/value encoding of
// validator registrations.
func TestRegistrationCodecRoundTrip(t *testing.T) {
	codec := RegistrationCodec{}

	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	reg := signedRegistration(t, blsSigner, 30_000_000)

	// Key round trip (0x-hex pubkey).
	key := codec.EncodeKey(reg.Message.Pubkey)
	assert.Regexp(t, "^0x[0-9a-f]{96}$", key)

	decodedKey, err := codec.DecodeKey(key)
	require.NoError(t, err)
	assert.Equal(t, reg.Message.Pubkey, decodedKey)

	// Value round trip (SSZ).
	value, err := codec.EncodeValue(reg)
	require.NoError(t, err)

	decoded, err := codec.DecodeValue(value)
	require.NoError(t, err)
	require.NotNil(t, decoded.Message)
	assert.Equal(t, reg.Message.Pubkey, decoded.Message.Pubkey)
	assert.Equal(t, reg.Message.FeeRecipient, decoded.Message.FeeRecipient)
	assert.Equal(t, reg.Message.GasLimit, decoded.Message.GasLimit)
	assert.Equal(t, reg.Message.Timestamp.Unix(), decoded.Message.Timestamp.Unix())
	assert.Equal(t, reg.Signature, decoded.Signature)
}

// TestRegistrationCodecRejectsInvalid pins nil/garbage rejection.
func TestRegistrationCodecRejectsInvalid(t *testing.T) {
	codec := RegistrationCodec{}

	_, err := codec.EncodeValue(nil)
	assert.Error(t, err, "nil registration must not encode")

	_, err = codec.EncodeValue(&apiv1.SignedValidatorRegistration{})
	assert.Error(t, err, "registration without message must not encode")

	_, err = codec.DecodeValue([]byte{0x01, 0x02})
	assert.Error(t, err, "garbage value must not decode")

	_, err = codec.DecodeKey("not-hex")
	assert.Error(t, err, "non-hex key must not decode")

	_, err = codec.DecodeKey("0xabcd")
	assert.Error(t, err, "short key must not decode")
}

// fakePersistence records the last flushed batch for tests.
type fakePersistence struct {
	mu      sync.Mutex
	batches int
	upserts map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration
}

func (f *fakePersistence) Load() (map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration, error) {
	return nil, nil
}

func (f *fakePersistence) PersistBatch(
	upserts map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration, _ []phase0.BLSPubKey,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.batches++

	if f.upserts == nil {
		f.upserts = make(map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration, len(upserts))
	}

	for key, value := range upserts {
		f.upserts[key] = value
	}

	return nil
}

func (f *fakePersistence) get(pubkey phase0.BLSPubKey) *apiv1.SignedValidatorRegistration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.upserts[pubkey]
}

// TestHandleRegisterValidators_OverwriteAndFlush asserts that a re-registration
// overwrites the stored entry (replace policy) and that the latest value
// reaches the attached persistence on flush.
func TestHandleRegisterValidators_OverwriteAndFlush(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	store := memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
	persistence := &fakePersistence{}
	store.SetPersistence(context.Background(), persistence, logrus.New())

	defer store.Stop()

	h := NewHandler(&config.BuilderAPIConfig{}, logrus.New(), &stubChainService{},
		newServingPlanService(&stubChainService{}), payload_builder.NewPayloadCache(10), store, blsSigner)
	h.SetEnabled(true)

	register := func(gasLimit uint64) {
		body, err := json.Marshal([]*apiv1.SignedValidatorRegistration{
			signedRegistration(t, blsSigner, gasLimit),
		})
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		h.HandleRegisterValidators(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}

	register(30_000_000)
	register(40_000_000)

	// The later registration overwrites the earlier one.
	require.Equal(t, 1, store.Len())
	stored, ok := store.Get(blsSigner.PublicKey())
	require.True(t, ok)
	assert.Equal(t, uint64(40_000_000), stored.Message.GasLimit)

	// A flush hands the latest value to the persistence adapter.
	require.NoError(t, store.Flush())

	persisted := persistence.get(blsSigner.PublicKey())
	require.NotNil(t, persisted, "flushed batch must contain the registration")
	assert.Equal(t, uint64(40_000_000), persisted.Message.GasLimit)
}

// TestRegistrationSettingsResolver pins the pre-Gloas resolver semantics:
// fee recipient from the registration pre-Gloas, self-scoped false post-Gloas
// and for unknown proposers.
func TestRegistrationSettingsResolver(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	pubkey := blsSigner.PublicKey()
	reg := signedRegistration(t, blsSigner, 30_000_000)

	store := memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
	store.Put(pubkey, reg)

	newResolver := func(fork version.DataVersion) *RegistrationSettingsResolver {
		return NewRegistrationSettingsResolver(store, &stubChainService{
			currentFork:   fork,
			pubkeyByIndex: map[phase0.ValidatorIndex]phase0.BLSPubKey{7: pubkey},
		})
	}

	// Pre-Gloas with a registration: fee recipient resolved, gas limit
	// deliberately not announced.
	settings, ok := newResolver(version.DataVersionFulu).ResolveProposerSettings(1, 7)
	require.True(t, ok)
	assert.Equal(t, reg.Message.FeeRecipient[:], settings.FeeRecipient[:])
	assert.Zero(t, settings.TargetGasLimit, "registration gas limit must not be announced")

	// Post-Gloas: the resolver self-scopes out.
	_, ok = newResolver(version.DataVersionGloas).ResolveProposerSettings(1, 7)
	assert.False(t, ok)

	// Unknown proposer index: no pubkey, no match.
	_, ok = newResolver(version.DataVersionFulu).ResolveProposerSettings(1, 8)
	assert.False(t, ok)

	// Known index but no registration for the pubkey.
	emptyResolver := NewRegistrationSettingsResolver(
		memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration](),
		&stubChainService{
			currentFork:   version.DataVersionFulu,
			pubkeyByIndex: map[phase0.ValidatorIndex]phase0.BLSPubKey{7: pubkey},
		})
	_, ok = emptyResolver.ResolveProposerSettings(1, 7)
	assert.False(t, ok)
}
