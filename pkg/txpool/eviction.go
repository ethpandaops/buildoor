package txpool

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// pendingBlockRetentionSlots bounds how long an included block whose payload
// the EL does not know yet (unrevealed Gloas envelope) stays queued for
// eviction processing.
const pendingBlockRetentionSlots = 16

// evictionCallTimeout bounds each EL lookup of the eviction loop.
const evictionCallTimeout = 5 * time.Second

// run is the eviction loop: every new head (and every revealed payload) drops
// the pool transactions the chain now contains, prunes transactions whose
// nonce the chain has passed, and applies the TTL.
func (p *Pool) run() {
	defer p.wg.Done()

	headSub := p.clClient.Events().SubscribeHead()
	payloadSub := p.clClient.Events().SubscribePayloadAvailable()

	defer headSub.Unsubscribe()
	defer payloadSub.Unsubscribe()

	// Execution block hashes seen at the head whose transactions could not be
	// fetched yet, keyed to the slot they were seen at.
	pending := make(map[common.Hash]phase0.Slot, pendingBlockRetentionSlots)

	for {
		select {
		case <-p.ctx.Done():
			return

		case event := <-headSub.Channel():
			if hash, ok := p.executionHashOf(event.Block); ok {
				pending[hash] = event.Slot
			}

			p.processPending(pending, event.Slot)
			p.sweepTTL(event.Slot)

		case event := <-payloadSub.Channel():
			if hash, ok := p.executionHashOf(event.BlockRoot); ok {
				pending[hash] = event.Slot
			}

			p.processPending(pending, event.Slot)
		}
	}
}

// executionHashOf resolves the execution block hash a beacon block commits to.
func (p *Pool) executionHashOf(root phase0.Root) (common.Hash, bool) {
	headTracker := p.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return common.Hash{}, false
	}

	ctx, cancel := context.WithTimeout(p.ctx, evictionCallTimeout)
	defer cancel()

	info, err := headTracker.GetBlock(ctx, root)
	if err != nil || info == nil || info.ExecutionBlockHash == (phase0.Hash32{}) {
		return common.Hash{}, false
	}

	return common.Hash(info.ExecutionBlockHash), true
}

// processPending evicts the transactions of every pending block the EL knows,
// and forgets blocks that stayed unknown for too long (withheld payloads).
func (p *Pool) processPending(pending map[common.Hash]phase0.Slot, currentSlot phase0.Slot) {
	for hash, seenAt := range pending {
		if p.evictIncluded(hash) {
			delete(pending, hash)

			continue
		}

		if currentSlot > seenAt+pendingBlockRetentionSlots {
			delete(pending, hash)
		}
	}
}

// evictIncluded drops the pool transactions contained in the execution block
// and prunes the touched senders' passed nonces. Returns false while the EL
// does not know the block.
func (p *Pool) evictIncluded(blockHash common.Hash) bool {
	// Nothing to evict from an empty pool, but the block still counts as
	// processed.
	p.mu.RLock()
	empty := len(p.byHash) == 0
	p.mu.RUnlock()

	if empty {
		return true
	}

	ctx, cancel := context.WithTimeout(p.ctx, evictionCallTimeout)
	defer cancel()

	block, found, err := p.elClient.BlockTransactions(ctx, blockHash)
	if err != nil {
		p.log.WithError(err).WithField("block", blockHash.Hex()).Debug("Cannot fetch included block")

		return false
	}

	if !found {
		return false
	}

	ours := p.isBuiltBlock(blockHash)
	touched := make(map[common.Address]struct{}, 16)

	p.mu.Lock()

	for _, txHash := range block.TxHashes {
		tx, ok := p.byHash[txHash]
		if !ok {
			continue
		}

		touched[tx.Sender] = struct{}{}
		p.removeLocked(txHash)

		if ours {
			p.stats.EvictedIncludedByUs++
			p.evictedLocked("included_by_us")
		} else {
			p.stats.EvictedIncludedByOther++
			p.evictedLocked("included_by_other")
		}
	}

	p.mu.Unlock()

	if len(touched) > 0 {
		p.version.Add(1)
		p.pruneNonces(ctx, touched, blockHash, block.Number)
	}

	p.log.WithFields(logrus.Fields{
		"block":   blockHash.Hex(),
		"senders": len(touched),
		"ours":    ours,
	}).Debug("Processed included block")

	return true
}

// pruneNonces drops the senders' transactions whose nonce is at or below the
// account nonce at the given block.
func (p *Pool) pruneNonces(ctx context.Context, senders map[common.Address]struct{}, blockHash common.Hash, number uint64) {
	addrs := make([]common.Address, 0, len(senders))
	for sender := range senders {
		addrs = append(addrs, sender)
	}

	states, err := p.elClient.AccountStatesAt(ctx, addrs, blockHash, number)
	if err != nil {
		p.log.WithError(err).Debug("Cannot refresh sender nonces after inclusion")

		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for sender, state := range states {
		queue := p.bySender[sender]
		if queue == nil {
			continue
		}

		for _, tx := range append([]*PooledTx(nil), queue.txs...) {
			if tx.Tx.Nonce() < state.Nonce {
				p.removeLocked(tx.Hash)
				p.stats.EvictedNonceTooLow++
				p.evictedLocked("nonce_too_low")
			}
		}
	}
}

// sweepTTL drops transactions older than the configured slot TTL.
func (p *Pool) sweepTTL(currentSlot phase0.Slot) {
	ttl := p.cfg.TxPool.TxTTLSlots
	if ttl == 0 || uint64(currentSlot) <= ttl {
		return
	}

	cutoff := currentSlot - phase0.Slot(ttl)

	p.mu.Lock()
	defer p.mu.Unlock()

	dropped := 0

	for hash, tx := range p.byHash {
		if tx.ArrivedSlot < cutoff {
			p.removeLocked(hash)
			p.stats.EvictedTTL++
			p.evictedLocked("ttl")
			dropped++
		}
	}

	if dropped > 0 {
		p.version.Add(1)
		p.log.WithField("dropped", dropped).Debug("Expired pool transactions dropped")
	}
}

// Available reports whether the pool can operate at all (its EL and beacon
// dependencies are wired) and the reason when not.
func (p *Pool) Available() (bool, string) {
	if p.elClient == nil {
		return false, "no --el-rpc configured"
	}

	return true, ""
}
