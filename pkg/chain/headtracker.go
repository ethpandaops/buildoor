package chain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// PayloadStatus is the reveal status of a Gloas block's committed execution
// payload. Pre-Gloas blocks embed their payload and are always Revealed.
type PayloadStatus string

const (
	// PayloadStatusPending: no reveal evidence yet and the reveal deadline has
	// not passed.
	PayloadStatusPending PayloadStatus = "pending"
	// PayloadStatusRevealed: the payload was seen (payload-available event or
	// a child block built on it).
	PayloadStatusRevealed PayloadStatus = "revealed"
	// PayloadStatusEmpty: the payload is treated as withheld — either a child
	// block built past it, or the reveal deadline passed without evidence
	// (provisional until contrary evidence arrives).
	PayloadStatusEmpty PayloadStatus = "empty"
)

// CandidateKey identifies a build-parent candidate for a slot: which beacon
// block and which execution payload the build extends. "parent" and
// "grandparent" are positions on the canonical head chain at resolution time,
// so after missed slots they name the last existing blocks, not fixed slot
// offsets.
type CandidateKey string

const (
	// CandidateParentFull: build on the head block and its committed payload.
	CandidateParentFull CandidateKey = "parent_full"
	// CandidateParentEmpty: build on the head block but on the execution
	// payload it built upon (the head block's own payload treated as
	// withheld). Gloas+ only.
	CandidateParentEmpty CandidateKey = "parent_empty"
	// CandidateGrandparentFull: build on the head block's parent and its
	// committed payload (a deliberate reorg of the head block).
	CandidateGrandparentFull CandidateKey = "grandparent_full"
	// CandidateGrandparentEmpty: build on the head block's parent but on the
	// payload it built upon (reorg of the head block combined with the
	// grandparent's payload treated as withheld). Gloas+ only.
	CandidateGrandparentEmpty CandidateKey = "grandparent_empty"
)

// AllCandidateKeys lists every candidate key in canonical priority order.
var AllCandidateKeys = []CandidateKey{
	CandidateParentFull,
	CandidateParentEmpty,
	CandidateGrandparentFull,
	CandidateGrandparentEmpty,
}

// IsValidCandidateKey reports whether the given string names a candidate key.
func IsValidCandidateKey(key string) bool {
	switch CandidateKey(key) {
	case CandidateParentFull, CandidateParentEmpty,
		CandidateGrandparentFull, CandidateGrandparentEmpty:
		return true
	default:
		return false
	}
}

// CandidateParent is one resolved build-parent candidate for a slot.
type CandidateParent struct {
	Key             CandidateKey
	ParentBlockRoot phase0.Root
	ParentSlot      phase0.Slot
	ParentBlockHash phase0.Hash32
	// ELParentGasLimit is the gas limit of the execution block identified by
	// ParentBlockHash (0 = unknown). Committed gas limits come from the bid
	// (Gloas) or the embedded payload (pre-Gloas).
	ELParentGasLimit uint64
	// ELParentNumber is the block number of the execution block identified by
	// ParentBlockHash (0 = unknown; Gloas blocks require the revealed
	// envelope to learn it).
	ELParentNumber uint64
	// ParentPayloadStatus is the reveal status of the beacon parent block's
	// own committed payload. Full candidates are only viable when it is (or
	// becomes) revealed; empty candidates when it stays withheld.
	ParentPayloadStatus PayloadStatus
}

// HeadChangeEvent is fired for every accepted head switch. ReorgDepth is 0 for
// a normal chain extension; for a reorg it is the number of slots between the
// old head and the common ancestor. CommonAncestor is zero when the fork point
// is deeper than the scan window.
type HeadChangeEvent struct {
	Old            *beacon.BlockInfo // nil on the first observed head
	New            *beacon.BlockInfo
	ReorgDepth     uint64
	CommonAncestor phase0.Root
}

const (
	// headBlockRetentionSlots is how many slots of ancestry blocks (and their
	// payload evidence) the tracker retains.
	headBlockRetentionSlots = 64
	// reorgScanDepthSlots bounds the ancestor walk used to locate the common
	// ancestor of the old and new head on a reorg.
	reorgScanDepthSlots = 16
	// headFetchTimeout bounds individual beacon-API block/envelope fetches.
	headFetchTimeout = 5 * time.Second
)

