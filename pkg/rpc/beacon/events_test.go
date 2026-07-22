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

// TestParseSingleAttestationEvent parses the beacon-APIs single_attestation
// event payload, ignoring the unused signature/source/target fields.
func TestParseSingleAttestationEvent(t *testing.T) {
	rawJSON := `{"committee_index":"3","attester_index":"12345",` +
		`"data":{"slot":"42","index":"1","beacon_block_root":"0x` + zeroHex32 + `",` +
		`"source":{"epoch":"0","root":"0x` + zeroHex32 + `"},` +
		`"target":{"epoch":"1","root":"0x` + zeroHex32 + `"}},` +
		`"signature":"0xabcdef"}`

	var raw singleAttestationEventJSON
	require.NoError(t, json.Unmarshal([]byte(rawJSON), &raw))

	event, err := parseSingleAttestationEvent(&raw)
	require.NoError(t, err)
	assert.Equal(t, phase0.Slot(42), event.Slot)
	assert.Equal(t, phase0.CommitteeIndex(3), event.CommitteeIndex)
	assert.Equal(t, phase0.ValidatorIndex(12345), event.AttesterIndex)
	assert.Equal(t, phase0.Root{}, event.BeaconBlockRoot)
}

// TestParseSingleAttestationEvent_Invalid rejects malformed fields.
func TestParseSingleAttestationEvent_Invalid(t *testing.T) {
	valid := func() *singleAttestationEventJSON {
		raw := &singleAttestationEventJSON{
			CommitteeIndex: "3",
			AttesterIndex:  "12345",
		}
		raw.Data.Slot = "42"
		raw.Data.BeaconBlockRoot = "0x" + zeroHex32

		return raw
	}

	tests := []struct {
		name    string
		mutate  func(*singleAttestationEventJSON)
		wantErr string
	}{
		{"bad committee_index", func(r *singleAttestationEventJSON) { r.CommitteeIndex = "x" }, "invalid committee_index"},
		{"bad attester_index", func(r *singleAttestationEventJSON) { r.AttesterIndex = "" }, "invalid attester_index"},
		{"bad slot", func(r *singleAttestationEventJSON) { r.Data.Slot = "-1" }, "invalid slot"},
		{"bad root", func(r *singleAttestationEventJSON) { r.Data.BeaconBlockRoot = "0x1234" }, "invalid beacon_block_root"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := valid()
			tc.mutate(raw)

			_, err := parseSingleAttestationEvent(raw)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
