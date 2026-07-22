package payload_bidder

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// PayloadIncludedEvent is fired when a beacon block committing to one of our
// payloads is observed at the head (consumed by the WebUI and the slot
// results tracker).
type PayloadIncludedEvent struct {
	Payload      *payload_builder.Payload
	BlockInfo    *beacon.BlockInfo
	BidValueGwei uint64
	WonBlock     *WonBlock // the won-block summary for this inclusion
}

// PayloadVerdict is the canonical outcome of a won slot's payload, derived
// from the current head's ancestry (Gloas+ only). Verdicts are re-evaluated
// on every head event within the tracking window, so a reorg can flip them;
// each change fires a new PayloadStatusEvent.
type PayloadVerdict string

const (
	// PayloadVerdictCanonical: the canonical chain's first block after the
	// won slot builds on our payload.
	PayloadVerdictCanonical PayloadVerdict = "canonical"
	// PayloadVerdictMissed: the won block is canonical but the next block
	// builds on an older execution block — the payload was withheld, revealed
	// too late, or voted empty.
	PayloadVerdictMissed PayloadVerdict = "missed"
	// PayloadVerdictOrphaned: the won beacon block itself was reorged out of
	// the canonical chain.
	PayloadVerdictOrphaned PayloadVerdict = "orphaned"
)

// PayloadStatusEvent reports the (possibly revised) canonical verdict for a
// won slot's payload.
type PayloadStatusEvent struct {
	Slot           phase0.Slot // the won slot
	Verdict        PayloadVerdict
	NextBlockSlot  phase0.Slot   // canonical block the verdict was derived from
	NextParentHash phase0.Hash32 // execution parent hash that block committed to
}

const (
	// wonTrackingWindowSlots is how long (in slots) a won slot's verdict keeps
	// being re-evaluated against the head's ancestry; reorgs deeper than this
	// window no longer revise the recorded status.
	wonTrackingWindowSlots = 16
	// blockCacheExtraSlots keeps ancestry blocks slightly longer than the
	// tracking window so verdict walks rarely refetch.
	blockCacheExtraSlots = 4
)

// wonTracking is the run-loop-owned reorg-aware state for one won slot.
type wonTracking struct {
	blockRoot phase0.Root    // beacon block that committed to our payload
	execHash  phase0.Hash32  // our payload's execution block hash
	verdict   PayloadVerdict // last fired verdict; empty until first resolution
}

// InclusionTracker watches head events and detects inclusion of our payloads.
// It records the payment obligation, requests the payload reveal, fires
// inclusion events carrying the won-block summary (won-block storage is owned
// by the slot results tracker), and checks the follow-up block to detect
// orphaned (unrevealed) payloads. Shared by both the p2p and Builder API
// flows.
type InclusionTracker struct {
	clClient   *beacon.Client
	chainSvc   chain.Service
	builderSvc *payload_builder.Service // payload cache + inclusion stats
	revealSvc  *RevealService           // optional; nil pre-Gloas
	payments   *PaymentTracker          // optional; nil pre-Gloas

	includedDispatch      utils.Dispatcher[*PayloadIncludedEvent]
	payloadStatusDispatch utils.Dispatcher[*PayloadStatusEvent]

	// Reorg-aware verdict state, owned by the run loop (no mutex): every won
	// slot is re-evaluated against each new head's ancestry until it leaves
	// the tracking window. blockCache holds resolved ancestry blocks by root.
	trackedWins map[phase0.Slot]*wonTracking
	blockCache  map[phase0.Root]*beacon.BlockInfo

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewInclusionTracker creates a new inclusion tracker. revealSvc and payments
// may be nil (pre-Gloas networks); the tracker then only fires inclusion
// events.
func NewInclusionTracker(
	clClient *beacon.Client,
	chainSvc chain.Service,
	builderSvc *payload_builder.Service,
	revealSvc *RevealService,
	payments *PaymentTracker,
	log logrus.FieldLogger,
) *InclusionTracker {
	return &InclusionTracker{
		clClient:    clClient,
		chainSvc:    chainSvc,
		builderSvc:  builderSvc,
		revealSvc:   revealSvc,
		payments:    payments,
		trackedWins: make(map[phase0.Slot]*wonTracking, 4),
		blockCache:  make(map[phase0.Root]*beacon.BlockInfo, 32),
		log:         log.WithField("component", "inclusion-tracker"),
	}
}

// SubscribeIncluded subscribes to payload inclusion events.
func (t *InclusionTracker) SubscribeIncluded(capacity int, blocking bool) *utils.Subscription[*PayloadIncludedEvent] {
	return t.includedDispatch.Subscribe(capacity, blocking)
}

// SubscribePayloadStatus subscribes to the canonical/missed verdicts derived
// from the follow-up block after a won slot (Gloas+ only).
func (t *InclusionTracker) SubscribePayloadStatus(capacity int, blocking bool) *utils.Subscription[*PayloadStatusEvent] {
	return t.payloadStatusDispatch.Subscribe(capacity, blocking)
}

// Start starts the inclusion tracker's main loop.
func (t *InclusionTracker) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	t.wg.Add(1)

	go t.run()

	t.log.Info("Inclusion tracker started")

	return nil
}

