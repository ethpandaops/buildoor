package payload_builder

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/inclusionlist"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// recordingEngine records the forkchoice updates' payload attributes.
type recordingEngine struct {
	EngineClient
	attrs []*engineall.PayloadAttributes
}

func (e *recordingEngine) ForkchoiceUpdatedAgnostic(
	_ context.Context, request *engineall.ForkchoiceUpdatedRequest,
) (*paris.ForkchoiceUpdatedResponse, error) {
	e.attrs = append(e.attrs, request.PayloadAttributes)
	id := paris.PayloadID{byte(len(e.attrs))}

	return &paris.ForkchoiceUpdatedResponse{
		PayloadStatus: paris.PayloadStatus{Status: paris.PayloadValidationStatusValid},
		PayloadID:     &id,
	}, nil
}

func newInclusionListBuilder(t *testing.T) (*PayloadBuilder, *recordingEngine, *beacon.PayloadAttributesEvent) {
	t.Helper()

	engine := &recordingEngine{}
	b, attrs, _ := newLocalTestBuilder(nil)
	b.engineClient = engine
	b.ilStore = inclusionlist.NewStore(nil, logrus.New())

	return b, engine, attrs
}

func addList(store *inclusionlist.Store, slot phase0.Slot, member phase0.ValidatorIndex, txs ...[]byte) {
	store.Add(&beacon.InclusionListEvent{
		Slot:           slot,
		ValidatorIndex: member,
		DependentRoot:  phase0.Root{0xd1},
		Transactions:   txs,
		ReceivedAt:     time.Now(),
	})
}

func TestResolveInclusionList(t *testing.T) {
	b, _, attrs := newInclusionListBuilder(t)
	attrs.InclusionListTransactions = [][]byte{[]byte("from-attributes"), []byte("shared")}

	addList(b.ilStore, 99, 1, []byte("gossiped"), []byte("shared"))

	require.Nil(t, b.resolveInclusionList(context.Background(), attrs, version.DataVersionGloas, nil), "before Heze")

	il := b.resolveInclusionList(context.Background(), attrs, version.DataVersionHeze, nil)
	require.Equal(t, phase0.Slot(99), il.Slot)
	require.Equal(t, [][]byte{[]byte("gossiped"), []byte("shared"), []byte("from-attributes")}, il.listTransactions(),
		"gossiped lists merged with the attributes' list")
	require.Equal(t, 1, il.Lists)

	omitOne := &InclusionListRequest{
		Mode: config.InclusionListInclude,
		Omit: map[common.Hash]struct{}{crypto.Keccak256Hash([]byte("shared")): {}},
	}
	il = b.resolveInclusionList(context.Background(), attrs, version.DataVersionHeze, omitOne)
	require.Equal(t, [][]byte{[]byte("gossiped"), []byte("from-attributes")}, il.listTransactions())
	require.Equal(t, 1, il.Omitted)

	il = b.resolveInclusionList(context.Background(), attrs, version.DataVersionHeze,
		&InclusionListRequest{Mode: config.InclusionListOmit})
	require.Empty(t, il.listTransactions())
	require.NotNil(t, il.listTransactions(), "omit still sends the (empty) field")
	require.Equal(t, 3, il.Omitted)
}

func TestEngineBuildRestartsWithANewList(t *testing.T) {
	b, engine, attrs := newInclusionListBuilder(t)

	prelude := &buildPrelude{
		beaconFork:    version.DataVersionHeze,
		engineVersion: enginev.DataVersionBogota,
		payloadAttrs:  &engineall.PayloadAttributes{Version: enginev.DataVersionBogota},
		fcState:       &paris.ForkchoiceState{},
	}
	prelude.inclusionList = b.resolveInclusionList(context.Background(), attrs, prelude.beaconFork, nil)

	restarted, _ := b.restartWithInclusionList(context.Background(), attrs, prelude)
	require.Nil(t, restarted, "an unchanged list keeps the build")
	require.Empty(t, engine.attrs)

	addList(b.ilStore, 99, 1, []byte("late"))

	restarted, id := b.restartWithInclusionList(context.Background(), attrs, prelude)
	require.NotNil(t, restarted)
	require.Equal(t, paris.PayloadID{1}, id)
	require.Len(t, engine.attrs, 1)
	require.Equal(t, []paris.Transaction{paris.Transaction("late")}, engine.attrs[0].InclusionListTransactions)
	require.Equal(t, [][]byte{[]byte("late")}, restarted.inclusionList.listTransactions())
	require.Empty(t, prelude.payloadAttrs.InclusionListTransactions, "the shared prelude stays untouched")
}