// elBlockMeta is envelope-derived metadata of a revealed execution block.
type elBlockMeta struct {
	number   uint64
	gasLimit uint64
}

// HeadTracker maintains buildoor's own view of the canonical chain: the
// current head, a bounded block-by-root ancestry cache, and per-block payload
// reveal status (Gloas). It is the validation oracle for payload-attributes
// sanitization and the source of build-parent candidates.
type HeadTracker struct {
	clClient  *beacon.Client
	chainSpec *ChainSpec
	genesis   *beacon.Genesis
	log       logrus.FieldLogger

	mu              sync.RWMutex
	head            *beacon.BlockInfo
	finality        *beacon.FinalityInfo
	finalityHead    phase0.Root
	blocks          map[phase0.Root]*beacon.BlockInfo
	payloadRevealed map[phase0.Root]bool
	payloadEmpty    map[phase0.Root]bool
	elMeta          map[phase0.Hash32]*elBlockMeta

	headChangeDispatcher *utils.Dispatcher[*HeadChangeEvent]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHeadTracker creates a new head tracker.
func NewHeadTracker(
	clClient *beacon.Client,
	chainSpec *ChainSpec,
	genesis *beacon.Genesis,
	log logrus.FieldLogger,
) *HeadTracker {
	return &HeadTracker{
		clClient:             clClient,
		chainSpec:            chainSpec,
		genesis:              genesis,
		log:                  log.WithField("component", "head-tracker"),
		blocks:               make(map[phase0.Root]*beacon.BlockInfo, headBlockRetentionSlots),
		payloadRevealed:      make(map[phase0.Root]bool, headBlockRetentionSlots),
		payloadEmpty:         make(map[phase0.Root]bool, headBlockRetentionSlots),
		elMeta:               make(map[phase0.Hash32]*elBlockMeta, headBlockRetentionSlots),
		headChangeDispatcher: &utils.Dispatcher[*HeadChangeEvent]{},
	}
}

// Start starts the head tracker's event loop.
func (h *HeadTracker) Start(ctx context.Context) {
	h.ctx, h.cancel = context.WithCancel(ctx)

	h.wg.Add(1)

	go h.run()
}

// Stop stops the head tracker and waits for its loop to exit.
func (h *HeadTracker) Stop() {
	if h.cancel != nil {
		h.cancel()
	}

	h.wg.Wait()
}

// SubscribeHeadChanges returns a subscription for head switch events
// (including reorgs).
func (h *HeadTracker) SubscribeHeadChanges() *utils.Subscription[*HeadChangeEvent] {
	return h.headChangeDispatcher.Subscribe(16, false)
}

// run processes head, payload-available and chain_reorg events.
func (h *HeadTracker) run() {
	defer h.wg.Done()

	headSub := h.clClient.Events().SubscribeHead()
	payloadSub := h.clClient.Events().SubscribePayloadAvailable()
	reorgSub := h.clClient.Events().SubscribeChainReorgs()

	defer headSub.Unsubscribe()
	defer payloadSub.Unsubscribe()
	defer reorgSub.Unsubscribe()

	for {
		select {
		case <-h.ctx.Done():
			return
		case event := <-headSub.Channel():
			h.processHead(event)
		case event := <-payloadSub.Channel():
			h.markPayloadRevealed(event.BlockRoot)
			h.prefetchELMeta(event.BlockRoot)
		case event := <-reorgSub.Channel():
			h.log.WithFields(logrus.Fields{
				"slot":     event.Slot,
				"depth":    event.Depth,
				"old_head": fmt.Sprintf("%#x", event.OldHeadBlock[:8]),
				"new_head": fmt.Sprintf("%#x", event.NewHeadBlock[:8]),
			}).Info("Beacon node reported chain reorg")
		}
	}
}

// FinalityInfo returns the finality checkpoint execution hashes cached for
// the current head (nil until the first refresh completes). Refreshed once
// per head change, so build paths never pay the beacon-API round trips.
func (h *HeadTracker) FinalityInfo() *beacon.FinalityInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.finality
}

