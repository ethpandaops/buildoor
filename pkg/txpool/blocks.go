package txpool

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/common"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// includedBlockCacheSize is how many recent blocks keep their transactions.
const includedBlockCacheSize = 32

// blockCache keeps the transactions of the recently included blocks: every
// head's block is read once and shared by the pool's eviction, the EL
// mempool mirror and the readers of the block's transactions.
type blockCache struct {
	mu     sync.Mutex
	blocks map[common.Hash]*execution.BlockTxList
	order  []common.Hash
	// gasUsed are the transactions' gas used of the cached blocks that a
	// reader asked for (receipts are read on demand, not per head).
	gasUsed map[common.Hash][]uint64
}

// blockGasUser reads a block's per-transaction gas used (the EL client;
// test fakes may not).
type blockGasUser interface {
	BlockGasUsed(ctx context.Context, blockHash common.Hash) ([]uint64, error)
}

// IncludedBlockGasUsed returns the gas each transaction of a recently
// included block used, in block order (nil when the EL client cannot tell).
func (p *Pool) IncludedBlockGasUsed(ctx context.Context, blockHash common.Hash) ([]uint64, error) {
	reader, ok := p.elClient.(blockGasUser)
	if !ok {
		return nil, nil
	}

	c := &p.blocks

	c.mu.Lock()
	defer c.mu.Unlock()

	if used, ok := c.gasUsed[blockHash]; ok {
		return used, nil
	}

	used, err := reader.BlockGasUsed(ctx, blockHash)
	if err != nil {
		return nil, err
	}

	// Kept only while the block itself is cached.
	if _, cached := c.blocks[blockHash]; cached {
		if c.gasUsed == nil {
			c.gasUsed = make(map[common.Hash][]uint64, includedBlockCacheSize)
		}

		c.gasUsed[blockHash] = used
	}

	return used, nil
}

// IncludedBlock returns the transactions of the execution block with the
// given hash, from the cache or the EL (found false while the EL does not
// know it yet, which is never cached).
func (p *Pool) IncludedBlock(ctx context.Context, blockHash common.Hash) (*execution.BlockTxList, bool, error) {
	c := &p.blocks

	// One read per block: concurrent readers of the same block wait for it.
	c.mu.Lock()
	defer c.mu.Unlock()

	if block := c.blocks[blockHash]; block != nil {
		return block, true, nil
	}

	block, found, err := p.elClient.BlockTransactions(ctx, blockHash)
	if err != nil || !found {
		return nil, found, err
	}

	if c.blocks == nil {
		c.blocks = make(map[common.Hash]*execution.BlockTxList, includedBlockCacheSize)
	}

	c.blocks[blockHash] = block
	c.order = append(c.order, blockHash)

	if len(c.order) > includedBlockCacheSize {
		delete(c.blocks, c.order[0])
		delete(c.gasUsed, c.order[0])
		c.order = c.order[1:]
	}

	return block, true, nil
}
