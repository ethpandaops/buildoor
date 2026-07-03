package beacon

import (
	"context"
	"fmt"

	eth2client "github.com/ethpandaops/go-eth2-client"
	"github.com/ethpandaops/go-eth2-client/api"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// SubmitExecutionPayloadBid submits a signed execution payload bid to the beacon node.
// The consensus version header and body encoding (SSZ or JSON per the client's content
// negotiation) are derived from the bid's Version by go-eth2-client.
func (c *Client) SubmitExecutionPayloadBid(ctx context.Context, bid *eth2all.SignedExecutionPayloadBid) error {
	submitter, ok := c.client.(eth2client.ExecutionPayloadBidSubmitter)
	if !ok {
		return fmt.Errorf("client does not support execution payload bid submission")
	}

	if err := submitter.SubmitAgnosticExecutionPayloadBid(ctx, &api.SubmitAgnosticExecutionPayloadBidOpts{
		SignedExecutionPayloadBid: bid,
	}); err != nil {
		return fmt.Errorf("failed to submit bid: %w", err)
	}

	return nil
}

// SubmitExecutionPayloadEnvelope submits a signed execution payload envelope using the
// stateless flow (SignedExecutionPayloadEnvelopeContents body, Eth-Execution-Payload-Blinded
// false). The stateful/blinded flow only works when the beacon node cached the full envelope
// from its own block production (produceBlockV4); buildoor builds payloads externally, so the
// beacon node never has them cached and the stateless form is the only valid one.
//
// The consensus version header and body encoding (SSZ or JSON per the client's content
// negotiation) are derived from the envelope's Version by go-eth2-client.
func (c *Client) SubmitExecutionPayloadEnvelope(
	ctx context.Context,
	envelope *eth2all.SignedExecutionPayloadEnvelope,
	blobs [][]byte,
	kzgProofs [][]byte,
) error {
	submitter, ok := c.client.(eth2client.ExecutionPayloadEnvelopeSubmitter)
	if !ok {
		return fmt.Errorf("client does not support execution payload envelope submission")
	}

	typedBlobs := make([]deneb.Blob, len(blobs))

	for i, b := range blobs {
		if len(b) != len(deneb.Blob{}) {
			return fmt.Errorf("invalid blob %d: expected %d bytes, got %d", i, len(deneb.Blob{}), len(b))
		}

		copy(typedBlobs[i][:], b)
	}

	typedProofs := make([]deneb.KZGProof, len(kzgProofs))

	for i, p := range kzgProofs {
		if len(p) != len(deneb.KZGProof{}) {
			return fmt.Errorf("invalid kzg proof %d: expected %d bytes, got %d", i, len(deneb.KZGProof{}), len(p))
		}

		copy(typedProofs[i][:], p)
	}

	if err := submitter.SubmitAgnosticExecutionPayloadEnvelope(ctx, &api.SubmitAgnosticExecutionPayloadEnvelopeOpts{
		SignedExecutionPayloadEnvelope: envelope,
		KZGProofs:                      typedProofs,
		Blobs:                          typedBlobs,
	}); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	return nil
}

// PayloadEnvelopeInfo contains key fields from a fetched execution payload envelope.
type PayloadEnvelopeInfo struct {
	BlockRoot    phase0.Root
	BlockHash    phase0.Hash32
	BuilderIndex uint64
}

// GetExecutionPayloadEnvelope fetches the signed execution payload envelope for a block.
// The blockID can be a block root (hex), slot number, or "head"/"finalized"/"genesis".
func (c *Client) GetExecutionPayloadEnvelope(
	ctx context.Context,
	blockID string,
) (*PayloadEnvelopeInfo, error) {
	provider, ok := c.client.(eth2client.ExecutionPayloadProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support execution payload envelope provider")
	}

	resp, err := provider.AgnosticSignedExecutionPayloadEnvelope(ctx, &api.SignedExecutionPayloadEnvelopeOpts{
		Block: blockID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get payload envelope: %w", err)
	}

	if resp.Data == nil || resp.Data.Message == nil || resp.Data.Message.Payload == nil {
		return nil, fmt.Errorf("payload envelope response is nil")
	}

	msg := resp.Data.Message

	return &PayloadEnvelopeInfo{
		BlockRoot:    msg.BeaconBlockRoot,
		BlockHash:    msg.Payload.BlockHash,
		BuilderIndex: uint64(msg.BuilderIndex),
	}, nil
}

// SubmitVoluntaryExit submits a signed voluntary exit to the beacon node.
func (c *Client) SubmitVoluntaryExit(ctx context.Context, exit *phase0.SignedVoluntaryExit) error {
	submitter, ok := c.client.(eth2client.VoluntaryExitSubmitter)
	if !ok {
		return fmt.Errorf("client does not support voluntary exit submission")
	}

	if err := submitter.SubmitVoluntaryExit(ctx, exit); err != nil {
		return fmt.Errorf("failed to submit exit: %w", err)
	}

	c.log.Info("Submitted exit!")

	return nil
}