// Stop stops the inclusion tracker and waits for the main loop to exit.
func (t *InclusionTracker) Stop() {
	if t.cancel != nil {
		t.cancel()
	}

	t.wg.Wait()

	t.log.Info("Inclusion tracker stopped")
}

// run is the main loop: process head events and prune expired payments on
// epoch transitions.
func (t *InclusionTracker) run() {
	defer t.wg.Done()

	headSub := t.clClient.Events().SubscribeHead()
	epochSub := t.chainSvc.SubscribeEpochStats()

	defer headSub.Unsubscribe()
	defer epochSub.Unsubscribe()

	for {
		select {
		case <-t.ctx.Done():
			return
		case event := <-headSub.Channel():
			t.processHead(event)
		case epochStats, ok := <-epochSub.Channel():
			if ok && t.payments != nil {
				t.payments.PruneExpiredPayments(epochStats.Epoch)
				// The new epoch's builder snapshot is authoritative, so drop
				// local balance deltas anchored to earlier epochs.
				t.payments.ReconcileToEpoch(epochStats.Epoch)
			}
		}
	}
}

// processHead resolves the head block's info and runs the inclusion checks.
func (t *InclusionTracker) processHead(event *beacon.HeadEvent) {
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()

	blockInfo, err := t.clClient.GetBlockInfo(ctx, fmt.Sprintf("0x%x", event.Block[:]))
	if err != nil {
		t.log.WithError(err).WithField("slot", event.Slot).Debug("Failed to get block info")
		return
	}

	t.processBlockInfo(blockInfo)
}

// processBlockInfo handles a resolved head block:
//  1. Check if this block commits to one of our payloads (arms tracking).
//  2. Re-evaluate the canonical verdict of every tracked won slot against
//     this head's ancestry — reorgs flip verdicts, each change fires an event.
//  3. Prune tracking state that left the window.
func (t *InclusionTracker) processBlockInfo(blockInfo *beacon.BlockInfo) {
	t.blockCache[blockInfo.Root] = blockInfo

	t.checkForOurPayload(blockInfo)
	t.evaluateTrackedWins(blockInfo)
	t.pruneTracking(blockInfo.Slot)
}

// evaluateTrackedWins re-derives every tracked won slot's canonical verdict
// from the new head's ancestry and fires a PayloadStatusEvent whenever the
// verdict changed (first resolution included). The verdict comes from the
// chain itself: walking parent roots from the head yields the canonical block
// at the won slot (bid included vs. orphaned) and the first canonical block
// after it, whose committed parent execution hash
// (BlockInfo.FinalitySafeExecutionBlockHash = the Gloas bid's
// parent_block_hash) proves whether our payload became canonical.
func (t *InclusionTracker) evaluateTrackedWins(head *beacon.BlockInfo) {
	for slot, win := range t.trackedWins {
		if head.Slot <= slot {
			continue
		}

		verdict, nextBlock := t.resolveVerdict(head, slot, win)
		if verdict == "" || verdict == win.verdict {
			continue
		}

		firstVerdict := win.verdict == ""
		win.verdict = verdict

		t.payloadStatusDispatch.Fire(&PayloadStatusEvent{
			Slot:           slot,
			Verdict:        verdict,
			NextBlockSlot:  nextBlock.Slot,
			NextParentHash: nextBlock.FinalitySafeExecutionBlockHash,
		})

		logFields := logrus.Fields{
			"slot":            slot,
			"block_hash":      fmt.Sprintf("%x", win.execHash[:8]),
			"next_block_slot": nextBlock.Slot,
			"verdict":         verdict,
		}

		if verdict == PayloadVerdictCanonical {
			t.log.WithFields(logFields).Info("Won payload is canonical — the chain builds on it")
		} else {
			t.log.WithFields(logFields).Warn("Won payload is NOT canonical")
		}

		if firstVerdict {
			t.logPaymentState(slot)
		}
	}
}

