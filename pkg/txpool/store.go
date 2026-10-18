package txpool

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// Store persists the queued transactions so a restart does not lose them:
// with few proposals a private transaction can wait for hours. Writes must
// not block, the pool calls them under its lock.
type Store interface {
	// Put records an admitted transaction, Delete a removed one, Clear all.
	Put(tx *PooledTx)
	Delete(hash common.Hash)
	Clear()
	// Load hands every stored transaction to fn (startup).
	Load(fn func(*StoredTx)) error
	Close() error
}

// StoredTx is one persisted transaction with its admission metadata.
type StoredTx struct {
	Tx          *types.Transaction
	Submitter   string
	Arrived     time.Time
	ArrivedSlot phase0.Slot
	Seq         uint64
}

// storeRecord is the RLP layout of a stored transaction.
type storeRecord struct {
	Tx          []byte // network encoding (blob sidecar included)
	Submitter   string
	ArrivedMs   uint64
	ArrivedSlot uint64
	Seq         uint64
}

const (
	// storeFlushInterval is how often pending writes reach the store.
	storeFlushInterval = 100 * time.Millisecond
	// storeKeyPrefix namespaces the transaction records.
	storeKeyPrefix = "tx/"
)

// PebbleStore is the Store on pebble, go-ethereum's database engine: an LSM
// tree suits the pool's churn (every slot admits and evicts), and blob
// transactions (up to ~1 MB with cell proofs) are ordinary values. Writes are
// buffered and coalesced per transaction, then flushed in one batch every
// storeFlushInterval without fsync: a crash loses at most that window.
type PebbleStore struct {
	db  *pebble.DB
	log logrus.FieldLogger

	mu       sync.Mutex
	pending  map[common.Hash]*PooledTx // nil = delete
	clearAll bool

	stop chan struct{}
	done chan struct{}
}

var _ Store = (*PebbleStore)(nil)

// OpenPebbleStore opens (or creates) the store in dir and starts its writer.
func OpenPebbleStore(dir string, log logrus.FieldLogger) (*PebbleStore, error) {
	db, err := pebble.Open(dir, &pebble.Options{Logger: pebbleLogger{log}})
	if err != nil {
		return nil, fmt.Errorf("open transaction pool store %s: %w", dir, err)
	}

	s := &PebbleStore{
		db:      db,
		log:     log.WithField("component", "txpool-store"),
		pending: make(map[common.Hash]*PooledTx, 256),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}

	go s.run()

	return s, nil
}

func (s *PebbleStore) Put(tx *PooledTx) {
	s.mu.Lock()
	s.pending[tx.Hash] = tx
	s.mu.Unlock()
}

func (s *PebbleStore) Delete(hash common.Hash) {
	s.mu.Lock()
	s.pending[hash] = nil
	s.mu.Unlock()
}

func (s *PebbleStore) Clear() {
	s.mu.Lock()
	s.clearAll = true
	s.pending = make(map[common.Hash]*PooledTx, 256)
	s.mu.Unlock()
}

// Load decodes every stored transaction; undecodable records are dropped.
func (s *PebbleStore) Load(fn func(*StoredTx)) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(storeKeyPrefix),
		UpperBound: storeKeyEnd(),
	})
	if err != nil {
		return fmt.Errorf("iterate transaction pool store: %w", err)
	}

	defer iter.Close()

	var broken []common.Hash

	for iter.First(); iter.Valid(); iter.Next() {
		stored, err := decodeStoreRecord(iter.Value())
		if err != nil {
			broken = append(broken, common.BytesToHash(iter.Key()[len(storeKeyPrefix):]))

			continue
		}

		fn(stored)
	}

	for _, hash := range broken {
		s.Delete(hash)
	}

	if len(broken) > 0 {
		s.log.WithField("records", len(broken)).Warn("Dropped undecodable transaction pool store records")
	}

	return iter.Error()
}

// Close flushes the pending writes (synced) and closes the store.
func (s *PebbleStore) Close() error {
	close(s.stop)
	<-s.done

	return errors.Join(s.flush(pebble.Sync), s.db.Close())
}

func (s *PebbleStore) run() {
	defer close(s.done)

	ticker := time.NewTicker(storeFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			if err := s.flush(pebble.NoSync); err != nil {
				s.log.WithError(err).Warn("Writing the transaction pool store failed")
			}
		}
	}
}

// flush writes the coalesced pending changes in one batch.
func (s *PebbleStore) flush(opts *pebble.WriteOptions) error {
	s.mu.Lock()
	pending, clearAll := s.pending, s.clearAll
	s.pending = make(map[common.Hash]*PooledTx, 256)
	s.clearAll = false
	s.mu.Unlock()

	if len(pending) == 0 && !clearAll {
		return nil
	}

	batch := s.db.NewBatch()
	defer batch.Close()

	if clearAll {
		if err := batch.DeleteRange([]byte(storeKeyPrefix), storeKeyEnd(), nil); err != nil {
			return err
		}
	}

	for hash, tx := range pending {
		if tx == nil {
			if err := batch.Delete(storeKey(hash), nil); err != nil {
				return err
			}

			continue
		}

		value, err := encodeStoreRecord(tx)
		if err != nil {
			s.log.WithError(err).WithField("tx", hash.Hex()).Warn("Cannot encode a pool transaction for the store")

			continue
		}

		if err := batch.Set(storeKey(hash), value, nil); err != nil {
			return err
		}
	}

	return batch.Commit(opts)
}

func storeKey(hash common.Hash) []byte {
	return append([]byte(storeKeyPrefix), hash[:]...)
}

func storeKeyEnd() []byte {
	end := []byte(storeKeyPrefix)
	end[len(end)-1]++

	return end
}

func encodeStoreRecord(tx *PooledTx) ([]byte, error) {
	raw, err := tx.Tx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return rlp.EncodeToBytes(&storeRecord{
		Tx:          raw,
		Submitter:   tx.Submitter,
		ArrivedMs:   uint64(max(tx.Arrived.UnixMilli(), 0)),
		ArrivedSlot: uint64(tx.ArrivedSlot),
		Seq:         tx.Seq,
	})
}

func decodeStoreRecord(value []byte) (*StoredTx, error) {
	var record storeRecord
	if err := rlp.DecodeBytes(value, &record); err != nil {
		return nil, err
	}

	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(record.Tx); err != nil {
		return nil, err
	}

	return &StoredTx{
		Tx:          tx,
		Submitter:   record.Submitter,
		Arrived:     time.UnixMilli(int64(record.ArrivedMs)), //nolint:gosec // written from a non-negative int64
		ArrivedSlot: phase0.Slot(record.ArrivedSlot),
		Seq:         record.Seq,
	}, nil
}

// pebbleLogger routes pebble's own messages to logrus at debug level.
type pebbleLogger struct {
	log logrus.FieldLogger
}

func (l pebbleLogger) Infof(format string, args ...any)  { l.log.Debugf(format, args...) }
func (l pebbleLogger) Errorf(format string, args ...any) { l.log.Warnf(format, args...) }
func (l pebbleLogger) Fatalf(format string, args ...any) { l.log.Fatalf(format, args...) }
