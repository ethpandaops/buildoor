package beacon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ethpandaops/go-eth2-client/api"
	"github.com/stretchr/testify/require"
)

// The distinction decides whether a builder key is spent or retried, and the
// client returns its error by pointer — a value-only check silently turns every
// rejection into a retry, which at fleet scale spins through every key.
func TestBidRejected(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "transport failure", err: errors.New("connection refused")},
		{
			name: "wrapped pointer rejection",
			err:  fmt.Errorf("failed to submit bid: %w", &api.Error{StatusCode: 400}),
			want: true,
		},
		{
			name: "wrapped value rejection",
			err:  fmt.Errorf("failed to submit bid: %w", api.Error{StatusCode: 400}),
			want: true,
		},
		{
			name: "server error is still a rejection we saw",
			err:  &api.Error{StatusCode: 500},
			want: true,
		},
		{name: "success-ish status is not a rejection", err: &api.Error{StatusCode: 202}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, BidRejected(test.err))
		})
	}
}
