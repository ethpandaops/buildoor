package action_plan

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

func TestFreezeInclusionList(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	omitted := "0x" + "ab" + "00000000000000000000000000000000000000000000000000000000000000"[:62]

	_, err := svc.ApplyUpdates([]*PlanUpdate{
		{Slots: []uint64{9100}, Set: map[string]json.RawMessage{
			"build.inclusion_list": json.RawMessage(`{"bits":"all","omit_txs":["` + omitted + `"]}`),
		}},
		{Slots: []uint64{9101}, Set: map[string]json.RawMessage{
			"build.inclusion_list": json.RawMessage(`{"mode":"omit"}`),
		}},
	}, "tester")
	require.NoError(t, err)

	plain := svc.Freeze(9099).Build.InclusionList
	require.Equal(t, config.InclusionListInclude, plain.Mode, "the global defaults")
	require.Equal(t, config.InclusionListBitsView, plain.Bits)

	partial := svc.Freeze(9100).Build.InclusionList
	require.Equal(t, config.InclusionListInclude, partial.Mode, "unset fields inherit")
	require.Equal(t, config.InclusionListBitsAll, partial.Bits)
	require.True(t, partial.Omits(omitted))

	require.Equal(t, config.InclusionListOmit, svc.Freeze(9101).Build.InclusionList.Mode)
}

func TestInclusionListPlanValidation(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	for _, raw := range []string{
		`{"mode":"skip"}`,
		`{"bits":"some"}`,
		`{"omit_txs":["0x1234"]}`,
	} {
		_, err := svc.ApplyUpdates([]*PlanUpdate{
			{Slots: []uint64{9200}, Set: map[string]json.RawMessage{"build.inclusion_list": json.RawMessage(raw)}},
		}, "tester")
		require.Error(t, err, raw)
	}
}