// refreshFinality re-resolves the finality info for the given head, unless it
// is already cached for it. Runs in the tracker's event loop, off every build
// hot path.
func (h *HeadTracker) refreshFinality(headRoot phase0.Root) {
	if h.clClient == nil {
		return
	}

	h.mu.RLock()
	upToDate := h.finalityHead == headRoot && h.finality != nil
	h.mu.RUnlock()

	if upToDate {
		return
	}

	ctx, cancel := context.WithTimeout(h.ctx, headFetchTimeout)
	defer cancel()

	info, err := h.clClient.GetFinalityInfo(ctx)
	if err != nil {
		h.log.WithError(err).Debug("Failed to refresh finality info")
		return
	}

	h.mu.Lock()
	h.finality = info
	h.finalityHead = headRoot
	h.mu.Unlock()
}

// CurrentHead returns the most recently observed head block (nil until the
// first head event resolves).
func (h *HeadTracker) CurrentHead() *beacon.BlockInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.head
}

// IsCanonical reports whether the given root is on the current head's
// ancestry within the retention window.
func (h *HeadTracker) IsCanonical(ctx context.Context, root phase0.Root) bool {
	head := h.CurrentHead()
	if head == nil {
		return false
	}

	cursor := head

	for {
		if cursor.Root == root {
			return true
		}

		if cursor.Slot == 0 || head.Slot-cursor.Slot >= headBlockRetentionSlots {
			return false
		}

		parent, err := h.GetBlock(ctx, cursor.ParentRoot)
		if err != nil {
			return false
		}

		cursor = parent
	}
}

// PrimeBlock contributes an already-resolved block to the shared ancestry
// cache (also derives payload-status evidence for its parent).
func (h *HeadTracker) PrimeBlock(info *beacon.BlockInfo) {
	h.storeBlock(info)
}

// PrimeHead seeds the tracker with an already-resolved head block (cached and
// adopted as current head unless an equal-or-newer head is known). Live head
// events take over from the first processed event.
func (h *HeadTracker) PrimeHead(info *beacon.BlockInfo) {
	h.storeBlock(info)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.head == nil || info.Slot >= h.head.Slot {
		h.head = info
	}
}

// GetBlock resolves a block by root through the shared ancestry cache,
// fetching from the beacon node on a miss.
func (h *HeadTracker) GetBlock(ctx context.Context, root phase0.Root) (*beacon.BlockInfo, error) {
	h.mu.RLock()
	info, ok := h.blocks[root]
	h.mu.RUnlock()

	if ok {
		return info, nil
	}

	if h.clClient == nil {
		return nil, fmt.Errorf("block %#x not cached and no beacon client available", root)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, headFetchTimeout)
	defer cancel()

	info, err := h.clClient.GetBlockInfo(fetchCtx, fmt.Sprintf("%#x", root))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve block %#x: %w", root, err)
	}

	h.storeBlock(info)

	return info, nil
}

// storeBlock caches a block and derives payload-status evidence for its
// parent from the execution parent hash the block committed to.
func (h *HeadTracker) storeBlock(info *beacon.BlockInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.blocks[info.Root]; exists {
		return
	}

	h.blocks[info.Root] = info

	if !h.isGloasSlot(info.Slot) {
		return
	}

	parent, ok := h.blocks[info.ParentRoot]
	if !ok || !h.isGloasSlot(parent.Slot) {
		return
	}

	// The block's committed execution parent (bid parent_block_hash) proves
	// whether its beacon parent's payload made it onto the EL chain: matching
	// the parent's committed payload hash means the parent was full, matching
	// the parent's own execution parent means the chain built past a withheld
	// payload.
	switch info.FinalitySafeExecutionBlockHash {
	case parent.ExecutionBlockHash:
		h.payloadRevealed[parent.Root] = true
	case parent.FinalitySafeExecutionBlockHash:
		h.payloadEmpty[parent.Root] = true
	}
}

// markPayloadRevealed records an execution_payload_available event.
func (h *HeadTracker) markPayloadRevealed(root phase0.Root) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.payloadRevealed[root] = true
	delete(h.payloadEmpty, root)
}

// GetPayloadStatus returns the reveal status of the block's committed payload.
// Unknown blocks report Pending. Pre-Gloas blocks are always Revealed (the
// payload is embedded in the block).
func (h *HeadTracker) GetPayloadStatus(root phase0.Root) PayloadStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	info, ok := h.blocks[root]
	if !ok {
		return PayloadStatusPending
	}

	return h.payloadStatusLocked(info)
}

