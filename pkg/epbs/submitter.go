package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BidSubmitter submits a signed ExecutionPayloadBid through some transport.
// The default implementation is HTTPSubmitter (POST to the beacon node).
// Spamoor mode swaps in a libp2p gossipsub-based implementation.
type BidSubmitter interface {
	Submit(ctx context.Context, signedBid *gloas.SignedExecutionPayloadBid) error
}

// HTTPSubmitter posts the bid to the beacon node's /eth/v1/beacon/execution_payload_bid endpoint.
type HTTPSubmitter struct {
	clClient *beacon.Client
}

// NewHTTPSubmitter creates a BidSubmitter that POSTs JSON bids to a beacon node.
func NewHTTPSubmitter(clClient *beacon.Client) *HTTPSubmitter {
	return &HTTPSubmitter{clClient: clClient}
}

// Submit JSON-encodes the signed bid and POSTs it to the beacon node.
func (s *HTTPSubmitter) Submit(ctx context.Context, signedBid *gloas.SignedExecutionPayloadBid) error {
	signedBidJSON, err := json.Marshal(signedBid)
	if err != nil {
		return fmt.Errorf("failed to marshal signed bid: %w", err)
	}

	return s.clClient.SubmitExecutionPayloadBid(ctx, signedBidJSON)
}