// resolveVerdict walks the head's ancestry down to the won slot. Returns an
// empty verdict when an ancestor cannot be resolved (transient fetch failure;
// retried on the next head event).
func (t *InclusionTracker) resolveVerdict(
	head *beacon.BlockInfo, slot phase0.Slot, win *wonTracking,
) (PayloadVerdict, *beacon.BlockInfo) {
	// next is the earliest visited canonical block with Slot > slot; cursor
	// descends until it reaches the canonical block at or before the won slot.
	next := head
	cursor := head

	for cursor.Slot > slot {
		next = cursor

		parent, ok := t.getBlock(cursor.ParentRoot)
		if !ok {
			return "", nil
		}

		cursor = parent
	}

	if cursor.Root != win.blockRoot {
		// The canonical chain no longer contains the block that committed to
		// our payload.
		return PayloadVerdictOrphaned, next
	}

	if next.FinalitySafeExecutionBlockHash == win.execHash {
		return PayloadVerdictCanonical, next
	}

	return PayloadVerdictMissed, next
}

// getBlock resolves a block by root through the ancestry cache, fetching from
// the beacon node on a miss.
func (t *InclusionTracker) getBlock(root phase0.Root) (*beacon.BlockInfo, bool) {
	if info, ok := t.blockCache[root]; ok {
		return info, true
	}

	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()

	info, err := t.clClient.GetBlockInfo(ctx, fmt.Sprintf("%#x", root))
	if err != nil {
		t.log.WithError(err).WithField("root", fmt.Sprintf("%#x", root)).
			Debug("Failed to resolve ancestry block")
		return nil, false
	}

	t.blockCache[root] = info

	return info, true
}

// pruneTracking drops won-slot tracking and ancestry-cache entries that left
// the reorg window.
func (t *InclusionTracker) pruneTracking(headSlot phase0.Slot) {
	if headSlot <= wonTrackingWindowSlots {
		return
	}

	minWinSlot := headSlot - wonTrackingWindowSlots
	for slot := range t.trackedWins {
		if slot < minWinSlot {
			delete(t.trackedWins, slot)
		}
	}

	if headSlot <= wonTrackingWindowSlots+blockCacheExtraSlots {
		return
	}

	minCacheSlot := headSlot - wonTrackingWindowSlots - blockCacheExtraSlots
	for root, info := range t.blockCache {
		if info.Slot < minCacheSlot {
			delete(t.blockCache, root)
		}
	}
}

// logPaymentState logs the payment consequence once the first verdict for a
// won slot is known. The RevealService stamps the shared payload on a
// successful reveal, so the payload's own reveal record is the source of
// truth: revealed payments were already deducted from the live balance via
// PaymentTracker.MarkRevealed; unrevealed ones stay pending for 2 epochs and
// settle through the beacon state's BuilderPendingPayments quorum logic.
func (t *InclusionTracker) logPaymentState(slot phase0.Slot) {
	payload := t.builderSvc.GetPayloadCache().Get(slot)
	if payload != nil && payload.Reveal() != nil {
		t.log.WithField("slot", slot).
			Debug("Won bid was revealed, payment already deducted")
		return
	}

	t.log.WithField("slot", slot).
		Warn("Won bid was NOT revealed — payment pending for 2 epochs")
}

