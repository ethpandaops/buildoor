package db

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestSlotArtifactsRoundTrip(t *testing.T) {
	d := testDB(t)

	batch := []SlotArtifact{
		{Slot: 100, Kind: "payload", Idx: 0, Fork: 12, Meta: `{"v":1}`, Data: []byte{1, 2, 3}, CreatedAt: 1111},
		{Slot: 100, Kind: "bid", Idx: 0, Fork: 12, Meta: `{"v":1,"total_value_gwei":5}`, Data: []byte{4}, CreatedAt: 1112},
		{Slot: 100, Kind: "bid", Idx: 1, Fork: 12, Meta: `{"v":1,"total_value_gwei":6}`, Data: []byte{5}, CreatedAt: 1113},
		{Slot: 200, Kind: "envelope", Idx: 0, Fork: 12, Meta: "", Data: []byte{6, 7}, CreatedAt: 1114},
	}
	require.NoError(t, d.InsertSlotArtifacts(batch))

	artifact, err := d.GetSlotArtifact(100, "payload", 0)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	require.Equal(t, []byte{1, 2, 3}, artifact.Data)
	require.Equal(t, int64(12), artifact.Fork)

	missing, err := d.GetSlotArtifact(100, "envelope", 0)
	require.NoError(t, err)
	require.Nil(t, missing)

	metas, err := d.GetSlotArtifactMetas(100, "bid")
	require.NoError(t, err)
	require.Len(t, metas, 2)
	require.Equal(t, 0, metas[0].Idx)
	require.Equal(t, 1, metas[1].Idx)
	require.Nil(t, metas[0].Data, "meta listing must not load the blobs")

	empty, err := d.GetSlotArtifactMetas(300, "bid")
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestSlotArtifactsMaxIdx(t *testing.T) {
	d := testDB(t)

	_, exists, err := d.GetMaxSlotArtifactIdx(100, "bid")
	require.NoError(t, err)
	require.False(t, exists)

	require.NoError(t, d.InsertSlotArtifacts([]SlotArtifact{
		{Slot: 100, Kind: "bid", Idx: 0, Data: []byte{1}},
		{Slot: 100, Kind: "bid", Idx: 7, Data: []byte{2}},
	}))

	maxIdx, exists, err := d.GetMaxSlotArtifactIdx(100, "bid")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 7, maxIdx)
}

func TestSlotArtifactsPrune(t *testing.T) {
	d := testDB(t)

	require.NoError(t, d.InsertSlotArtifacts([]SlotArtifact{
		{Slot: 10, Kind: "payload", Data: []byte{1}},
		{Slot: 20, Kind: "payload", Data: []byte{2}},
		{Slot: 30, Kind: "payload", Data: []byte{3}},
	}))

	deleted, err := d.DeleteSlotArtifactsBefore(30)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	remaining, err := d.GetSlotArtifact(30, "payload", 0)
	require.NoError(t, err)
	require.NotNil(t, remaining)

	gone, err := d.GetSlotArtifact(20, "payload", 0)
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestSlotArtifactsDisabledNoOp(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	d := NewDatabase(&Config{}, log)
	require.NoError(t, d.Init())
	require.False(t, d.Enabled())

	require.NoError(t, d.InsertSlotArtifacts([]SlotArtifact{{Slot: 1, Kind: "payload", Data: []byte{1}}}))

	artifact, err := d.GetSlotArtifact(1, "payload", 0)
	require.NoError(t, err)
	require.Nil(t, artifact)

	metas, err := d.GetSlotArtifactMetas(1, "payload")
	require.NoError(t, err)
	require.Empty(t, metas)

	_, exists, err := d.GetMaxSlotArtifactIdx(1, "payload")
	require.NoError(t, err)
	require.False(t, exists)

	deleted, err := d.DeleteSlotArtifactsBefore(100)
	require.NoError(t, err)
	require.Zero(t, deleted)
}
