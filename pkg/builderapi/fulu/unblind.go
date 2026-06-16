// Package fulu provides unblinding of Fulu SignedBlindedBeaconBlock for publishing.
package fulu

import (
	apiv1electra "github.com/ethpandaops/go-eth2-client/api/v1/electra"
	apiv1fulu "github.com/ethpandaops/go-eth2-client/api/v1/fulu"
	"github.com/ethpandaops/go-eth2-client/spec/electra"

	"github.com/ethpandaops/buildoor/pkg/builder"
)

// UnblindSignedBlindedBeaconBlock builds full Fulu SignedBlockContents from a blinded block and
// the matching PayloadReadyEvent (full payload + blobs). The proposer signature is preserved.
func UnblindSignedBlindedBeaconBlock(
	blinded *apiv1electra.SignedBlindedBeaconBlock,
	event *builder.PayloadReadyEvent,
) (*apiv1fulu.SignedBlockContents, error) {
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

	signedBlock := &electra.SignedBeaconBlock{
		Message:   fullBlock,
		Signature: blinded.Signature,
	}

	contents := &apiv1fulu.SignedBlockContents{
		SignedBlock: signedBlock,
	}
	if event.BlobsBundle != nil {
		contents.KZGProofs = event.BlobsBundle.Proofs
		contents.Blobs = event.BlobsBundle.Blobs
	}

	return contents, nil
}
