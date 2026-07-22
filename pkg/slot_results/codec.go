package slot_results

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// Namespace is the kv_store namespace holding the persisted slot results.
const Namespace = "slot_results"

// ResultCodec translates slot results to their persisted form in the
// kv_store: decimal slot string keys, JSON values.
type ResultCodec struct{}

var _ db.KVCodec[phase0.Slot, *SlotResult] = ResultCodec{}

// EncodeKey encodes a slot as its decimal string form.
func (ResultCodec) EncodeKey(slot phase0.Slot) string {
	return strconv.FormatUint(uint64(slot), 10)
}

// DecodeKey parses a decimal slot string.
func (ResultCodec) DecodeKey(key string) (phase0.Slot, error) {
	slot, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid slot result key %q: %w", key, err)
	}

	return phase0.Slot(slot), nil
}

// EncodeValue JSON-encodes a slot result.
func (ResultCodec) EncodeValue(result *SlotResult) ([]byte, error) {
	if result == nil {
		return nil, fmt.Errorf("cannot encode nil slot result")
	}

	return json.Marshal(result)
}

// DecodeValue JSON-decodes a slot result.
func (ResultCodec) DecodeValue(value []byte) (*SlotResult, error) {
	result := &SlotResult{}
	if err := json.Unmarshal(value, result); err != nil {
		return nil, fmt.Errorf("failed to decode slot result: %w", err)
	}

	return result, nil
}
