package payload_bidder

import (
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// RevealContext are the inputs a transport supplies for a payload reveal that
// aren't carried by the payload: the builder index and the beacon block roots
// the envelope is bound to.
type RevealContext struct {
	BuilderIndex          uint64
	BeaconBlockRoot       phase0.Root
	ParentBeaconBlockRoot phase0.Root
}

// BuildSignedEnvelope constructs and signs a fork-agnostic
// SignedExecutionPayloadEnvelope for the given payload, and returns the blobs
// and KZG proofs to publish alongside it. The fork-agnostic envelope embeds the
// canonical payload directly (no per-fork conversion needed).
func BuildSignedEnvelope(
	p *payload_builder.Payload,
	rc RevealContext,
	s *Signer,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (signed *eth2all.SignedExecutionPayloadEnvelope, blobs, proofs [][]byte, err error) {
	execRequests := p.ExecutionRequests
	if execRequests == nil {
		execRequests = &electra.ExecutionRequests{
			Deposits:       []*electra.DepositRequest{},
			Withdrawals:    []*electra.WithdrawalRequest{},
			Consolidations: []*electra.ConsolidationRequest{},
		}
	}

	envelope := &eth2all.ExecutionPayloadEnvelope{
		Version:               p.ExecutionPayload.Version,
		Payload:               p.ExecutionPayload,
		ExecutionRequests:     execRequests,
		BuilderIndex:          gloas.BuilderIndex(rc.BuilderIndex),
		BeaconBlockRoot:       rc.BeaconBlockRoot,
		ParentBeaconBlockRoot: rc.ParentBeaconBlockRoot,
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