// payloadStatusLocked derives the payload status for a cached block. Callers
// must hold at least a read lock.
func (h *HeadTracker) payloadStatusLocked(info *beacon.BlockInfo) PayloadStatus {
	if !h.isGloasSlot(info.Slot) {
		return PayloadStatusRevealed
	}

	if h.payloadRevealed[info.Root] {
		return PayloadStatusRevealed
	}

	if h.payloadEmpty[info.Root] {
		return PayloadStatusEmpty
	}

	// Past the reveal deadline without any evidence the payload is treated as
	// withheld; a late reveal or child-block evidence still flips it back.
	if time.Now().After(h.payloadDueTime(info.Slot)) {
		return PayloadStatusEmpty
	}

	return PayloadStatusPending
}

// payloadDueTime returns the wall-clock payload reveal deadline of a slot.
func (h *HeadTracker) payloadDueTime(slot phase0.Slot) time.Time {
	slotStart := h.genesis.GenesisTime.Add(time.Duration(uint64(slot)) * h.chainSpec.SecondsPerSlot)
	dueOffset := h.chainSpec.SecondsPerSlot * time.Duration(h.chainSpec.PayloadDueBps) / 10000

	return slotStart.Add(dueOffset)
}

// isGloasSlot reports whether the Gloas fork is active at the given slot.
func (h *HeadTracker) isGloasSlot(slot phase0.Slot) bool {
	epoch := phase0.Epoch(uint64(slot) / h.chainSpec.SlotsPerEpoch)

	return h.chainSpec.IsForkActive(version.DataVersionGloas, epoch)
}

// processHead resolves a head event's block, detects reorgs against the
// previous head and fires a HeadChangeEvent.
func (h *HeadTracker) processHead(event *beacon.HeadEvent) {
	newHead, err := h.GetBlock(h.ctx, event.Block)
	if err != nil {
		h.log.WithError(err).WithField("slot", event.Slot).Debug("Failed to resolve head block")
		return
	}

	h.mu.Lock()
	oldHead := h.head

	if oldHead != nil && oldHead.Root == newHead.Root {
		h.mu.Unlock()
		return
	}

	h.head = newHead
	h.mu.Unlock()

	change := &HeadChangeEvent{
		Old: oldHead,
		New: newHead,
	}

	if oldHead != nil && newHead.ParentRoot != oldHead.Root {
		change.ReorgDepth, change.CommonAncestor = h.resolveReorg(oldHead, newHead)

		h.log.WithFields(logrus.Fields{
			"old_head": fmt.Sprintf("%#x", oldHead.Root[:8]),
			"new_head": fmt.Sprintf("%#x", newHead.Root[:8]),
			"old_slot": oldHead.Slot,
			"new_slot": newHead.Slot,
			"depth":    change.ReorgDepth,
		}).Info("Head switched to a non-child block (reorg)")
	}

	h.prune(newHead.Slot)
	h.refreshFinality(newHead.Root)

	h.headChangeDispatcher.Fire(change)
}

// resolveReorg locates the common ancestor of the old and new head within the
// scan window. Returns the reorg depth (slots the old chain lost) and the
// ancestor root; depth falls back to the scan window and the root stays zero
// when the fork point is deeper.
func (h *HeadTracker) resolveReorg(oldHead, newHead *beacon.BlockInfo) (uint64, phase0.Root) {
	oldChain := make(map[phase0.Root]phase0.Slot, reorgScanDepthSlots)

	cursor := oldHead
	for range reorgScanDepthSlots {
		oldChain[cursor.Root] = cursor.Slot

		if cursor.Slot == 0 {
			break
		}

		parent, err := h.GetBlock(h.ctx, cursor.ParentRoot)
		if err != nil {
			break
		}

		cursor = parent
	}

	cursor = newHead
	for range reorgScanDepthSlots {
		if ancestorSlot, ok := oldChain[cursor.Root]; ok {
			return uint64(oldHead.Slot - ancestorSlot), cursor.Root
		}

		if cursor.Slot == 0 {
			break
		}

		parent, err := h.GetBlock(h.ctx, cursor.ParentRoot)
		if err != nil {
			break
		}

		cursor = parent
	}

	return reorgScanDepthSlots, phase0.Root{}
}

