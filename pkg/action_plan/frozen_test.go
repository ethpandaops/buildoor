package action_plan

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExecutionPaymentPortion covers the execution-payment split math: the
// absolute amount wins over the percent, the percent floors exactly, the
// proposer's advertised limit caps the portion (unless ignored), and the
// result clamps to the total.
func TestExecutionPaymentPortion(t *testing.T) {
	tests := []struct {
		name        string
		absolute    uint64
		percent     uint64
		ignoreLimit bool
		totalGwei   uint64
		prefLimit   uint64
		want        uint64
	}{
		{name: "both unset claims nothing", totalGwei: 3000, prefLimit: 3000, want: 0},
		{name: "absolute amount", absolute: 1000, totalGwei: 3000, prefLimit: 3000, want: 1000},
		{name: "absolute clamps to total", absolute: 5000, totalGwei: 3000, prefLimit: 9000, want: 3000},
		{name: "absolute wins over percent", absolute: 1000, percent: 90, totalGwei: 3000, prefLimit: 3000, want: 1000},
		{name: "percent of total", percent: 50, totalGwei: 3000, prefLimit: 3000, want: 1500},
		{name: "percent floors", percent: 33, totalGwei: 101, prefLimit: 101, want: 33},
		{name: "hundred percent", percent: 100, totalGwei: 3000, prefLimit: 3000, want: 3000},
		{name: "overlarge percent clamps", percent: 250, totalGwei: 3000, prefLimit: 3000, want: 3000},
		{name: "percent avoids overflow", percent: 50, totalGwei: ^uint64(0), prefLimit: ^uint64(0), want: ^uint64(0) / 2},
		{name: "preference limit caps the portion", absolute: 1000, totalGwei: 3000, prefLimit: 600, want: 600},
		{name: "zero preference limit suppresses", absolute: 1000, totalGwei: 3000, want: 0},
		{name: "ignore limit serves beyond the preference", absolute: 1000, ignoreLimit: true, totalGwei: 3000, want: 1000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := &ResolvedBuilderAPISettings{
				ExecutionPaymentGwei:    test.absolute,
				ExecutionPaymentPercent: test.percent,
				IgnorePreferenceLimit:   test.ignoreLimit,
			}
			assert.Equal(t, test.want, s.ExecutionPaymentPortion(test.totalGwei, test.prefLimit))
		})
	}
}
