package payload_bidder

import (
	"context"
	"fmt"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/jqtransform"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// transformTimeout bounds how long an operator jq transform may run before it
// is cancelled, so a pathological expression cannot stall bid/envelope
// construction.
const transformTimeout = 2 * time.Second

// RevealContext are the inputs a transport supplies for a payload reveal that
// aren't carried by the payload: the builder index and the beacon block roots
// the envelope is bound to.
type RevealContext struct {
	BuilderIndex          uint64
	BeaconBlockRoot       phase0.Root
	ParentBeaconBlockRoot phase0.Root

	// Transform, when set, is an operator-supplied jq expression applied to the
	// envelope MESSAGE (JSON) before signing; the modified message is then
	// signed, so the resulting envelope is validly signed but customized.
	Transform string
}

// BuildSignedEnvelope constructs and signs a fork-agnostic
// SignedExecutionPayloadEnvelope for the given payload, and returns the blobs
// and KZG proofs to publish alongside it. The fork-agnostic envelope embeds the
// canonical payload directly (no per-fork conversion needed).
func BuildSignedEnvelope(
	ctx context.Context,
	p *payload_builder.Payload,
	rc RevealContext,
	s *Signer,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (signed *eth2all.SignedExecutionPayloadEnvelope, blobs, proofs [][]byte, err error) {
	envelope := &eth2all.ExecutionPayloadEnvelope{
		Version:               p.ExecutionPayload.Version,
		Payload:               p.ExecutionPayload,
		ExecutionRequests:     p.ExecutionRequests,
		BuilderIndex:          gloas.BuilderIndex(rc.BuilderIndex),
		BeaconBlockRoot:       rc.BeaconBlockRoot,
		ParentBeaconBlockRoot: rc.ParentBeaconBlockRoot,
	}

	// Apply the operator's jq transform to the envelope message before signing.
	if rc.Transform != "" {
		// The JSON view requires execution_requests; default a nil field to an
		// empty set so the round-trip succeeds (SSZ-equivalent to nil).
		if envelope.ExecutionRequests == nil {
			envelope.ExecutionRequests = &eth2all.ExecutionRequests{Version: envelope.Version}
		}

		transformed := &eth2all.ExecutionPayloadEnvelope{Version: envelope.Version}
		if err := jqtransform.ApplyTyped(ctx, rc.Transform, envelope, transformed); err != nil {
			return nil, nil, nil, fmt.Errorf("envelope transform failed: %w", err)
		}

		envelope = transformed
	}

	sig, err := s.SignEnvelope(envelope, forkVersion, genesisValidatorsRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to sign envelope: %w", err)
	}

	signed = &eth2all.SignedExecutionPayloadEnvelope{
		Version:   envelope.Version,
		Message:   envelope,
		Signature: sig,
	}

	if p.BlobsBundle != nil && len(p.BlobsBundle.Blobs) > 0 {
		blobs = p.BlobsBundle.BlobsAsBytes()
		proofs = p.BlobsBundle.ProofsAsBytes()
	}

	return signed, blobs, proofs, nil
}
