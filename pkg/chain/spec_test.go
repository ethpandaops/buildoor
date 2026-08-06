package chain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseChainSpecSlotDuration(t *testing.T) {
	tests := []struct {
		name     string
		specData map[string]string
		want     time.Duration
		wantErr  bool
	}{
		{
			// EIP-7782 networks serve a stale SECONDS_PER_SLOT alongside
			// SLOT_DURATION_MS; the ms value must win.
			name: "SLOT_DURATION_MS preferred over stale SECONDS_PER_SLOT",
			specData: map[string]string{
				"SLOT_DURATION_MS": "6000",
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 6 * time.Second,
		},
		{
			name: "SECONDS_PER_SLOT fallback",
			specData: map[string]string{
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 12 * time.Second,
		},
		{
			name: "sub-second SLOT_DURATION_MS",
			specData: map[string]string{
				"SLOT_DURATION_MS": "500",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 500 * time.Millisecond,
		},
		{
			name: "zero SLOT_DURATION_MS rejected",
			specData: map[string]string{
				"SLOT_DURATION_MS": "0",
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			wantErr: true,
		},
		{
			name: "neither slot duration key present",
			specData: map[string]string{
				"SLOTS_PER_EPOCH": "32",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseChainSpec(tt.specData, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, spec.SecondsPerSlot)
		})
	}
}
