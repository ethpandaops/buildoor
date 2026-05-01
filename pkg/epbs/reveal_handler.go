package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
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

// SubmitReveal constructs the envelope locally, signs it, and publishes it to the beacon node.
func (h *RevealHandler) SubmitReveal(
	ctx context.Context,
	payload *BuiltPayload,
	blockInfo *beacon.BlockInfo,
) error {
	gloasPayload, err := fulu.ExecutionPayloadToGloas(payload.ExecutionPayload)
	if err != nil {
		return fmt.Errorf("failed to convert payload to gloas format: %w", err)
	}

	var execRequests *electra.ExecutionRequests
	if len(payload.ExecutionRequests) > 0 {
		execRequests, err = fulu.ParseExecutionRequests(payload.ExecutionRequests)
		if err != nil {
			return fmt.Errorf("failed to parse execution requests: %w", err)
		}
	} else {
		execRequests = &electra.ExecutionRequests{}
	}

	envelope := &gloas.ExecutionPayloadEnvelope{
		Payload:               gloasPayload,
		ExecutionRequests:     execRequests,
		BuilderIndex:          gloas.BuilderIndex(h.builderIndex),
		BeaconBlockRoot:       blockInfo.Root,
		ParentBeaconBlockRoot: blockInfo.ParentRoot,
	}

	var forkVersion phase0.Version
	if h.chainSpec.GloasForkVersion != nil {
		forkVersion = *h.chainSpec.GloasForkVersion
	}

	signature, err := h.signer.SignExecutionPayloadEnvelope(
		envelope,
		forkVersion,
		h.genesis.GenesisValidatorsRoot,
	)
	if err != nil {
		return fmt.Errorf("failed to sign envelope: %w", err)
	}

	signedEnvelope := &gloas.SignedExecutionPayloadEnvelope{
		Message:   envelope,
		Signature: signature,
	}

	signedEnvelopeJSON, err := json.Marshal(signedEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal signed envelope: %w", err)
	}

	var blobs [][]byte
	var cellProofs [][]byte

	if payload.BlobsBundle != nil && len(payload.BlobsBundle.Blobs) > 0 {
		blobs = payload.BlobsBundle.Blobs
		cellProofs = payload.BlobsBundle.Proofs

		h.log.WithFields(logrus.Fields{
			"blob_count":       len(blobs),
			"cell_proof_count": len(cellProofs),
		}).Info("Including blobs and cell proofs with envelope publish")
	}

	if err := h.clClient.SubmitExecutionPayloadEnvelope(ctx, signedEnvelopeJSON, blobs, cellProofs); err != nil {
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
