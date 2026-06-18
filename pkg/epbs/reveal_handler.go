package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// RevealHandler handles submission of execution payload reveals.
type RevealHandler struct {
	signer       *Signer
	clClient     *beacon.Client
	chainSvc     chain.Service
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewRevealHandler creates a new reveal handler.
func NewRevealHandler(
	signer *Signer,
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

// SubmitReveal constructs the envelope locally, signs it, and then publishes it to the beacon node.
func (h *RevealHandler) SubmitReveal(
	ctx context.Context,
	payload *BuiltPayload,
	blockInfo *beacon.BlockInfo,
) error {
	gloasPayload, err := fulu.GloasPayload(payload.ExecutionPayload)
	if err != nil {
		return fmt.Errorf("failed to convert payload to gloas format: %w", err)
	}

	execRequests := payload.ExecutionRequests
	if execRequests == nil {
		execRequests = &electra.ExecutionRequests{
			Deposits:       make([]*electra.DepositRequest, 0),
			Withdrawals:    make([]*electra.WithdrawalRequest, 0),
			Consolidations: make([]*electra.ConsolidationRequest, 0),
		}
	}

	envelope := &gloas.ExecutionPayloadEnvelope{
		Payload:               gloasPayload,
		ExecutionRequests:     execRequests,
		BuilderIndex:          gloas.BuilderIndex(h.builderIndex),
		BeaconBlockRoot:       blockInfo.Root,
		ParentBeaconBlockRoot: blockInfo.ParentRoot,
	}

	forkVersion, err := h.chainSvc.GetForkVersion()
	if err != nil {
		return fmt.Errorf("failed to get current fork version: %w", err)
	}

	signature, err := h.signer.SignExecutionPayloadEnvelope(
		envelope,
		forkVersion,
		h.chainSvc.GetGenesis().GenesisValidatorsRoot,
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
		blobs = payload.BlobsBundle.BlobsAsBytes()
		cellProofs = payload.BlobsBundle.ProofsAsBytes()

		h.log.WithFields(logrus.Fields{
			"blob_count":      len(blobs),
			"kzg_proof_count": len(cellProofs),
		}).Info("Including blobs and kzg proofs with envelope publish")
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
