package api

import "encoding/json"

// Illustrative JSON templates for the transform live-test, used only when no
// captured artifact is available yet (cold start). They show the field names
// and types (uint64/bytes rendered as strings, per the beacon JSON encoding)
// so operators can write expressions before any slot has produced artifacts.
// They are input samples only — never re-serialized into typed objects — so
// they need not be exhaustive, only representative.

const payloadTemplateJSON = `{
  "parent_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "fee_recipient": "0x0000000000000000000000000000000000000000",
  "state_root": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "receipts_root": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "logs_bloom": "0x00",
  "prev_randao": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "block_number": "0",
  "gas_limit": "30000000",
  "gas_used": "0",
  "timestamp": "0",
  "extra_data": "0x",
  "base_fee_per_gas": "0",
  "block_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "transactions": [],
  "withdrawals": [],
  "blob_gas_used": "0",
  "excess_blob_gas": "0"
}`

const bidTemplateJSON = `{
  "parent_block_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "parent_block_root": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "block_hash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "fee_recipient": "0x0000000000000000000000000000000000000000",
  "gas_limit": "30000000",
  "builder_index": "0",
  "slot": "0",
  "value": "0",
  "execution_payment": "0",
  "blob_kzg_commitments": [],
  "execution_requests_root": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "prev_randao": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "inclusion_list_bits": "0xffff"
}`

const envelopeTemplateJSON = `{
  "payload": ` + payloadTemplateJSON + `,
  "execution_requests": {"deposits": [], "withdrawals": [], "consolidations": []},
  "builder_index": "0",
  "beacon_block_root": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "parent_beacon_block_root": "0x0000000000000000000000000000000000000000000000000000000000000000"
}`

// transformTemplate returns the illustrative input JSON for the target.
func transformTemplate(target string) []byte {
	switch target {
	case transformTargetBid:
		return json.RawMessage(bidTemplateJSON)
	case transformTargetEnvelope:
		return json.RawMessage(envelopeTemplateJSON)
	default:
		return json.RawMessage(payloadTemplateJSON)
	}
}
