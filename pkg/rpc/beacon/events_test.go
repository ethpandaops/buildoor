package beacon

import (
	"encoding/json"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// payloadAttributesJSON builds a raw payload_attributes SSE event. The
// pre-Deneb variants omit parent_beacon_block_root (Deneb/EIP-4788) and, for
// bellatrix, withdrawals (Capella).
func payloadAttributesJSON(t *testing.T, fork string, withWithdrawals, withParentBeaconRoot bool) *payloadAttributesEventJSON {
	t.Helper()

	attrs := `"timestamp":"1700000000","prev_randao":"0x` + zeroHex32 + `","suggested_fee_recipient":"0x0000000000000000000000000000000000000001"`
	if withWithdrawals {
		attrs += `,"withdrawals":[{"index":"1","validator_index":"2","address":"0x0000000000000000000000000000000000000002","amount":"3"}]`
	}
	if withParentBeaconRoot {
		attrs += `,"parent_beacon_block_root":"0x` + zeroHex32 + `"`
	}

	raw := `{"version":"` + fork + `","data":{"proposal_slot":"7","proposer_index":"11",` +
		`"parent_block_root":"0x` + zeroHex32 + `","parent_block_number":"6","parent_block_hash":"0x` + zeroHex32 + `",` +
		`"payload_attributes":{` + attrs + `}}}`

	var event payloadAttributesEventJSON
	require.NoError(t, json.Unmarshal([]byte(raw), &event))

	return &event
}

const zeroHex32 = "0000000000000000000000000000000000000000000000000000000000000000"

// TestParsePayloadAttributesEvent_PreDeneb accepts bellatrix/capella events
// without a parent_beacon_block_root (the field exists from Deneb/EIP-4788
// onwards) and leaves the root zero.
func TestParsePayloadAttributesEvent_PreDeneb(t *testing.T) {
	tests := []struct {
		fork            string
		withWithdrawals bool
	}{
		{fork: "bellatrix", withWithdrawals: false},
		{fork: "capella", withWithdrawals: true},
	}

	for _, test := range tests {
		t.Run(test.fork, func(t *testing.T) {
			raw := payloadAttributesJSON(t, test.fork, test.withWithdrawals, false)

			event, err := parsePayloadAttributesEvent(raw)
			require.NoError(t, err, "pre-Deneb payload_attributes must parse without parent_beacon_block_root")

			assert.Equal(t, test.fork, event.Version)
			assert.Equal(t, phase0.Slot(7), event.ProposalSlot)
			assert.Equal(t, phase0.Root{}, event.ParentBeaconBlockRoot, "missing field stays zero")

			if test.withWithdrawals {
				require.Len(t, event.Withdrawals, 1)
				assert.Equal(t, phase0.Gwei(3), event.Withdrawals[0].Amount)
			} else {
				assert.Empty(t, event.Withdrawals)
			}
		})
	}
}

// TestParsePayloadAttributesEvent_Deneb still parses and validates
// parent_beacon_block_root when present.
func TestParsePayloadAttributesEvent_Deneb(t *testing.T) {
	raw := payloadAttributesJSON(t, "deneb", true, true)

	event, err := parsePayloadAttributesEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, phase0.Root{}, event.ParentBeaconBlockRoot)

	// A present-but-invalid root is still an error.
	raw.Data.PayloadAttributes.ParentBeaconBlockRoot = "0x1234"
	_, err = parsePayloadAttributesEvent(raw)
	require.ErrorContains(t, err, "invalid parent_beacon_block_root")
}
