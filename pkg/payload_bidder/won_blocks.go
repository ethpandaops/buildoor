package payload_bidder

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// WonBlockSource identifies which subsystem delivered a won block.
const (
	WonBlockSourceBuilderAPI = "builder_api"
	WonBlockSourceEPBS       = "epbs"
)

// WonBlocksNamespace is the kv_store namespace that held the persisted
// won-block records. Retained (with WonBlock/WonBlockCodec) so the slot
// results store can migrate the namespace; kept for at least one release.
const WonBlocksNamespace = "won_blocks"

// WonBlock is a block of ours that was included in a beacon block, won via
// either the Builder API or p2p ePBS bidding. The JSON tags are the wire shape
// consumed by the WebUI (bids-won REST endpoint and bid_won SSE event) — do
// not change them.
type WonBlock struct {
	Source          string `json:"source"`
	Slot            uint64 `json:"slot"`
	BlockHash       string `json:"block_hash"`
	NumTransactions int    `json:"num_transactions"`
	NumBlobs        int    `json:"num_blobs"`
	ValueWei        string `json:"value_wei"`
	ValueETH        string `json:"value_eth"`
	Timestamp       int64  `json:"timestamp"` // Unix milliseconds at inclusion time
}

// WonBlockCodec translates the won-block store's entries to their persisted
// form: decimal slot string keys, JSON-encoded values. JSON is deliberate:
// WonBlock is a local aggregate (not a spec SSZ type), and the kv_store value
// is an opaque blob either way.
type WonBlockCodec struct{}

var _ db.KVCodec[phase0.Slot, *WonBlock] = WonBlockCodec{}

// EncodeKey encodes a slot as its decimal string form.
func (WonBlockCodec) EncodeKey(slot phase0.Slot) string {
	return strconv.FormatUint(uint64(slot), 10)
}

// DecodeKey parses a decimal slot string.
func (WonBlockCodec) DecodeKey(key string) (phase0.Slot, error) {
	slot, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid won block slot key %q: %w", key, err)
	}

	return phase0.Slot(slot), nil
}

// EncodeValue JSON-encodes a won block.
func (WonBlockCodec) EncodeValue(wonBlock *WonBlock) ([]byte, error) {
	if wonBlock == nil {
		return nil, fmt.Errorf("cannot encode nil won block")
	}

	return json.Marshal(wonBlock)
}

// DecodeValue JSON-decodes a won block.
func (WonBlockCodec) DecodeValue(value []byte) (*WonBlock, error) {
	wonBlock := &WonBlock{}
	if err := json.Unmarshal(value, wonBlock); err != nil {
		return nil, fmt.Errorf("failed to decode won block: %w", err)
	}

	return wonBlock, nil
}