// prune drops blocks and payload evidence outside the retention window.
func (h *HeadTracker) prune(headSlot phase0.Slot) {
	if headSlot <= headBlockRetentionSlots {
		return
	}

	minSlot := headSlot - headBlockRetentionSlots

	h.mu.Lock()
	defer h.mu.Unlock()

	for root, info := range h.blocks {
		if info.Slot >= minSlot {
			continue
		}

		delete(h.blocks, root)
		delete(h.payloadRevealed, root)
		delete(h.payloadEmpty, root)
		delete(h.elMeta, info.ExecutionBlockHash)
		delete(h.elMeta, info.FinalitySafeExecutionBlockHash)
	}
}

// ResolveCandidates derives the build-parent candidates for the given slot
// from the current head chain. The head must be older than the target slot.
// Empty variants are only produced under Gloas; a variant whose parent tuple
// duplicates another is dropped.
func (h *HeadTracker) ResolveCandidates(ctx context.Context, slot phase0.Slot) ([]*CandidateParent, error) {
	parent := h.CurrentHead()
	if parent == nil {
		return nil, fmt.Errorf("no head observed yet")
	}

	if parent.Slot >= slot {
		return nil, fmt.Errorf("head slot %d is not below target slot %d", parent.Slot, slot)
	}

	candidates := make([]*CandidateParent, 0, len(AllCandidateKeys))
	gloas := h.isGloasSlot(parent.Slot)

	parentStatus := h.GetPayloadStatus(parent.Root)

	candidates = append(candidates, h.buildCandidate(
		ctx, CandidateParentFull, parent, parent.ExecutionBlockHash, parentStatus))

	if gloas && parent.FinalitySafeExecutionBlockHash != parent.ExecutionBlockHash {
		candidates = append(candidates, h.buildCandidate(
			ctx, CandidateParentEmpty, parent, parent.FinalitySafeExecutionBlockHash, parentStatus))
	}

	if parent.Slot > 0 {
		grandparent, err := h.GetBlock(ctx, parent.ParentRoot)
		if err != nil {
			h.log.WithError(err).Debug("Failed to resolve grandparent block for candidates")
		} else {
			gpStatus := h.GetPayloadStatus(grandparent.Root)

			candidates = append(candidates, h.buildCandidate(
				ctx, CandidateGrandparentFull, grandparent, grandparent.ExecutionBlockHash, gpStatus))

			if h.isGloasSlot(grandparent.Slot) &&
				grandparent.FinalitySafeExecutionBlockHash != grandparent.ExecutionBlockHash {
				candidates = append(candidates, h.buildCandidate(
					ctx, CandidateGrandparentEmpty, grandparent,
					grandparent.FinalitySafeExecutionBlockHash, gpStatus))
			}
		}
	}

	return candidates, nil
}

// buildCandidate assembles one candidate tuple, resolving the EL parent's gas
// limit and block number on a best-effort basis.
func (h *HeadTracker) buildCandidate(
	ctx context.Context,
	key CandidateKey,
	parent *beacon.BlockInfo,
	elParentHash phase0.Hash32,
	parentStatus PayloadStatus,
) *CandidateParent {
	candidate := &CandidateParent{
		Key:                 key,
		ParentBlockRoot:     parent.Root,
		ParentSlot:          parent.Slot,
		ParentBlockHash:     elParentHash,
		ParentPayloadStatus: parentStatus,
	}

	// Cache-only: candidate resolution runs on the build hot path and must
	// never block on a beacon-API round trip.
	committer := h.findCommitterOfExecHash(ctx, parent, elParentHash)
	if committer != nil {
		candidate.ELParentGasLimit = committer.GasLimit
		candidate.ELParentNumber = committer.ExecutionBlockNumber
	}

	if candidate.ELParentNumber == 0 {
		h.mu.RLock()
		meta := h.elMeta[elParentHash]
		h.mu.RUnlock()

		if meta != nil {
			candidate.ELParentNumber = meta.number

			if candidate.ELParentGasLimit == 0 {
				candidate.ELParentGasLimit = meta.gasLimit
			}
		}
	}

	return candidate
}

