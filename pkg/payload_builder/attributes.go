package payload_builder

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// attrSanitizeTimeout bounds the chain-view lookups a single attributes
// sanitization may perform.
const attrSanitizeTimeout = 3 * time.Second

// sanitizeAttributes validates a payload-attributes event against the chain
// view and returns a corrected copy when the event is inconsistent (the
// original is returned untouched when it is fine or cannot be verified).
//
// Corrections applied:
//   - A Gloas event whose execution parent hash is the beacon parent's
//     committed payload hash while that payload is known withheld references
//     an execution block that does not exist (a forkchoiceUpdated on it can
//     never resolve); the hash is redirected to the parent's own execution
//     parent — the last actually built block.
//   - A missing execution parent block number is backfilled from the chain
//     view (several clients omit it under Gloas).
func (s *Service) sanitizeAttributes(event *beacon.PayloadAttributesEvent) *beacon.PayloadAttributesEvent {
	headTracker := s.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return event
	}

	parentEpoch := phase0.Epoch(0)
	if event.ProposalSlot > 0 {
		parentEpoch = s.chainSvc.GetEpochOfSlot(event.ProposalSlot - 1)
	}

	if s.chainSvc.ActiveForkAtEpoch(parentEpoch) < version.DataVersionGloas {
		return event
	}

	ctx, cancel := context.WithTimeout(s.ctx, attrSanitizeTimeout)
	defer cancel()

	parentBlock, err := headTracker.GetBlock(ctx, event.ParentBlockRoot)
	if err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":        event.ProposalSlot,
			"parent_root": fmt.Sprintf("%#x", event.ParentBlockRoot[:8]),
		}).Debug("Cannot verify payload attributes parent (block unknown)")

		return event
	}

	sanitized := event

	// The event claims a full parent whose payload the chain view knows was
	// withheld: the referenced execution block was never revealed and cannot
	// be built on. Redirect to the parent's own execution parent. The
	// expected withdrawals differ between the full and empty parent (they
	// stay unchanged when the parent payload is withheld), so they are
	// re-sourced from the parent block's own slot attributes when available.
	if event.ParentBlockHash == parentBlock.ExecutionBlockHash &&
		parentBlock.ExecutionBlockHash != parentBlock.FinalitySafeExecutionBlockHash &&
		headTracker.GetPayloadStatus(parentBlock.Root) == chain.PayloadStatusEmpty {
		corrected := *sanitized
		corrected.ParentBlockHash = parentBlock.FinalitySafeExecutionBlockHash
		corrected.ParentBlockNumber = 0

		var parentSlotAttrs *beacon.PayloadAttributesEvent
		if s.clClient != nil {
			parentSlotAttrs = s.clClient.Events().GetLatestPayloadAttributes(parentBlock.Slot)
		}

		if parentSlotAttrs != nil {
			corrected.Withdrawals = parentSlotAttrs.Withdrawals
		} else {
			s.log.WithFields(logrus.Fields{
				"slot":        event.ProposalSlot,
				"parent_slot": parentBlock.Slot,
			}).Warn("No attributes for the parent block's slot, " +
				"redirected build keeps the (possibly wrong) full-parent withdrawals")
		}

		sanitized = &corrected

		s.log.WithFields(logrus.Fields{
			"slot":           event.ProposalSlot,
			"claimed_parent": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
			"actual_parent":  fmt.Sprintf("%x", corrected.ParentBlockHash[:8]),
		}).Warn("Payload attributes reference an unrevealed execution payload, " +
			"redirecting to the last built execution block")
	}

	if sanitized.ParentBlockNumber == 0 {
		number, _ := headTracker.ResolveELParentMeta(
			ctx, sanitized.ParentBlockRoot, sanitized.ParentBlockHash)
		if number != 0 {
			if sanitized == event {
				corrected := *event
				sanitized = &corrected
			}

			sanitized.ParentBlockNumber = number
		}
	}

	return sanitized
}