// checkForOurPayload checks if the beacon block commits to one of our payloads
// (pre-Gloas the payload is embedded in the block, so the execution block hash
// match works on all forks).
func (t *InclusionTracker) checkForOurPayload(blockInfo *beacon.BlockInfo) {
	payload := t.builderSvc.GetPayloadCache().GetByBlockHash(blockInfo.ExecutionBlockHash)
	if payload == nil {
		return
	}

	bidValueGwei := uint64(0)
	if payload.BlockValue != nil && payload.BlockValue.Sign() > 0 {
		bidValueGwei = new(big.Int).Div(payload.BlockValue, big.NewInt(1_000_000_000)).Uint64()
	}

	t.log.WithFields(logrus.Fields{
		"slot":       blockInfo.Slot,
		"block_hash": fmt.Sprintf("%x", blockInfo.ExecutionBlockHash[:8]),
		"bid_value":  bidValueGwei,
	}).Info("Our payload was included in a beacon block!")

	// Builder payments and reveals only exist post-Gloas; before that the
	// payload is part of the block itself and nothing is owed or revealed.
	if t.chainSvc.GetCurrentFork() >= version.DataVersionGloas && t.revealSvc != nil && t.payments != nil {
		// Record as pending payment (moved to a balance deduction if revealed,
		// or pending for 2 epochs if not).
		if bidValueGwei > 0 {
			t.payments.RecordWonBid(payload.Attributes.ProposalSlot, bidValueGwei)
		}

		// Request the reveal; the per-slot dedup makes this a no-op for
		// Builder-API-won slots whose reveal was requested at delivery time.
		t.revealSvc.RequestReveal(&RevealRequest{
			Payload:   payload,
			BlockInfo: blockInfo,
			Transport: payload_builder.BidTransportP2P,
		})
	}

	// Build the won-block summary carried on the inclusion event. Storage is
	// owned by the slot results tracker (a subscriber); this tracker only
	// derives the summary.
	wonBlock := t.buildWonBlock(payload, blockInfo.ExecutionBlockHash)

	t.builderSvc.IncrementBlocksIncluded()

	t.includedDispatch.Fire(&PayloadIncludedEvent{
		Payload:      payload,
		BlockInfo:    blockInfo,
		BidValueGwei: bidValueGwei,
		WonBlock:     wonBlock,
	})

	// Arm the reorg-aware canonical tracking (Gloas+ only: pre-Gloas the
	// payload is embedded in the winning block and canonical immediately).
	// A re-inclusion after a reorg overwrites the entry and resets the
	// verdict, so the revised outcome fires again.
	slot := payload.Attributes.ProposalSlot
	if t.chainSvc.ActiveForkAtEpoch(t.chainSvc.GetEpochOfSlot(slot)) >= version.DataVersionGloas {
		t.trackedWins[slot] = &wonTracking{
			blockRoot: blockInfo.Root,
			execHash:  blockInfo.ExecutionBlockHash,
		}
	}
}

// buildWonBlock derives the won-block summary for an included payload (no
// storage side effects). The source is derived from the payload's bid
// records: any Builder-API bid marks the win as a Builder API delivery,
// otherwise it was won via p2p bidding.
func (t *InclusionTracker) buildWonBlock(
	payload *payload_builder.Payload, blockHash phase0.Hash32) *WonBlock {
	numTxs := 0
	if payload.ExecutionPayload != nil {
		numTxs = len(payload.ExecutionPayload.Transactions)
	}

	numBlobs := 0
	if payload.BlobsBundle != nil {
		numBlobs = len(payload.BlobsBundle.Commitments)
	}

	valueWei := "0"
	valueETH := "0.000000000000000000"

	if payload.BlockValue != nil {
		valueWei = payload.BlockValue.String()
		valueETH = weiToETHString(payload.BlockValue)
	}

	source := WonBlockSourceEPBS

	for _, bid := range payload.Bids() {
		if bid.Transport == payload_builder.BidTransportBuilderAPI {
			source = WonBlockSourceBuilderAPI
			break
		}
	}

	return &WonBlock{
		Source:          source,
		Slot:            uint64(payload.Attributes.ProposalSlot),
		BlockHash:       fmt.Sprintf("%#x", blockHash),
		NumTransactions: numTxs,
		NumBlobs:        numBlobs,
		ValueWei:        valueWei,
		ValueETH:        valueETH,
		Timestamp:       time.Now().UnixMilli(),
	}
}

// weiToETHString converts wei to an 18-decimal ETH string.
func weiToETHString(wei *big.Int) string {
	if wei == nil {
		return "0.000000000000000000"
	}

	eth := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))

	return eth.Text('f', 18)
}
