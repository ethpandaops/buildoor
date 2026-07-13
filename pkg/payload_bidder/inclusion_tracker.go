package payload_bidder

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// maxWonBlocks caps the won-block store (in memory AND, via the flush batch's
// delete propagation, the kv_store namespace) — matching the old UI cap.
const maxWonBlocks = 1000

// PayloadIncludedEvent is fired when a beacon block committing to one of our
// payloads is observed at the head (consumed by the WebUI).
type PayloadIncludedEvent struct {
	Payload      *payload_builder.Payload
	BlockInfo    *beacon.BlockInfo
	BidValueGwei uint64
	WonBlock     *WonBlock // the recorded won-block entry for this inclusion
}

// InclusionTracker watches head events and detects inclusion of our payloads.
// It records the payment obligation, requests the payload reveal, records won
// blocks (it is the single owner of won-block records, covering both flows
// and all forks), and checks the follow-up block to detect orphaned
// (unrevealed) payloads. Shared by both the p2p and Builder API flows.
type InclusionTracker struct {
	clClient   *beacon.Client
	chainSvc   chain.Service
	builderSvc *payload_builder.Service // payload cache + inclusion stats
	revealSvc  *RevealService           // optional; nil pre-Gloas
	payments   *PaymentTracker          // optional; nil pre-Gloas
	stateDB    *db.Database             // optional (disabled no-op mode)

	// wonBlocks holds the won-block records keyed by slot; persisted into the
	// state-db's kv_store (WonBlocksNamespace) when a state-db is attached.
	wonBlocks *memstore.Store[phase0.Slot, *WonBlock]

	includedDispatch utils.Dispatcher[*PayloadIncludedEvent]

	// Follow-up orphan check state, owned by the run loop.
	lastIncludedSlot phase0.Slot
	lastIncludedHash phase0.Hash32

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewInclusionTracker creates a new inclusion tracker. revealSvc and payments
// may be nil (pre-Gloas networks); the tracker then only persists won blocks
// and fires inclusion events.
func NewInclusionTracker(
	clClient *beacon.Client,
	chainSvc chain.Service,
	builderSvc *payload_builder.Service,
	revealSvc *RevealService,
	payments *PaymentTracker,
	log logrus.FieldLogger,
) *InclusionTracker {
	return &InclusionTracker{
		clClient:   clClient,
		chainSvc:   chainSvc,
		builderSvc: builderSvc,
		revealSvc:  revealSvc,
		payments:   payments,
		wonBlocks:  memstore.New[phase0.Slot, *WonBlock](),
		log:        log.WithField("component", "inclusion-tracker"),
	}
}

// SetStateDB sets the optional state-db used to persist won blocks (attached
// to the won-block store on Start). When unset, won blocks are kept in memory
// only.
func (t *InclusionTracker) SetStateDB(stateDB *db.Database) {
	t.stateDB = stateDB
}

// SubscribeIncluded subscribes to payload inclusion events.
func (t *InclusionTracker) SubscribeIncluded(capacity int) *utils.Subscription[*PayloadIncludedEvent] {
	return t.includedDispatch.Subscribe(capacity, false)
}

// Start starts the inclusion tracker's main loop. When a state-db was set it
// first attaches the won-block store's persistence, rehydrating wins recorded
// in prior runs.
func (t *InclusionTracker) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	if t.stateDB != nil {
		t.wonBlocks.SetPersistence(t.ctx,
			db.NewKVPersistence(t.stateDB, WonBlocksNamespace, WonBlockCodec{}), t.log)
	}

	t.wg.Add(1)

	go t.run()

	t.log.Info("Inclusion tracker started")

	return nil
}

// Stop stops the inclusion tracker, waits for the main loop to exit, and
// flushes the won-block store. Must run before the state-db closes (run.go
// registers the tracker's Stop defer after the state-db's close defer, so
// LIFO ordering guarantees this).
func (t *InclusionTracker) Stop() {
	if t.cancel != nil {
		t.cancel()
	}

	t.wg.Wait()

	t.wonBlocks.Stop()

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

	// The head event is the authoritative source for the canonical block root.
	// In particular, go-eth2-client v0.1.6 hashes the nested Gloas bid's blob
	// commitments as a progressive list, while the devnet-6 compatibility mode
	// uses a bounded list. Recomputing the block root through the current library
	// schema can therefore make an otherwise valid devnet-6 envelope reference
	// look like an unknown block. Keep the decoded block for all other fields,
	// but preserve the root supplied by the beacon node.
	blockInfo.Root = event.Block

	t.processBlockInfo(blockInfo)
}

// processBlockInfo handles a resolved head block:
//  1. Check the follow-up for our previously included bid — if this block does
//     not build on a revealed payload of ours, the payload was orphaned.
//  2. Check if this block commits to one of our payloads.
func (t *InclusionTracker) processBlockInfo(blockInfo *beacon.BlockInfo) {
	t.checkFollowUpBlock(blockInfo)
	t.checkForOurPayload(blockInfo)
}

