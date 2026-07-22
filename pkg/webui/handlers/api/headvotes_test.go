package api

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

func TestBuildHeadVoteRows(t *testing.T) {
	detail := chain.VoteDetail{
		Slot:         5,
		TotalMembers: 6,
		Attesters: []chain.VoteAttester{
			{Index: 0, SeenAtMs: 100, InBlock: true},     // node-a, bucket 0
			{Index: 1, SeenAtMs: 260, InBlock: true},     // node-a, bucket 1
			{Index: 2, SeenAtMs: -1, InBlock: true},      // node-a, on chain but unseen
			{Index: 10, SeenAtMs: 2100, InBlock: true},   // node-b, bucket 8
			{Index: 11, SeenAtMs: -1, InBlock: false},    // node-b, never voted
			{Index: 99, SeenAtMs: 99999, InBlock: false}, // unknown, clamps into last bucket
		},
	}

	nameOf := func(idx phase0.ValidatorIndex) string {
		switch {
		case idx < 10:
			return "node-a"
		case idx < 20:
			return "node-b"
		default:
			return headVoteRowUnknown
		}
	}

	rows := buildHeadVoteRows(detail, nameOf, 250, 36)
	require.Len(t, rows, 3)

	// Sorted by name, unknown last.
	assert.Equal(t, "node-a", rows[0].Name)
	assert.Equal(t, "node-b", rows[1].Name)
	assert.Equal(t, headVoteRowUnknown, rows[2].Name)

	a := rows[0]
	assert.Equal(t, 3, a.Members)
	assert.Equal(t, 2, a.Seen)
	assert.Equal(t, 1, a.InBlockUnseen)
	require.Len(t, a.Counts, 36)
	assert.Equal(t, 1, a.Counts[0])
	assert.Equal(t, 1, a.Counts[1])

	b := rows[1]
	assert.Equal(t, 2, b.Members)
	assert.Equal(t, 1, b.Seen)
	assert.Zero(t, b.InBlockUnseen, "never-voted members are not in-block-unseen")
	assert.Equal(t, 1, b.Counts[8])

	u := rows[2]
	assert.Equal(t, 1, u.Seen)
	assert.Equal(t, 1, u.Counts[35], "late arrivals clamp into the last bucket")
}
