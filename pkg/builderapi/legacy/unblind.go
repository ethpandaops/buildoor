package legacy

import (
	"fmt"

	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	apiv1electra "github.com/ethpandaops/go-eth2-client/api/v1/electra"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// UnblindSignedBlindedBeaconBlock builds full SignedBlockContents from a blinded block and
// the matching Payload (full payload + blobs). The proposer signature is preserved.
// fork selects the wire version of the returned contents; Electra and Fulu share the
// Electra block schema, so both are built from the same blinded block layout.
func UnblindSignedBlindedBeaconBlock(
	blinded *apiv1electra.SignedBlindedBeaconBlock,
	event *payload_builder.Payload,
	fork version.DataVersion,
) (*apiv1all.SignedBlockContents, error) {
	if blinded == nil || blinded.Message == nil || blinded.Message.Body == nil || event == nil || event.ExecutionPayload == nil {
		return nil, nil
	}

	fullPayload, err := DenebPayload(event.ExecutionPayload)
	if err != nil {
		return nil, err
	}

	// Build full beacon block body from blinded body, replacing header with full payload.
	blindedBody := blinded.Message.Body
	fullBody := &electra.BeaconBlockBody{
		RANDAOReveal:          blindedBody.RANDAOReveal,
		ETH1Data:              blindedBody.ETH1Data,
		Graffiti:              blindedBody.Graffiti,
		ProposerSlashings:     blindedBody.ProposerSlashings,
		AttesterSlashings:     blindedBody.AttesterSlashings,
		Attestations:          blindedBody.Attestations,
		Deposits:              blindedBody.Deposits,
		VoluntaryExits:        blindedBody.VoluntaryExits,
		SyncAggregate:         blindedBody.SyncAggregate,
		ExecutionPayload:      fullPayload,
		BLSToExecutionChanges: blindedBody.BLSToExecutionChanges,
		BlobKZGCommitments:    blindedBody.BlobKZGCommitments,
		ExecutionRequests:     blindedBody.ExecutionRequests,
	}

	// Copy BlobKZGCommitments from event if we have blobs (must match blinded commitments).
	if event.BlobsBundle != nil && len(event.BlobsBundle.Commitments) > 0 {
		fullBody.BlobKZGCommitments = event.BlobsBundle.Commitments
	}

	fullBlock := &electra.BeaconBlock{
		Slot:          blinded.Message.Slot,
		ProposerIndex: blinded.Message.ProposerIndex,
		ParentRoot:    blinded.Message.ParentRoot,
		StateRoot:     blinded.Message.StateRoot,
		Body:          fullBody,
	}

	// Wrap the Electra-schema block in the fork-agnostic signed block pinned
	// to the active fork (Electra and Fulu share the Electra schema).
	signedBlock := &eth2all.SignedBeaconBlock{Version: fork}
	if err := signedBlock.FromView(&electra.SignedBeaconBlock{
		Message:   fullBlock,
		Signature: blinded.Signature,
	}); err != nil {
		return nil, fmt.Errorf("failed to build fork-agnostic signed block: %w", err)
	}

	contents := &apiv1all.SignedBlockContents{
		Version:     fork,
		SignedBlock: signedBlock,
	}
	if event.BlobsBundle != nil {
		contents.KZGProofs = event.BlobsBundle.Proofs
		contents.Blobs = event.BlobsBundle.Blobs
	}

	return contents, nil
}
