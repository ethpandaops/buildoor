package payload_bidder

import (
	"fmt"
	"strconv"

	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// ProposerPreferencesNamespace is the kv_store namespace holding the cached
// proposer preferences.
const ProposerPreferencesNamespace = "proposer_preferences"

// ProposerPreferencesCodec translates the proposer preference store's entries
// to their persisted form: decimal slot string keys, SSZ-encoded values.
type ProposerPreferencesCodec struct{}

var _ db.KVCodec[phase0.Slot, *gloasspec.SignedProposerPreferences] = ProposerPreferencesCodec{}

// EncodeKey encodes a slot as its decimal string form.
func (ProposerPreferencesCodec) EncodeKey(slot phase0.Slot) string {
	return strconv.FormatUint(uint64(slot), 10)
}

// DecodeKey parses a decimal slot string.
func (ProposerPreferencesCodec) DecodeKey(key string) (phase0.Slot, error) {
	slot, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid proposer preference slot key %q: %w", key, err)
	}

	return phase0.Slot(slot), nil
}

// EncodeValue SSZ-encodes a signed proposer preference.
func (ProposerPreferencesCodec) EncodeValue(prefs *gloasspec.SignedProposerPreferences) ([]byte, error) {
	if prefs == nil || prefs.Message == nil {
		return nil, fmt.Errorf("cannot encode nil proposer preferences")
	}

	return prefs.MarshalSSZ()
}

// DecodeValue SSZ-decodes a signed proposer preference.
func (ProposerPreferencesCodec) DecodeValue(value []byte) (*gloasspec.SignedProposerPreferences, error) {
	prefs := &gloasspec.SignedProposerPreferences{}
	if err := prefs.UnmarshalSSZ(value); err != nil {
		return nil, fmt.Errorf("failed to decode proposer preferences: %w", err)
	}

	return prefs, nil
}
