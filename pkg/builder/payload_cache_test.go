package builder

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeEvent(slot uint64, parentRoot, parentHash, blockHash byte, value uint64, variant PayloadVariant) *PayloadReadyEvent {
	var pr phase0.Root
	pr[0] = parentRoot
	var ph, bh phase0.Hash32
	ph[0] = parentHash
	bh[0] = blockHash
	return &PayloadReadyEvent{
		Slot:            phase0.Slot(slot),
		ParentBlockRoot: pr,
		ParentBlockHash: ph,
		BlockHash:       bh,
		BlockValue:      value,
		Variant:         variant,
	}
}

func TestPayloadCache_StoresFullAndEmptyForSameSlot(t *testing.T) {
	c := NewPayloadCache(8)

	full := makeEvent(10, 0xaa, 0xff, 0x01, 100, PayloadVariantFull)
	empty := makeEvent(10, 0xaa, 0xee, 0x02, 50, PayloadVariantEmpty)

	c.Store(full)
	c.Store(empty)

	all := c.GetAllForSlot(phase0.Slot(10))
	require.Len(t, all, 2, "both variants must coexist for the same slot")

	gotFull := c.GetByVariant(phase0.Slot(10), PayloadVariantFull)
	require.NotNil(t, gotFull)
	assert.Equal(t, full.BlockHash, gotFull.BlockHash)

	gotEmpty := c.GetByVariant(phase0.Slot(10), PayloadVariantEmpty)
	require.NotNil(t, gotEmpty)
	assert.Equal(t, empty.BlockHash, gotEmpty.BlockHash)
}

func TestPayloadCache_GetReturnsHighestValue(t *testing.T) {
	c := NewPayloadCache(8)

	low := makeEvent(20, 0xbb, 0xff, 0x10, 50, PayloadVariantFull)
	high := makeEvent(20, 0xbb, 0xee, 0x11, 200, PayloadVariantEmpty)

	c.Store(low)
	c.Store(high)

	got := c.Get(phase0.Slot(20))
	require.NotNil(t, got)
	assert.Equal(t, uint64(200), got.BlockValue, "Get(slot) should return the highest-value entry")
}

func TestPayloadCache_KeepsHighestValueOnSameKey(t *testing.T) {
	c := NewPayloadCache(8)

	low := makeEvent(30, 0xcc, 0xdd, 0x20, 100, PayloadVariantFull)
	high := makeEvent(30, 0xcc, 0xdd, 0x21, 300, PayloadVariantFull)

	c.Store(low)
	c.Store(high)
	c.Store(low) // re-store lower; should be ignored

	all := c.GetAllForSlot(phase0.Slot(30))
	require.Len(t, all, 1, "same triple key should retain only one entry")
	assert.Equal(t, uint64(300), all[0].BlockValue)
}

func TestPayloadCache_CleanupRemovesOldSlotsBothVariants(t *testing.T) {
	c := NewPayloadCache(8)

	c.Store(makeEvent(40, 0x01, 0x10, 0x30, 100, PayloadVariantFull))
	c.Store(makeEvent(40, 0x01, 0x11, 0x31, 100, PayloadVariantEmpty))
	c.Store(makeEvent(50, 0x02, 0x20, 0x40, 100, PayloadVariantFull))

	c.Cleanup(phase0.Slot(50))

	assert.Empty(t, c.GetAllForSlot(phase0.Slot(40)), "slot 40 should be evicted")
	assert.Len(t, c.GetAllForSlot(phase0.Slot(50)), 1, "slot 50 should remain")
}

func TestPayloadCache_GetByBlockHashFindsAcrossVariants(t *testing.T) {
	c := NewPayloadCache(8)

	full := makeEvent(60, 0xaa, 0xff, 0x50, 100, PayloadVariantFull)
	empty := makeEvent(60, 0xaa, 0xee, 0x51, 100, PayloadVariantEmpty)
	c.Store(full)
	c.Store(empty)

	require.Equal(t, full, c.GetByBlockHash(full.BlockHash))
	require.Equal(t, empty, c.GetByBlockHash(empty.BlockHash))
}
