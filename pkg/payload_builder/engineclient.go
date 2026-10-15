package payload_builder

import (
	"context"

	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/identification"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
)

// EngineClient is the subset of the go-eth-engine-client JSON-RPC service used
// by buildoor. It dispatches each fork-agnostic request to the matching
// versioned engine_* method internally. The jsonrpc.Service satisfies it.
type EngineClient interface {
	// ForkchoiceUpdatedAgnostic updates the forkchoice and optionally starts a
	// payload build, using the fork-agnostic request union.
	ForkchoiceUpdatedAgnostic(
		ctx context.Context,
		request *engineall.ForkchoiceUpdatedRequest,
	) (*paris.ForkchoiceUpdatedResponse, error)

	// GetPayloadAgnostic retrieves a built payload as the fork-agnostic union,
	// dispatching to the engine_getPayload version implied by dataVersion.
	GetPayloadAgnostic(
		ctx context.Context,
		dataVersion enginev.DataVersion,
		payloadID paris.PayloadID,
	) (*engineall.GetPayloadResponse, error)

	// GetBlobsAgnostic reads blobs and proofs from the EL blob pool
	// (engine_getBlobsV1 / V2 by the request version), all-or-nothing.
	GetBlobsAgnostic(
		ctx context.Context,
		request *engineall.GetBlobsRequest,
	) (*engineall.GetBlobsResponse, error)

	// ClientVersion exchanges client identity via engine_getClientVersionV1.
	ClientVersion(
		ctx context.Context,
		clientVersion *identification.ClientVersion,
	) ([]*identification.ClientVersion, error)
}
