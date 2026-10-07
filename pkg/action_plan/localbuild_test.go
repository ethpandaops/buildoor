package action_plan

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

func boolPtr(v bool) *bool    { return &v }
func u64Ptr(v uint64) *uint64 { return &v }

func TestResolveLocalBuildInheritsGlobal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LocalBuild.Enabled = true
	cfg.LocalBuild.PayloadSource = config.PayloadSourceLocalOrEL
	cfg.TxPool.MaxTxsPerBlock = 7
	cfg.TxPool.GasFillPct = 50
	cfg.TxPool.Ordering = config.TxOrderingTip

	local := resolveLocalBuild(nil, cfg)
	require.True(t, local.Enabled)
	require.Equal(t, config.PayloadSourceLocalOrEL, local.PayloadSource)
	require.Equal(t, config.TxSourceTxPool, local.TxSource)
	require.True(t, local.BuildELPayload)
	require.Equal(t, uint64(7), local.MaxTxs)
	require.Equal(t, uint64(50), local.GasFillPct)
	require.Equal(t, config.TxOrderingTip, local.Ordering)
	require.False(t, local.Forced)
}

func TestResolveLocalBuildPlanOverrides(t *testing.T) {
	cfg := config.DefaultConfig() // local build globally disabled

	plan := &SlotPlan{Build: &BuildPlan{Local: &LocalBuildPlan{
		Enabled:        boolPtr(true),
		PayloadSource:  config.PayloadSourceLocal,
		Transactions:   []string{"0x01"},
		BuildELPayload: boolPtr(false),
		MaxTxs:         u64Ptr(3),
		GasFillPct:     u64Ptr(0), // invalid → ignored
		Ordering:       "random",
	}}}

	local := resolveLocalBuild(plan, cfg)
	require.True(t, local.Enabled)
	require.True(t, local.Forced, "enabled by the plan while globally disabled")
	require.Equal(t, config.PayloadSourceLocal, local.PayloadSource)
	require.Equal(t, config.TxSourceExplicit, local.TxSource, "a transaction list implies the explicit source")
	require.Equal(t, []string{"0x01"}, local.Transactions)
	require.False(t, local.BuildELPayload)
	require.Equal(t, uint64(3), local.MaxTxs)
	require.Equal(t, uint64(100), local.GasFillPct)
	require.Equal(t, config.TxOrderingRandom, local.Ordering)

	// A plan may also switch the extension off for a slot.
	off := resolveLocalBuild(&SlotPlan{Build: &BuildPlan{Local: &LocalBuildPlan{Enabled: boolPtr(false)}}}, cfg)
	require.False(t, off.Enabled)
	require.False(t, off.Forced)
}

func signedRawTx(t *testing.T) string {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(1)
	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID: chainID, Nonce: 0, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
	})
	require.NoError(t, err)

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return hexutil.Encode(raw)
}

func TestLocalBuildPlanValidate(t *testing.T) {
	valid := signedRawTx(t)

	tests := []struct {
		name    string
		plan    LocalBuildPlan
		wantErr string
	}{
		{name: "empty inherits", plan: LocalBuildPlan{}},
		{name: "valid explicit list", plan: LocalBuildPlan{Transactions: []string{valid}}},
		{name: "explicit with matching source", plan: LocalBuildPlan{TxSource: "explicit", Transactions: []string{valid}}},
		{name: "bad payload source", plan: LocalBuildPlan{PayloadSource: "maybe"}, wantErr: "payload_source"},
		{name: "bad tx source", plan: LocalBuildPlan{TxSource: "mempool"}, wantErr: "tx_source"},
		{name: "explicit without list", plan: LocalBuildPlan{TxSource: "explicit"}, wantErr: "requires build.local.transactions"},
		{name: "list with non-explicit source", plan: LocalBuildPlan{TxSource: "txpool", Transactions: []string{valid}}, wantErr: "only allowed with tx_source explicit"},
		{name: "bad ordering", plan: LocalBuildPlan{Ordering: "price"}, wantErr: "ordering"},
		{name: "bad fill pct", plan: LocalBuildPlan{GasFillPct: u64Ptr(101)}, wantErr: "gas_fill_pct"},
		{name: "bad hex", plan: LocalBuildPlan{Transactions: []string{"zz"}}, wantErr: "transactions[0]"},
		{name: "undecodable tx", plan: LocalBuildPlan{Transactions: []string{"0x02ff"}}, wantErr: "invalid transaction"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.plan.validate()
			if tc.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestBuildPlanIsZeroWithLocal(t *testing.T) {
	require.True(t, (&BuildPlan{Local: &LocalBuildPlan{}}).isZero())
	require.False(t, (&BuildPlan{Local: &LocalBuildPlan{Enabled: boolPtr(true)}}).isZero())
	require.False(t, (&BuildPlan{Local: &LocalBuildPlan{TxSource: "empty"}}).isZero())

	// Clone is deep for the list.
	plan := &BuildPlan{Local: &LocalBuildPlan{Transactions: []string{"0x01"}}}
	clone := plan.clone()
	clone.Local.Transactions[0] = "0x02"
	require.Equal(t, "0x01", plan.Local.Transactions[0])
}