// checkFollowUpBlock checks if the previous slot's included bid was confirmed
// or orphaned. The RevealService stamps the shared payload on a successful
// reveal, so the payload's own reveal record is the source of truth.
func (t *InclusionTracker) checkFollowUpBlock(blockInfo *beacon.BlockInfo) {
	prevSlot := t.lastIncludedSlot
	prevHash := t.lastIncludedHash

	if prevSlot == 0 {
		return
	}

	// Only check blocks after the included slot.
	if blockInfo.Slot <= prevSlot {
		return
	}

	// Clear the tracking regardless — we only check once.
	t.lastIncludedSlot = 0
	t.lastIncludedHash = phase0.Hash32{}

	payload := t.builderSvc.GetPayloadCache().Get(prevSlot)
	if payload != nil && payload.Reveal() != nil {
		// We revealed — the payment was already deducted from the live balance
		// via PaymentTracker.MarkRevealed.
		t.log.WithFields(logrus.Fields{
			"slot":       prevSlot,
			"block_hash": fmt.Sprintf("%x", prevHash[:8]),
		}).Debug("Previous bid was revealed, payment already deducted")

		return
	}

	// We did NOT reveal — the payment stays pending for 2 epochs and is settled
	// by the beacon state's BuilderPendingPayments quorum logic.
	t.log.WithFields(logrus.Fields{
		"slot":       prevSlot,
		"block_hash": fmt.Sprintf("%x", prevHash[:8]),
	}).Warn("Previous bid was NOT revealed — payment pending for 2 epochs")
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

	// Record the won block (persisted into the kv_store when a state-db is
	// attached). The InclusionTracker is the single owner of won-block
	// records, for all forks and both flows.
	wonBlock := t.recordWonBlock(payload, blockInfo.ExecutionBlockHash)

	t.builderSvc.IncrementBlocksIncluded()

	t.includedDispatch.Fire(&PayloadIncludedEvent{
		Payload:      payload,
		BlockInfo:    blockInfo,
		BidValueGwei: bidValueGwei,
		WonBlock:     wonBlock,
	})

	// Track for the follow-up orphan check on the next head block.
	t.lastIncludedSlot = payload.Attributes.ProposalSlot
	t.lastIncludedHash = blockInfo.ExecutionBlockHash
}

// recordWonBlock records an included payload in the won-block store and
// returns the entry. The source is derived from the payload's bid records:
// any Builder-API bid marks the win as a Builder API delivery, otherwise it
// was won via p2p bidding.
func (t *InclusionTracker) recordWonBlock(
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

	wonBlock := &WonBlock{
		Source:          source,
		Slot:            uint64(payload.Attributes.ProposalSlot),
		BlockHash:       fmt.Sprintf("%#x", blockHash),
		NumTransactions: numTxs,
		NumBlobs:        numBlobs,
		ValueWei:        valueWei,
		ValueETH:        valueETH,
		Timestamp:       time.Now().UnixMilli(),
	}

	t.wonBlocks.Put(payload.Attributes.ProposalSlot, wonBlock)
	t.pruneWonBlocks()

	return wonBlock
}

// pruneWonBlocks drops the smallest slots down to maxWonBlocks entries. The
// deletions propagate to the kv_store namespace via the flush batch, so the
// durable history is capped at maxWonBlocks too (matching the old UI cap).
func (t *InclusionTracker) pruneWonBlocks() {
	excess := t.wonBlocks.Len() - maxWonBlocks
	if excess <= 0 {
		return
	}

	slots := make([]phase0.Slot, 0, t.wonBlocks.Len())
	for slot := range t.wonBlocks.Entries() {
		slots = append(slots, slot)
	}

	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	cutoff := slots[excess-1]

	t.wonBlocks.Prune(func(slot phase0.Slot) bool { return slot <= cutoff })
}

// GetWonBlocks returns a page of won-block records sorted by slot descending
// (newest first) plus the total record count.
func (t *InclusionTracker) GetWonBlocks(offset, limit int) ([]*WonBlock, int) {
	entries := t.wonBlocks.Entries()
	total := len(entries)

	if offset < 0 {
		offset = 0
	}

	if offset >= total || limit <= 0 {
		return []*WonBlock{}, total
	}

	sorted := make([]*WonBlock, 0, total)
	for _, wonBlock := range entries {
		sorted = append(sorted, wonBlock)
	}

	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slot > sorted[j].Slot })

	end := offset + limit
	if end > total {
		end = total
	}

	return sorted[offset:end], total
}

// weiToETHString converts wei to an 18-decimal ETH string.
func weiToETHString(wei *big.Int) string {
	if wei == nil {
		return "0.000000000000000000"
	}

	eth := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))

	return eth.Text('f', 18)
}
