package payload_builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExpectedBidGasLimit(t *testing.T) {
	tests := []struct {
		name           string
		parentGasLimit uint64
		targetGasLimit uint64
		expected       uint64
	}{
		{
			name:           "target reachable within one step",
			parentGasLimit: 300_000_000,
			targetGasLimit: 300_000_000,
			expected:       300_000_000,
		},
		{
			name:           "target slightly above parent within bounds",
			parentGasLimit: 299_903_641,
			targetGasLimit: 300_000_000,
			expected:       300_000_000,
		},
		{
			name:           "target far above parent clamps to max step up",
			parentGasLimit: 30_000_000,
			targetGasLimit: 300_000_000,
			expected:       30_000_000 + 30_000_000/1024 - 1,
		},
		{
			name:           "target far below parent clamps to max step down",
			parentGasLimit: 300_000_000,
			targetGasLimit: 45_000_000,
			expected:       300_000_000 - (300_000_000/1024 - 1),
		},
		{
			name:           "target slightly below parent within bounds",
			parentGasLimit: 300_000_000,
			targetGasLimit: 299_900_000,
			expected:       299_900_000,
		},
		{
			name:           "boundary exactly at max diff up",
			parentGasLimit: 1_024_000,
			targetGasLimit: 1_024_000 + 999,
			expected:       1_024_000 + 999,
		},
		{
			name:           "boundary one above max diff up",
			parentGasLimit: 1_024_000,
			targetGasLimit: 1_024_000 + 1000,
			expected:       1_024_000 + 999,
		},
		{
			name:           "tiny parent has zero adjustment room",
			parentGasLimit: 100,
			targetGasLimit: 200,
			expected:       100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, expectedBidGasLimit(tt.parentGasLimit, tt.targetGasLimit))
		})
	}
}
