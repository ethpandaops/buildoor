package beacon

import (
	"context"
	"testing"

	"github.com/ethpandaops/go-eth2-client/mock"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// newMockedClient returns a beacon Client backed by go-eth2-client's mock
// service (which implements ExecutionPayloadEnvelopeSubmitter as a no-op).
func newMockedClient(t *testing.T) *Client {
	t.Helper()

	mockSvc, err := mock.New(context.Background())
	require.NoError(t, err)

	return &Client{
		client: mockSvc,
		log:    logrus.New(),
	}
}

func TestSubmitExecutionPayloadEnvelopeBlobValidation(t *testing.T) {
	client := newMockedClient(t)
	ctx := context.Background()
	envelope := &eth2all.SignedExecutionPayloadEnvelope{Version: version.DataVersionGloas}

	t.Run("invalid blob length", func(t *testing.T) {
		err := client.SubmitExecutionPayloadEnvelope(ctx, envelope, [][]byte{make([]byte, 100)}, nil)
		require.ErrorContains(t, err, "invalid blob 0")
	})

	t.Run("invalid kzg proof length", func(t *testing.T) {
		err := client.SubmitExecutionPayloadEnvelope(ctx, envelope,
			[][]byte{make([]byte, 131072)}, [][]byte{make([]byte, 47)})
		require.ErrorContains(t, err, "invalid kzg proof 0")
	})

	t.Run("valid submission", func(t *testing.T) {
		err := client.SubmitExecutionPayloadEnvelope(ctx, envelope,
			[][]byte{make([]byte, 131072)}, [][]byte{make([]byte, 48)})
		require.NoError(t, err)
	})

	t.Run("no blobs", func(t *testing.T) {
		err := client.SubmitExecutionPayloadEnvelope(ctx, envelope, nil, nil)
		require.NoError(t, err)
	})
}
