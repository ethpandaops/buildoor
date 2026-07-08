package legacy

import (
	"encoding/hex"
	"fmt"
	"strings"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// RegistrationsNamespace is the kv_store namespace holding the Builder API
// validator registrations.
const RegistrationsNamespace = "validator_registrations"

// RegistrationCodec translates the validator registration store's entries to
// their persisted form: 0x-hex pubkey keys, SSZ-encoded values.
type RegistrationCodec struct{}

var _ db.KVCodec[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration] = RegistrationCodec{}

// EncodeKey encodes a validator pubkey as its 0x-prefixed hex string form.
func (RegistrationCodec) EncodeKey(pubkey phase0.BLSPubKey) string {
	return "0x" + hex.EncodeToString(pubkey[:])
}

// DecodeKey parses a 0x-prefixed hex pubkey string.
func (RegistrationCodec) DecodeKey(key string) (phase0.BLSPubKey, error) {
	var pubkey phase0.BLSPubKey

	raw, err := hex.DecodeString(strings.TrimPrefix(key, "0x"))
	if err != nil {
		return pubkey, fmt.Errorf("invalid validator registration pubkey key %q: %w", key, err)
	}

	if len(raw) != len(pubkey) {
		return pubkey, fmt.Errorf("invalid validator registration pubkey key %q: got %d bytes, want %d",
			key, len(raw), len(pubkey))
	}

	copy(pubkey[:], raw)

	return pubkey, nil
}

// EncodeValue SSZ-encodes a signed validator registration.
func (RegistrationCodec) EncodeValue(reg *apiv1.SignedValidatorRegistration) ([]byte, error) {
	if reg == nil || reg.Message == nil {
		return nil, fmt.Errorf("cannot encode nil validator registration")
	}

	return reg.MarshalSSZ()
}

// DecodeValue SSZ-decodes a signed validator registration.
func (RegistrationCodec) DecodeValue(value []byte) (*apiv1.SignedValidatorRegistration, error) {
	reg := &apiv1.SignedValidatorRegistration{}
	if err := reg.UnmarshalSSZ(value); err != nil {
		return nil, fmt.Errorf("failed to decode validator registration: %w", err)
	}

	return reg, nil
}
