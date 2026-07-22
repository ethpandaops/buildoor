package chain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasBuilderExited(t *testing.T) {
	tests := []struct {
		name string
		info *BuilderInfo
		want bool
	}{
		{name: "nil info (pubkey not in registry)", info: nil, want: false},
		{name: "active builder", info: &BuilderInfo{WithdrawableEpoch: FarFutureEpoch}, want: false},
		{name: "exit initiated", info: &BuilderInfo{WithdrawableEpoch: 1234}, want: true},
		{name: "exit at epoch zero", info: &BuilderInfo{WithdrawableEpoch: 0}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HasBuilderExited(tt.info))
		})
	}
}
