package beacon

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
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

// TestInjectPayloadAttributes caches and dispatches synthesized attributes,
// but never overwrites a slot that already has node-received attributes.
func TestInjectPayloadAttributes(t *testing.T) {
	stream := NewEventStream(&Client{})
	sub := stream.SubscribePayloadAttributes()
	defer sub.Unsubscribe()

	synthesized := &PayloadAttributesEvent{ProposalSlot: 10, ParentBlockNumber: 9}
	require.True(t, stream.InjectPayloadAttributes(synthesized))
	assert.Equal(t, synthesized, stream.GetLatestPayloadAttributes(10))

	select {
	case got := <-sub.Channel():
		assert.Equal(t, synthesized, got)
	default:
		t.Fatal("expected the injected event to be dispatched")
	}

	// A second injection for the same slot and parent tuple is dropped (the
	// cached event wins).
	require.False(t, stream.InjectPayloadAttributes(&PayloadAttributesEvent{ProposalSlot: 10}))
	assert.Equal(t, synthesized, stream.GetLatestPayloadAttributes(10))
}

func TestPayloadAttributeVariants(t *testing.T) {
	stream := NewEventStream(&Client{})

	// Two events for the same slot with different parents: both variants are
	// retained and the newest becomes the slot's latest.
	fullParent := &PayloadAttributesEvent{
		ProposalSlot:    20,
		ParentBlockRoot: phase0.Root{0x01},
		ParentBlockHash: phase0.Hash32{0xaa},
	}
	emptyParent := &PayloadAttributesEvent{
		ProposalSlot:    20,
		ParentBlockRoot: phase0.Root{0x01},
		ParentBlockHash: phase0.Hash32{0xbb},
	}

	stream.cachePayloadAttributes(fullParent)
	stream.cachePayloadAttributes(emptyParent)

	assert.Equal(t, emptyParent, stream.GetLatestPayloadAttributes(20), "newest event wins as latest")
	assert.Len(t, stream.GetPayloadAttributesVariants(20), 2)
	assert.Equal(t, fullParent, stream.GetPayloadAttributesVariant(20, AttrParentKeyOf(fullParent)))
	assert.Equal(t, emptyParent, stream.GetPayloadAttributesVariant(20, AttrParentKeyOf(emptyParent)))

	// A newer event for an existing tuple replaces that variant (and the
	// latest pointer).
	fullParentUpdated := &PayloadAttributesEvent{
		ProposalSlot:    20,
		ParentBlockRoot: phase0.Root{0x01},
		ParentBlockHash: phase0.Hash32{0xaa},
		Timestamp:       99,
	}
	stream.cachePayloadAttributes(fullParentUpdated)

	assert.Len(t, stream.GetPayloadAttributesVariants(20), 2)
	assert.Equal(t, fullParentUpdated, stream.GetPayloadAttributesVariant(20, AttrParentKeyOf(fullParent)))
	assert.Equal(t, fullParentUpdated, stream.GetLatestPayloadAttributes(20))

	// An injected variant for a NEW tuple is added but does not steal the
	// latest pointer from a node-received event.
	derived := &PayloadAttributesEvent{
		ProposalSlot:    20,
		ParentBlockRoot: phase0.Root{0x02},
		ParentBlockHash: phase0.Hash32{0xcc},
	}
	require.True(t, stream.InjectPayloadAttributes(derived))
	assert.Len(t, stream.GetPayloadAttributesVariants(20), 3)
	assert.Equal(t, fullParentUpdated, stream.GetLatestPayloadAttributes(20),
		"injected variant must not become latest when node events exist")

	// Cleanup drops the whole slot entry.
	stream.CleanupPayloadAttributesCache(21)
	assert.Nil(t, stream.GetLatestPayloadAttributes(20))
	assert.Empty(t, stream.GetPayloadAttributesVariants(20))
}

