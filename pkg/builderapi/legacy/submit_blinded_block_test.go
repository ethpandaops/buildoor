package legacy

import (
	"bytes"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// postBlindedBlock submits the builder-specs Fulu example blinded block to the
// given submit endpoint version.
func postBlindedBlock(h *Handler, apiVersion int) *httptest.ResponseRecorder {
	path := "/eth/v2/builder/blinded_blocks"
	if apiVersion == 1 {
		path = "/eth/v1/builder/blinded_blocks"
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(blindedBlockJSON())))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if apiVersion == 1 {
		h.HandleSubmitBlindedBlockV1(rec, req)
	} else {
		h.HandleSubmitBlindedBlock(rec, req)
	}

	return rec
}

// TestHandleSubmitBlindedBlock_RecordsSubmissions verifies both submit path
// versions record "received" once decoded and "accepted" after a successful
// publish, and "failed" (after "received") when no payload matches.
func TestHandleSubmitBlindedBlock_RecordsSubmissions(t *testing.T) {
	tests := []struct {
		name         string
		apiVersion   int
		seed         bool
		wantCode     int
		wantStatuses []string
		wantErrPart  string
	}{
		{
			name:         "v1 success records received then accepted",
			apiVersion:   1,
			seed:         true,
			wantCode:     http.StatusOK,
			wantStatuses: []string{submissionStatusReceived, submissionStatusAccepted},
		},
		{
			name:         "v2 success records received then accepted",
			apiVersion:   2,
			seed:         true,
			wantCode:     http.StatusAccepted,
			wantStatuses: []string{submissionStatusReceived, submissionStatusAccepted},
		},
		{
			name:         "v2 missing payload records received then failed",
			apiVersion:   2,
			seed:         false,
			wantCode:     http.StatusBadRequest,
			wantStatuses: []string{submissionStatusReceived, submissionStatusFailed},
			wantErrPart:  "no matching payload",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
			h.SetEnabled(true)
			h.SetCLClient(&stubProposalSubmitter{})

			recorder := &stubSlotResultRecorder{}
			h.SetResultRecorder(recorder)

			if test.seed {
				// BaseFeePerGas must be set for the v1 unblind response
				// (the payload is JSON-marshalled into the response body).
				event := seedPayload(h, big.NewInt(1_000_000_000))
				event.ExecutionPayload.BaseFeePerGas = uint256.NewInt(7)
			}

			rec := postBlindedBlock(h, test.apiVersion)
			assert.Equal(t, test.wantCode, rec.Code)

			calls := recorder.submissionCalls()
			require.Len(t, calls, len(test.wantStatuses))

			for i, wantStatus := range test.wantStatuses {
				assert.Equal(t, phase0.Slot(1), calls[i].slot)
				assert.Equal(t, submissionDialect, calls[i].dialect)
				assert.Equal(t, wantStatus, calls[i].status)
			}

			if test.wantErrPart != "" {
				assert.Contains(t, calls[len(calls)-1].errMsg, test.wantErrPart)
			}
		})
	}
}

// TestHandleSubmitBlindedBlock_PublishFailureRecorded verifies a failing CL
// publish records "failed" after "received".
func TestHandleSubmitBlindedBlock_PublishFailureRecorded(t *testing.T) {
	h := newTestHandler(&stubChainService{currentFork: version.DataVersionFulu}, nil)
	h.SetEnabled(true)
	h.SetCLClient(&stubProposalSubmitter{err: assert.AnError})

	recorder := &stubSlotResultRecorder{}
	h.SetResultRecorder(recorder)
	seedPayload(h, big.NewInt(1_000_000_000))

	rec := postBlindedBlock(h, 2)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	calls := recorder.submissionCalls()
	require.Len(t, calls, 2)
	assert.Equal(t, submissionStatusReceived, calls[0].status)
	assert.Equal(t, submissionStatusFailed, calls[1].status)
	assert.Contains(t, calls[1].errMsg, "failed to publish")
}
