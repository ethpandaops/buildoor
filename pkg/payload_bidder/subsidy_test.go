package payload_bidder

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplySubsidyGwei(t *testing.T) {
	tests := []struct {
		name    string
		value   uint64
		subsidy int64
		want    uint64
	}{
		{name: "zero keeps the value", value: 100, subsidy: 0, want: 100},
		{name: "positive adds", value: 100, subsidy: 25, want: 125},
		{name: "negative deducts the margin", value: 100, subsidy: -25, want: 75},
		{name: "margin above the value floors at zero", value: 100, subsidy: -250, want: 0},
		{name: "margin equal to the value", value: 100, subsidy: -100, want: 0},
		{name: "addition saturates", value: math.MaxUint64 - 1, subsidy: 5, want: math.MaxUint64},
		{name: "most negative subsidy", value: math.MaxUint64, subsidy: math.MinInt64, want: math.MaxUint64 - (1 << 63)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ApplySubsidyGwei(tt.value, tt.subsidy))
		})
	}
}
