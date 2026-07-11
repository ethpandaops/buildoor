package action_plan

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// PlanCodec translates slot plans to their persisted form in the kv_store:
// decimal slot string keys, JSON-encoded values. JSON is deliberate — the
// SlotPlan is a local aggregate, not a spec SSZ type.
type PlanCodec struct{}

var _ db.KVCodec[phase0.Slot, *SlotPlan] = PlanCodec{}

// EncodeKey encodes a slot as its decimal string form.
func (PlanCodec) EncodeKey(slot phase0.Slot) string {
	return strconv.FormatUint(uint64(slot), 10)
}

// DecodeKey parses a decimal slot string.
func (PlanCodec) DecodeKey(key string) (phase0.Slot, error) {
	slot, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid slot plan key %q: %w", key, err)
	}

	return phase0.Slot(slot), nil
}

// EncodeValue JSON-encodes a slot plan.
func (PlanCodec) EncodeValue(plan *SlotPlan) ([]byte, error) {
	if plan == nil {
		return nil, fmt.Errorf("cannot encode nil slot plan")
	}

	return json.Marshal(plan)
}

// DecodeValue JSON-decodes a slot plan.
func (PlanCodec) DecodeValue(value []byte) (*SlotPlan, error) {
	plan := &SlotPlan{}
	if err := json.Unmarshal(value, plan); err != nil {
		return nil, fmt.Errorf("failed to decode slot plan: %w", err)
	}

	return plan, nil
}
