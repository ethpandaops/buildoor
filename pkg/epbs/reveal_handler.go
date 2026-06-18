package epbs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// RevealHandler reveals built payloads via the shared payload_bidder and gossips
// the envelope over p2p.
type RevealHandler struct {
	signer       *payload_bidder.Signer
	clClient     *beacon.Client
	chainSvc     chain.Service
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewRevealHandler creates a new reveal handler.
func NewRevealHandler(
	signer *payload_bidder.Signer,
	clClient *beacon.Client,
	chainSvc chain.Service,
	builderIndex uint64,
	log logrus.FieldLogger,
) *RevealHandler {
	return &RevealHandler{
		signer:       signer,
		clClient:     clClient,
		chainSvc:     chainSvc,
		builderIndex: builderIndex,
		log:          log.WithField("component", "reveal-handler"),
	}
}

// SubmitReveal builds the signed envelope via payload_bidder and publishes it
// (with blobs / KZG proofs) to the beacon node over p2p.
func (h *RevealHandler) SubmitReveal(
	ctx context.Context,
	payload *payload_builder.Payload,
	blockInfo *beacon.BlockInfo,
) error {
	forkVersion, err := h.chainSvc.GetForkVersion()
	if err != nil {
		return fmt.Errorf("failed to get current fork version: %w", err)
	}

	signedEnvelope, blobs, cellProofs, err := payload_bidder.BuildSignedEnvelope(payload, payload_bidder.RevealContext{
		BuilderIndex:          h.builderIndex,
		BeaconBlockRoot:       blockInfo.Root,
		ParentBeaconBlockRoot: blockInfo.ParentRoot,
	}, h.signer, forkVersion, h.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		return fmt.Errorf("failed to build signed envelope: %w", err)
	}

	signedEnvelopeJSON, err := json.Marshal(signedEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal signed envelope: %w", err)
	}

	if len(blobs) > 0 {
		h.log.WithFields(logrus.Fields{
			"blob_count":      len(blobs),
			"kzg_proof_count": len(cellProofs),
		}).Info("Including blobs and kzg proofs with envelope publish")
	}

	if err := h.clClient.SubmitExecutionPayloadEnvelope(ctx, signedEnvelopeJSON, blobs, cellProofs); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	payload.MarkRevealed(payload_builder.RevealRecord{
		Transport:       payload_builder.BidTransportP2P,
		BeaconBlockRoot: blockInfo.Root,
		At:              time.Now(),
	})

	h.log.WithFields(logrus.Fields{
		"slot":       payload.Attributes.ProposalSlot,
		"block_hash": fmt.Sprintf("%x", payload.BlockHash[:8]),
	}).Info("Payload revealed")

	return nil
}

// SetBuilderIndex updates the builder index.
func (h *RevealHandler) SetBuilderIndex(index uint64) {
	h.builderIndex = index
}
