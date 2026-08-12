package builder_keys

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// Namespace is the kv_store namespace holding per-key usage history.
const Namespace = "builder_keys"

// Usage is the persisted history of one builder key. It survives restarts so a
// key that was deposited in an earlier run is recognised as ours even before the
// beacon state confirms it, and so a key that has cycled out of the registry can
// be reused instead of pushing the highest derivation index up forever.
type Usage struct {
	KeyIndex uint64 `json:"key_index"`
	// Pubkey is the derived public key at the time of writing. A mismatch on
	// load means the entry key changed and the whole record set is about a
	// different fleet.
	Pubkey string `json:"pubkey"`
	// UseCount is how many deposit generations this key has gone through.
	UseCount      uint32 `json:"use_count"`
	FirstUsedAt   int64  `json:"first_used_at,omitempty"`
	LastDepositAt int64  `json:"last_deposit_at,omitempty"`
	LastExitAt    int64  `json:"last_exit_at,omitempty"`
}

// UsageCodec translates key usage records to their persisted kv_store form:
// decimal key-index keys, JSON-encoded values.
type UsageCodec struct{}

var _ db.KVCodec[uint64, *Usage] = UsageCodec{}

// EncodeKey encodes an internal key index as its decimal string form.
func (UsageCodec) EncodeKey(keyIndex uint64) string {
	return strconv.FormatUint(keyIndex, 10)
}

// DecodeKey parses a decimal key index.
func (UsageCodec) DecodeKey(key string) (uint64, error) {
	keyIndex, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid builder key usage key %q: %w", key, err)
	}

	return keyIndex, nil
}

// EncodeValue JSON-encodes a usage record.
func (UsageCodec) EncodeValue(usage *Usage) ([]byte, error) {
	if usage == nil {
		return nil, fmt.Errorf("cannot encode nil builder key usage")
	}

	return json.Marshal(usage)
}

// DecodeValue JSON-decodes a usage record.
func (UsageCodec) DecodeValue(value []byte) (*Usage, error) {
	usage := &Usage{}
	if err := json.Unmarshal(value, usage); err != nil {
		return nil, fmt.Errorf("failed to decode builder key usage: %w", err)
	}

	return usage, nil
}
