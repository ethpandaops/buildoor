package legacy

import (
	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// UnblindSignedBlindedBeaconBlock builds full SignedBlockContents from a
// fork-agnostic blinded block and the matching Payload (full payload +
// blobs). The proposer signature is preserved and the returned contents
// carry the blinded block's fork version.
func UnblindSignedBlindedBeaconBlock(
	blinded *apiv1all.SignedBlindedBeaconBlock,
	event *payload_builder.Payload,
) (*apiv1all.SignedBlockContents, error) {
	if blinded == nil || blinded.Message == nil || blinded.Message.Body == nil || event == nil || event.ExecutionPayload == nil {
		return nil, nil
	}

	// Build the full beacon block body from the blinded body, replacing the
	// execution payload header with the full payload from the payload cache.
	blindedBody := blinded.Message.Body
	fullBody := &eth2all.BeaconBlockBody{
		Version:               blinded.Version,
		RANDAOReveal:          blindedBody.RANDAOReveal,
		ETH1Data:              blindedBody.ETH1Data,
		Graffiti:              blindedBody.Graffiti,
		ProposerSlashings:     blindedBody.ProposerSlashings,
		AttesterSlashings:     blindedBody.AttesterSlashings,
		Attestations:          blindedBody.Attestations,
		Deposits:              blindedBody.Deposits,
		VoluntaryExits:        blindedBody.VoluntaryExits,
		SyncAggregate:         blindedBody.SyncAggregate,
		ExecutionPayload:      event.ExecutionPayload,
		BLSToExecutionChanges: blindedBody.BLSToExecutionChanges,
		BlobKZGCommitments:    blindedBody.BlobKZGCommitments,
		ExecutionRequests:     blindedBody.ExecutionRequests,
	}

	// Copy BlobKZGCommitments from event if we have blobs (must match blinded commitments).
	if event.BlobsBundle != nil && len(event.BlobsBundle.Commitments) > 0 {
		fullBody.BlobKZGCommitments = event.BlobsBundle.Commitments
	}

	fullBlock := &eth2all.BeaconBlock{
		Version:       blinded.Version,
		Slot:          blinded.Message.Slot,
		ProposerIndex: blinded.Message.ProposerIndex,
		ParentRoot:    blinded.Message.ParentRoot,
		StateRoot:     blinded.Message.StateRoot,
		Body:          fullBody,
	}

	contents := &apiv1all.SignedBlockContents{
		Version: blinded.Version,
		SignedBlock: &eth2all.SignedBeaconBlock{
			Version:   blinded.Version,
			Message:   fullBlock,
			Signature: blinded.Signature,
		},
	}
	if event.BlobsBundle != nil {
		contents.KZGProofs = event.BlobsBundle.Proofs
		contents.Blobs = event.BlobsBundle.Blobs
	}

	return contents, nil
}
