package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	enginespec "github.com/ethpandaops/go-eth-engine-client/spec"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/amsterdam"
	"github.com/ethpandaops/go-eth-engine-client/spec/cancun"
	"github.com/ethpandaops/go-eth-engine-client/spec/osaka"
	"github.com/ethpandaops/go-eth-engine-client/spec/prague"
	"github.com/ethpandaops/go-eth-engine-client/spec/shanghai"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
)

// testingBuildBlockMethod is the execution-apis testing namespace method that
// builds a payload from an explicit transaction list on a given parent.
const testingBuildBlockMethod = "testing_buildBlockV1"

// jsonRPCMethodNotFound is the JSON-RPC 2.0 error code most clients answer
// with for a method their enabled namespaces do not include.
const jsonRPCMethodNotFound = -32601

// unavailableMessage matches the error texts clients use for a missing or
// disabled method when they do not use the method-not-found code — nethermind
// answers -32600 "the method ... is found but the namespace 'Testing' is
// disabled" for a known method in a disabled namespace.
var unavailableMessage = regexp.MustCompile(`(?i)(does not exist|not available|not found|disabled|unknown method|unsupported method)`)

// TestingAPIStatus is the result of probing the EL for the testing namespace.
type TestingAPIStatus struct {
	// Available is true when testing_buildBlockV1 exists on the endpoint.
	Available bool
	// Reason explains an unavailable result (method missing, transport error).
	Reason string
}

// ProbeTestingAPI checks whether the EL exposes testing_buildBlockV1. The
// method is called with deliberately invalid parameters: a "method not found"
// error means the namespace is not enabled, while any other JSON-RPC error
// (typically invalid params) proves the method exists. Transport errors are
// reported as unavailable with the error as reason.
func (c *Client) ProbeTestingAPI(ctx context.Context) TestingAPIStatus {
	var result json.RawMessage

	err := c.rpcClient.CallContext(ctx, &result, testingBuildBlockMethod, nil)
	if err == nil {
		// The EL accepted garbage parameters; the method certainly exists.
		return TestingAPIStatus{Available: true}
	}

	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		if rpcErr.ErrorCode() == jsonRPCMethodNotFound || unavailableMessage.MatchString(rpcErr.Error()) {
			return TestingAPIStatus{
				Available: false,
				Reason:    fmt.Sprintf("EL does not expose %s (%s)", testingBuildBlockMethod, rpcErr.Error()),
			}
		}

		return TestingAPIStatus{Available: true}
	}

	return TestingAPIStatus{
		Available: false,
		Reason:    fmt.Sprintf("probe failed: %s", err.Error()),
	}
}

// BuildBlockV1 builds a payload through testing_buildBlockV1 on the given
// parent with the given attributes and transaction list, and returns it in the
// fork-agnostic engine_getPayload response shape.
//
// The transactions parameter follows the spec's three-way semantics: nil is
// encoded as JSON null (the EL MAY fill the block from its own mempool), an
// empty slice as [] (the EL MUST build an empty block), and a non-empty slice
// as the exact ordered list the EL MUST include. Blob transactions must be
// encoded the way the EL expects (network form with sidecar, or canonical).
// extraData nil leaves the field to the EL.
//
// The attributes are serialized in the shape of the given engine version, so
// post-Amsterdam builds carry slotNumber and targetGasLimit.
func (c *Client) BuildBlockV1(
	ctx context.Context,
	engineVersion enginev.DataVersion,
	parentHash common.Hash,
	attrs *engineall.PayloadAttributes,
	transactions [][]byte,
	extraData []byte,
) (*engineall.GetPayloadResponse, error) {
	if attrs == nil {
		return nil, errors.New("payload attributes are required")
	}

	var txs []hexutil.Bytes
	if transactions != nil {
		txs = make([]hexutil.Bytes, len(transactions))
		for i, tx := range transactions {
			txs[i] = hexutil.Bytes(tx)
		}
	}

	var extra *hexutil.Bytes
	if extraData != nil {
		e := hexutil.Bytes(extraData)
		extra = &e
	}

	versioned, target, err := newVersionedGetPayloadResponse(engineVersion)
	if err != nil {
		return nil, err
	}

	if err := c.rpcClient.CallContext(ctx, target, testingBuildBlockMethod, parentHash, attrs, txs, extra); err != nil {
		return nil, fmt.Errorf("%s: %w", testingBuildBlockMethod, err)
	}

	out := &engineall.GetPayloadResponse{}
	if err := out.FromVersioned(versioned); err != nil {
		return nil, fmt.Errorf("%s: decode response: %w", testingBuildBlockMethod, err)
	}

	if out.ExecutionPayload == nil {
		return nil, fmt.Errorf("%s: response carries no execution payload", testingBuildBlockMethod)
	}

	return out, nil
}

// newVersionedGetPayloadResponse allocates the versioned response container
// for the given engine version and returns it together with the concrete
// struct JSON must be decoded into (the testing response is shaped like the
// engine_getPayload response of the same version).
func newVersionedGetPayloadResponse(
	v enginev.DataVersion,
) (*enginespec.VersionedGetPayloadResponse, any, error) {
	out := &enginespec.VersionedGetPayloadResponse{Version: v}

	switch v {
	case enginev.DataVersionShanghai:
		out.Shanghai = &shanghai.GetPayloadResponse{}

		return out, out.Shanghai, nil
	case enginev.DataVersionCancun:
		out.Cancun = &cancun.GetPayloadResponse{}

		return out, out.Cancun, nil
	case enginev.DataVersionPrague:
		out.Prague = &prague.GetPayloadResponse{}

		return out, out.Prague, nil
	case enginev.DataVersionOsaka:
		out.Osaka = &osaka.GetPayloadResponse{}

		return out, out.Osaka, nil
	case enginev.DataVersionAmsterdam:
		out.Amsterdam = &amsterdam.GetPayloadResponse{}

		return out, out.Amsterdam, nil
	case enginev.DataVersionBogota:
		out.Bogota = &amsterdam.GetPayloadResponse{}

		return out, out.Bogota, nil
	default:
		return nil, nil, fmt.Errorf("%s: unsupported engine version %s", testingBuildBlockMethod, v)
	}
}
