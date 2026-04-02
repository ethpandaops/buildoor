package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/electra"
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// RevealHandler handles submission of execution payload reveals.
type RevealHandler struct {
	signer       *Signer
	clClient     *beacon.Client
	genesis      *beacon.Genesis
	chainSpec    *beacon.ChainSpec
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewRevealHandler creates a new reveal handler.
func NewRevealHandler(
	signer *Signer,
	clClient *beacon.Client,
	genesis *beacon.Genesis,
	chainSpec *beacon.ChainSpec,
	builderIndex uint64,
	log logrus.FieldLogger,
) *RevealHandler {
	return &RevealHandler{
		signer:       signer,
		clClient:     clClient,
		genesis:      genesis,
		chainSpec:    chainSpec,
		builderIndex: builderIndex,
		log:          log.WithField("component", "reveal-handler"),
	}
}

// SubmitReveal submits a payload reveal for the given slot.
// It uses the two-step flow:
//  1. Construct: POST /eth/v1/builder/execution_payload_envelope — beacon node derives state_root
//  2. Sign the returned envelope
//  3. Publish: POST /eth/v1/beacon/execution_payload_envelope — broadcast to the network
func (h *RevealHandler) SubmitReveal(
	ctx context.Context,
	payload *BuiltPayload,
	blockRoot phase0.Root,
) error {
	// Marshal execution payload to JSON for the construct request.
	payloadJSON, err := json.Marshal(payload.ExecutionPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal execution payload: %w", err)
	}

	// Parse raw execution requests into typed electra.ExecutionRequests for the construct request.
	var execRequests *electra.ExecutionRequests
	if len(payload.ExecutionRequests) > 0 {
		execRequests, err = fulu.ParseExecutionRequests(payload.ExecutionRequests)
		if err != nil {
			return fmt.Errorf("failed to parse execution requests: %w", err)
		}
	} else {
		execRequests = &electra.ExecutionRequests{}
	}

	execRequestsJSON, err := json.Marshal(execRequests)
	if err != nil {
		return fmt.Errorf("failed to marshal execution requests: %w", err)
	}

	beaconBlockRootHex := fmt.Sprintf("%#x", blockRoot)

	h.log.WithFields(logrus.Fields{
		"slot":              payload.Slot,
		"block_hash":        fmt.Sprintf("%x", payload.BlockHash[:8]),
		"beacon_block_root": beaconBlockRootHex,
	}).Info("Constructing execution payload envelope via beacon node")

	// Step 1: Construct — beacon node fills in state_root, builder_index, slot.
	envelopeJSON, err := h.clClient.ConstructExecutionPayloadEnvelope(
		ctx,
		beaconBlockRootHex,
		payloadJSON,
		execRequestsJSON,
	)
	if err != nil {
		return fmt.Errorf("failed to construct envelope: %w", err)
	}

	// Unmarshal the returned envelope so we can sign it.
	var envelope gloas.ExecutionPayloadEnvelope
	if err := json.Unmarshal(envelopeJSON, &envelope); err != nil {
		return fmt.Errorf("failed to unmarshal constructed envelope: %w", err)
	}

	h.log.WithFields(logrus.Fields{
		"slot":          envelope.Slot,
		"builder_index": envelope.BuilderIndex,
		"state_root":    fmt.Sprintf("%#x", envelope.StateRoot),
	}).Info("Envelope constructed by beacon node")

	// Step 2: Sign the envelope.
	var forkVersion phase0.Version
	if h.chainSpec.GloasForkVersion != nil {
		forkVersion = *h.chainSpec.GloasForkVersion
	}

	signature, err := h.signer.SignExecutionPayloadEnvelope(
		&envelope,
		forkVersion,
		h.genesis.GenesisValidatorsRoot,
	)
	if err != nil {
		return fmt.Errorf("failed to sign envelope: %w", err)
	}

	signedEnvelope := &gloas.SignedExecutionPayloadEnvelope{
		Message:   &envelope,
		Signature: signature,
	}

	signedEnvelopeJSON, err := json.Marshal(signedEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal signed envelope: %w", err)
	}

	// Step 3: Publish the signed envelope.
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
