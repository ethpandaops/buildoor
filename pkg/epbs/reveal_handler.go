package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// RevealHandler handles submission of execution payload reveals.
type RevealHandler struct {
	signer       *Signer
	clClient     *beacon.Client
	genesis      *beacon.Genesis
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewRevealHandler creates a new reveal handler.
func NewRevealHandler(
	signer *Signer,
	clClient *beacon.Client,
	genesis *beacon.Genesis,
	builderIndex uint64,
	log logrus.FieldLogger,
) *RevealHandler {
	return &RevealHandler{
		signer:       signer,
		clClient:     clClient,
		genesis:      genesis,
		builderIndex: builderIndex,
		log:          log.WithField("component", "reveal-handler"),
	}
}

// SubmitReveal submits a payload reveal for the given slot.
func (h *RevealHandler) SubmitReveal(
	ctx context.Context,
	payload *BuiltPayload,
	blockRoot phase0.Root,
) error {
	// Parse the execution payload from JSON
	var execPayload deneb.ExecutionPayload
	if err := json.Unmarshal(payload.ExecutionPayload, &execPayload); err != nil {
		return fmt.Errorf("failed to parse execution payload: %w", err)
	}

	// Build the execution payload envelope
	envelope := &gloas.ExecutionPayloadEnvelope{
		Payload:            &execPayload,
		ExecutionRequests:  nil, // No execution requests for now
		BuilderIndex:       gloas.BuilderIndex(h.builderIndex),
		BeaconBlockRoot:    blockRoot,
		Slot:               payload.Slot,
		BlobKZGCommitments: nil,           // No blobs for now
		StateRoot:          phase0.Root{}, // Will be filled by beacon node
	}

	// Sign the envelope
	signature, err := h.signer.SignExecutionPayloadEnvelope(
		envelope,
		h.genesis.GenesisValidatorsRoot,
	)
	if err != nil {
		return fmt.Errorf("failed to sign envelope: %w", err)
	}

	// Create signed envelope
	signedEnvelope := &gloas.SignedExecutionPayloadEnvelope{
		Message:   envelope,
		Signature: signature,
	}

	// Marshal to JSON for submission
	signedEnvelopeJSON, err := json.Marshal(signedEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal signed envelope: %w", err)
	}

	// Submit envelope
	if err := h.clClient.SubmitExecutionPayloadEnvelope(ctx, signedEnvelopeJSON); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	h.log.WithFields(logrus.Fields{
		"slot":       payload.Slot,
		"block_hash": fmt.Sprintf("%x", payload.BlockHash[:8]),
	}).Info("Payload revealed")

	return nil
}

// SetBuilderIndex updates the builder index.
func (h *RevealHandler) SetBuilderIndex(index uint64) {
	h.builderIndex = index
}
