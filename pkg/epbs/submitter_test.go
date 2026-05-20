package epbs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

func sampleSignedBid() *gloas.SignedExecutionPayloadBid {
	return &gloas.SignedExecutionPayloadBid{
		Message: &gloas.ExecutionPayloadBid{
			ParentBlockHash:       phase0.Hash32{0x11},
			ParentBlockRoot:       phase0.Root{0x22},
			BlockHash:             phase0.Hash32{0x33},
			PrevRandao:            phase0.Root{0x44},
			FeeRecipient:          bellatrix.ExecutionAddress{0x55},
			GasLimit:              30_000_000,
			BuilderIndex:          gloas.BuilderIndex(7),
			Slot:                  phase0.Slot(123),
			Value:                 phase0.Gwei(1_000_000),
			ExecutionPayment:      0,
			BlobKZGCommitments:    []deneb.KZGCommitment{},
			ExecutionRequestsRoot: [32]byte{0x66},
		},
		Signature: phase0.BLSSignature{0x77},
	}
}

// TestHTTPSubmitter_HappyPath stands up a stub beacon endpoint and verifies
// the HTTP submitter posts JSON to /eth/v1/beacon/execution_payload_bid with
// the right headers and a body that round-trips back to the original bid.
func TestHTTPSubmitter_HappyPath(t *testing.T) {
	var (
		gotPath   string
		gotMethod string
		gotCT     string
		gotBody   []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	clClient, err := beacon.NewClient(context.Background(), srv.URL, logrus.New())
	require.NoError(t, err)

	defer clClient.Close()

	submitter := NewHTTPSubmitter(clClient)
	signed := sampleSignedBid()

	require.NoError(t, submitter.Submit(context.Background(), signed))

	assert.Equal(t, "/eth/v1/beacon/execution_payload_bid", gotPath)
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "application/json", gotCT)

	// Body must be a JSON-encoded SignedExecutionPayloadBid that round-trips.
	var roundtrip gloas.SignedExecutionPayloadBid
	require.NoError(t, json.Unmarshal(gotBody, &roundtrip))
	assert.Equal(t, signed.Message.Slot, roundtrip.Message.Slot)
	assert.Equal(t, signed.Message.BlockHash, roundtrip.Message.BlockHash)
	assert.Equal(t, signed.Signature, roundtrip.Signature)
}

// TestHTTPSubmitter_ServerError surfaces non-2xx responses to the caller.
func TestHTTPSubmitter_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer srv.Close()

	clClient, err := beacon.NewClient(context.Background(), srv.URL, logrus.New())
	require.NoError(t, err)

	defer clClient.Close()

	submitter := NewHTTPSubmitter(clClient)

	err = submitter.Submit(context.Background(), sampleSignedBid())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}
