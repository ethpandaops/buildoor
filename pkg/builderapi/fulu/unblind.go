// Package fulu provides unblinding of Fulu SignedBlindedBeaconBlock for publishing.
package fulu

import (
	apiv1electra "github.com/attestantio/go-eth2-client/api/v1/electra"
	apiv1fulu "github.com/attestantio/go-eth2-client/api/v1/fulu"
	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/electra"

	"github.com/ethpandaops/buildoor/pkg/builder"
)

// UnblindSignedBlindedBeaconBlock builds full Fulu SignedBlockContents from a blinded block and
// the matching PayloadReadyEvent (full payload + blobs). The proposer signature is preserved.
func UnblindSignedBlindedBeaconBlock(
	blinded *apiv1electra.SignedBlindedBeaconBlock,
	event *builder.PayloadReadyEvent,
) (*apiv1fulu.SignedBlockContents, error) {
	if blinded == nil || blinded.Message == nil || blinded.Message.Body == nil || event == nil || event.Payload == nil {
		return nil, nil
	}

	fullPayload, err := ExecutionPayloadFromEngine(event.Payload)
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
		fullBody.BlobKZGCommitments = make([]deneb.KZGCommitment, len(event.BlobsBundle.Commitments))
		for i, c := range event.BlobsBundle.Commitments {
			copy(fullBody.BlobKZGCommitments[i][:], c[:])
		}
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

	// Build KZGProofs and Blobs from event.BlobsBundle (deneb.KZGProof is 48 bytes; engine may use 32-byte hashes).
	var kzgProofs []deneb.KZGProof
	var blobs []deneb.Blob
	if event.BlobsBundle != nil {
		kzgProofs = make([]deneb.KZGProof, len(event.BlobsBundle.Proofs))
		for i, p := range event.BlobsBundle.Proofs {
			copy(kzgProofs[i][:], p[:])
			if len(p) < 48 {
				// Pad to 48 bytes if engine stored 32-byte hash
				for j := len(p); j < 48 && j < len(kzgProofs[i]); j++ {
					kzgProofs[i][j] = 0
				}
			}
		}
		blobs = make([]deneb.Blob, len(event.BlobsBundle.Blobs))
		for i, b := range event.BlobsBundle.Blobs {
			copy(blobs[i][:], b)
		}
	}

	return &apiv1fulu.SignedBlockContents{
		SignedBlock: signedBlock,
		KZGProofs:   kzgProofs,
		Blobs:       blobs,
	}, nil
}