func TestPayloadAttributesBuildInputsEqual(t *testing.T) {
	base := func() *PayloadAttributesEvent {
		return &PayloadAttributesEvent{
			ProposalSlot:          30,
			ProposerIndex:         7,
			ParentBlockRoot:       phase0.Root{0x01},
			ParentBlockNumber:     29,
			ParentBlockHash:       phase0.Hash32{0xaa},
			Timestamp:             1000,
			PrevRandao:            phase0.Root{0xcc},
			SuggestedFeeRecipient: common.Address{0x0f},
			ParentBeaconBlockRoot: phase0.Root{0x01},
			TargetGasLimit:        60_000_000,
			Withdrawals: []*capella.Withdrawal{
				{Index: 1, ValidatorIndex: 10, Amount: 500},
				{Index: 2, ValidatorIndex: 11, Amount: 600},
			},
			InclusionListTransactions: [][]byte{{0x01, 0x02}},
		}
	}

	tests := []struct {
		name   string
		mutate func(e *PayloadAttributesEvent)
		equal  bool
	}{
		{name: "identical", mutate: func(*PayloadAttributesEvent) {}, equal: true},
		{
			name:   "slot and parent number ignored",
			mutate: func(e *PayloadAttributesEvent) { e.ProposalSlot = 31; e.ParentBlockNumber = 0; e.Synthesized = true },
			equal:  true,
		},
		{
			name:   "withdrawal amount differs",
			mutate: func(e *PayloadAttributesEvent) { e.Withdrawals[1].Amount = 601 },
			equal:  false,
		},
		{
			name:   "withdrawal count differs",
			mutate: func(e *PayloadAttributesEvent) { e.Withdrawals = e.Withdrawals[:1] },
			equal:  false,
		},
		{name: "prev randao differs", mutate: func(e *PayloadAttributesEvent) { e.PrevRandao = phase0.Root{0xdd} }, equal: false},
		{name: "timestamp differs", mutate: func(e *PayloadAttributesEvent) { e.Timestamp++ }, equal: false},
		{name: "proposer differs", mutate: func(e *PayloadAttributesEvent) { e.ProposerIndex = 8 }, equal: false},
		{name: "target gas limit differs", mutate: func(e *PayloadAttributesEvent) { e.TargetGasLimit = 0 }, equal: false},
		{name: "fee recipient differs", mutate: func(e *PayloadAttributesEvent) { e.SuggestedFeeRecipient = common.Address{} }, equal: false},
		{
			name:   "inclusion list differs",
			mutate: func(e *PayloadAttributesEvent) { e.InclusionListTransactions = [][]byte{{0x01, 0x03}} },
			equal:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := base(), base()
			tt.mutate(b)
			assert.Equal(t, tt.equal, a.BuildInputsEqual(b))
			assert.Equal(t, tt.equal, b.BuildInputsEqual(a), "symmetric")
		})
	}
}

// TestInjectPayloadAttributes_NodeEventReplacesSynthesized: a node-received
// event for the same parent tuple replaces the synthesized variant, and the
// marker tells them apart.
func TestInjectPayloadAttributes_NodeEventReplacesSynthesized(t *testing.T) {
	stream := NewEventStream(&Client{})

	synthesized := &PayloadAttributesEvent{ProposalSlot: 12, ParentBlockHash: phase0.Hash32{0xaa}, Synthesized: true}
	require.True(t, stream.InjectPayloadAttributes(synthesized))
	assert.True(t, stream.GetLatestPayloadAttributes(12).Synthesized)

	real := &PayloadAttributesEvent{ProposalSlot: 12, ParentBlockHash: phase0.Hash32{0xaa}, Timestamp: 5}
	stream.cachePayloadAttributes(real)

	assert.Same(t, real, stream.GetLatestPayloadAttributes(12), "node event wins")
	assert.Len(t, stream.GetPayloadAttributesVariants(12), 1)
	assert.False(t, stream.GetLatestPayloadAttributes(12).Synthesized)
}
