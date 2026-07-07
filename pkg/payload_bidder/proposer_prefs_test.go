package payload_bidder

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// newTestSignedPrefs builds a minimal signed proposer preference for a slot.
func newTestSignedPrefs(slot phase0.Slot, validatorIndex phase0.ValidatorIndex,
	feeRecipient byte, targetGasLimit uint64) *gloasspec.SignedProposerPreferences {
	prefs := &gloasspec.SignedProposerPreferences{
		Message: &gloasspec.ProposerPreferences{
			ProposalSlot:   slot,
			ValidatorIndex: validatorIndex,
			TargetGasLimit: targetGasLimit,
		},
	}
	prefs.Message.FeeRecipient = bellatrix.ExecutionAddress{feeRecipient}

	return prefs
}

// newTestPropPrefsService builds a service around the given stub chain
// (clClient nil — the service is never started in these tests).
func newTestPropPrefsService(chainSvc *stubChainService) *ProposerPreferencesService {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return NewProposerPreferencesService(nil, chainSvc, log)
}

func TestProposerPreferencesService_KeepsFirstPreferencePerSlot(t *testing.T) {
	svc := newTestPropPrefsService(&stubChainService{
		genesisTime:  time.Now(),
		slotDuration: time.Second,
		currentFork:  version.DataVersionGloas,
	})

	first := newTestSignedPrefs(100, 1, 0xaa, 30_000_000)
	second := newTestSignedPrefs(100, 1, 0xbb, 40_000_000)

	svc.handleEvent(first)
	svc.handleEvent(second) // same slot → ignored

	got, ok := svc.GetStore().Get(100)
	require.True(t, ok)
	assert.Same(t, first, got, "first preference for a slot must win")
	assert.Equal(t, 1, svc.GetStore().Len())

	// Nil events are dropped without caching anything.
	svc.handleEvent(nil)
	svc.handleEvent(&gloasspec.SignedProposerPreferences{})
	assert.Equal(t, 1, svc.GetStore().Len())
}

func TestProposerPreferencesService_PrunesPastSlots(t *testing.T) {
	svc := newTestPropPrefsService(&stubChainService{
		genesisTime:  time.Now(),
		slotDuration: time.Second,
		currentFork:  version.DataVersionGloas,
	})

	// SlotsPerEpoch is 32 in the stub chain spec.
	for _, slot := range []phase0.Slot{0, 31, 32, 40} {
		svc.handleEvent(newTestSignedPrefs(slot, 1, 0x01, 0))
	}

	require.Equal(t, 4, svc.GetStore().Len())

	// Epoch 1 starts at slot 32 → slots 0 and 31 are in the past.
	svc.pruneForEpoch(1)

	assert.Equal(t, 2, svc.GetStore().Len())
	assert.False(t, svc.GetStore().Has(0))
	assert.False(t, svc.GetStore().Has(31))
	assert.True(t, svc.GetStore().Has(32))
	assert.True(t, svc.GetStore().Has(40))
}

func TestProposerPreferencesService_ResolveProposerSettings(t *testing.T) {
	chainSvc := &stubChainService{
		genesisTime:  time.Now(),
		slotDuration: time.Second,
		currentFork:  version.DataVersionGloas,
	}
	svc := newTestPropPrefsService(chainSvc)

	svc.handleEvent(newTestSignedPrefs(64, 7, 0xcc, 45_000_000))

	// Known slot post-Gloas → settings from the cached preference.
	settings, ok := svc.ResolveProposerSettings(64, 7)
	require.True(t, ok)
	assert.Equal(t, common.Address{0xcc}, settings.FeeRecipient)
	assert.Equal(t, uint64(45_000_000), settings.TargetGasLimit)

	// Unknown slot → no match.
	_, ok = svc.ResolveProposerSettings(65, 7)
	assert.False(t, ok)

	// Pre-Gloas fork → resolver self-scopes out even for a cached slot.
	chainSvc.currentFork = version.DataVersionFulu
	_, ok = svc.ResolveProposerSettings(64, 7)
	assert.False(t, ok)

	// Interface compliance is asserted at compile time in proposer_prefs.go;
	// double-check the concrete value satisfies it too.
	var _ payload_builder.ProposerSettingsResolver = svc
}

func TestProposerPreferencesCodec_RoundTrip(t *testing.T) {
	codec := ProposerPreferencesCodec{}

	// Key round-trip.
	key := codec.EncodeKey(12345)
	assert.Equal(t, "12345", key)

	slot, err := codec.DecodeKey(key)
	require.NoError(t, err)
	assert.Equal(t, phase0.Slot(12345), slot)

	_, err = codec.DecodeKey("not-a-slot")
	assert.Error(t, err)

	// Value round-trip.
	prefs := newTestSignedPrefs(12345, 42, 0xee, 36_000_000)
	prefs.Message.DependentRoot = phase0.Root{0x11}
	prefs.Signature = phase0.BLSSignature{0x22}

	encoded, err := codec.EncodeValue(prefs)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := codec.DecodeValue(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.Message)
	assert.Equal(t, prefs.Message.ProposalSlot, decoded.Message.ProposalSlot)
	assert.Equal(t, prefs.Message.ValidatorIndex, decoded.Message.ValidatorIndex)
	assert.Equal(t, prefs.Message.FeeRecipient, decoded.Message.FeeRecipient)
	assert.Equal(t, prefs.Message.TargetGasLimit, decoded.Message.TargetGasLimit)
	assert.Equal(t, prefs.Message.DependentRoot, decoded.Message.DependentRoot)
	assert.Equal(t, prefs.Signature, decoded.Signature)

	// Nil values must be rejected instead of persisted.
	_, err = codec.EncodeValue(nil)
	assert.Error(t, err)

	_, err = codec.EncodeValue(&gloasspec.SignedProposerPreferences{})
	assert.Error(t, err)

	_, err = codec.DecodeValue([]byte{0x01})
	assert.Error(t, err)
}