// LookupELParentMeta returns the block number and gas limit of the execution
// block identified by execHash from already-known data only: the committing
// beacon block's own fields and the envelope-metadata cache. It never
// performs a beacon-API fetch, so build hot paths never block on one
// (payload-available events prefetch the metadata in the background).
func (h *HeadTracker) LookupELParentMeta(
	ctx context.Context, fromRoot phase0.Root, execHash phase0.Hash32,
) (number, gasLimit uint64) {
	h.mu.RLock()
	from, cached := h.blocks[fromRoot]
	meta := h.elMeta[execHash]
	h.mu.RUnlock()

	if meta != nil {
		return meta.number, meta.gasLimit
	}

	if !cached {
		return 0, 0
	}

	if committer := h.findCommitterOfExecHash(ctx, from, execHash); committer != nil {
		return committer.ExecutionBlockNumber, committer.GasLimit
	}

	return 0, 0
}

// prefetchELMeta resolves and caches the envelope metadata of a revealed
// payload in the background, so later build passes find it cached instead of
// blocking on a beacon-API fetch.
func (h *HeadTracker) prefetchELMeta(root phase0.Root) {
	if h.clClient == nil {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(h.ctx, headFetchTimeout)
		defer cancel()

		block, err := h.GetBlock(ctx, root)
		if err != nil {
			return
		}

		h.resolveELMeta(ctx, block, block.ExecutionBlockHash)
	}()
}

// ResolveELParentMeta resolves the block number and gas limit of the
// execution block identified by execHash, walking the beacon ancestry from
// fromRoot to find the block that committed it (best-effort; zeros when
// unknown). May fetch from the beacon node — do not call from build hot
// paths; use LookupELParentMeta there.
func (h *HeadTracker) ResolveELParentMeta(
	ctx context.Context, fromRoot phase0.Root, execHash phase0.Hash32,
) (number, gasLimit uint64) {
	from, err := h.GetBlock(ctx, fromRoot)
	if err != nil {
		return 0, 0
	}

	committer := h.findCommitterOfExecHash(ctx, from, execHash)
	if committer == nil {
		return 0, 0
	}

	number = committer.ExecutionBlockNumber
	gasLimit = committer.GasLimit

	if number == 0 {
		if meta := h.resolveELMeta(ctx, committer, execHash); meta != nil {
			number = meta.number

			if gasLimit == 0 {
				gasLimit = meta.gasLimit
			}
		}
	}

	return number, gasLimit
}

// findCommitterOfExecHash walks the ancestry from the given block looking for
// the beacon block that committed the execution block with the given hash.
func (h *HeadTracker) findCommitterOfExecHash(
	ctx context.Context, from *beacon.BlockInfo, execHash phase0.Hash32,
) *beacon.BlockInfo {
	cursor := from

	for range reorgScanDepthSlots {
		if cursor.ExecutionBlockHash == execHash {
			return cursor
		}

		if cursor.Slot == 0 {
			return nil
		}

		parent, err := h.GetBlock(ctx, cursor.ParentRoot)
		if err != nil {
			return nil
		}

		cursor = parent
	}

	return nil
}

// resolveELMeta fetches the revealed envelope of the block that committed the
// given execution block hash to learn the EL block number (and gas limit).
// Returns nil when the envelope is unavailable (e.g. withheld payload).
func (h *HeadTracker) resolveELMeta(
	ctx context.Context, committer *beacon.BlockInfo, execHash phase0.Hash32,
) *elBlockMeta {
	h.mu.RLock()
	meta, ok := h.elMeta[execHash]
	h.mu.RUnlock()

	if ok {
		return meta
	}

	if h.clClient == nil {
		return nil
	}

	fetchCtx, cancel := context.WithTimeout(ctx, headFetchTimeout)
	defer cancel()

	envelope, err := h.clClient.GetExecutionPayloadEnvelope(fetchCtx, fmt.Sprintf("%#x", committer.Root))
	if err != nil {
		h.log.WithError(err).WithField("root", fmt.Sprintf("%#x", committer.Root)).
			Debug("Failed to fetch payload envelope for EL metadata")
		return nil
	}

	payload := envelope.Message.Payload
	if payload.BlockHash != execHash {
		return nil
	}

	meta = &elBlockMeta{
		number:   payload.BlockNumber,
		gasLimit: payload.GasLimit,
	}

	h.mu.Lock()
	h.elMeta[execHash] = meta
	h.mu.Unlock()

	return meta
}
