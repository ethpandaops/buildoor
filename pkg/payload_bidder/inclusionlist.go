package payload_bidder

import (
	"context"
	"strings"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/inclusionlist"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// InclusionListBits returns the inclusion list bits a bid on the payload
// claims (FOCIL, Heze), per mode:
//   - view (the honest builder): the committee lists observed now for the
//     payload's slot and parent — gossip ignores a bid missing a timely list
//     a node saw, a proposer one missing a list it saw;
//   - all: every committee position; none: no position.
//
// Without a known committee the view falls back to the bits the payload was
// built with.
func InclusionListBits(ctx context.Context, store *inclusionlist.Store, p *payload_builder.Payload, mode string) []byte {
	size := 2
	if store != nil {
		size = store.BitsLen()
	}

	bits := make([]byte, size)

	switch strings.ToLower(mode) {
	case config.InclusionListBitsAll:
		for i := range bits {
			bits[i] = 0xff
		}

		return bits
	case config.InclusionListBitsNone:
		return bits
	}

	var observed []byte

	if store != nil && p.Attributes != nil && p.Attributes.ProposalSlot > 0 {
		observed = store.View(ctx, p.Attributes.ProposalSlot-1, p.Attributes.ParentBlockRoot).Bits
	}

	if observed == nil && p.InclusionList != nil {
		observed = p.InclusionList.Bits
	}

	copy(bits, observed)

	return bits
}
