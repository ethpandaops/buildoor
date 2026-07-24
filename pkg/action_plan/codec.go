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

// RuleCodec translates recurring slot rules to their persisted form in the
// kv_store: the rule id as key, JSON-encoded values.
type RuleCodec struct{}

var _ db.KVCodec[string, *SlotRule] = RuleCodec{}

// EncodeKey uses the rule id verbatim.
func (RuleCodec) EncodeKey(id string) string {
	return id
}

// DecodeKey uses the stored key verbatim.
func (RuleCodec) DecodeKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty slot rule key")
	}

	return key, nil
}

// EncodeValue JSON-encodes a slot rule.
func (RuleCodec) EncodeValue(rule *SlotRule) ([]byte, error) {
	if rule == nil {
		return nil, fmt.Errorf("cannot encode nil slot rule")
	}

	return json.Marshal(rule)
}

// DecodeValue JSON-decodes a slot rule.
func (RuleCodec) DecodeValue(value []byte) (*SlotRule, error) {
	rule := &SlotRule{}
	if err := json.Unmarshal(value, rule); err != nil {
		return nil, fmt.Errorf("failed to decode slot rule: %w", err)
	}

	return rule, nil
}
