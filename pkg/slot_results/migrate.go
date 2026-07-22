package slot_results

import (
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
)

// migrateWonBlocks moves the legacy won_blocks kv_store namespace into the
// slot_results namespace: each old won-block becomes (or merges into) a slot
// result's Inclusion. Idempotent and crash-safe across its two batches — the
// new rows commit first, the old namespace rows are deleted only afterwards,
// and a re-run never overwrites an existing result's data. No-op when the
// database is disabled or the namespace is empty.
func migrateWonBlocks(stateDB *db.Database, slotsPerEpoch uint64, log logrus.FieldLogger) {
	if !stateDB.Enabled() || slotsPerEpoch == 0 {
		return
	}

	oldPersistence := db.NewKVPersistence(stateDB, payload_bidder.WonBlocksNamespace,
		payload_bidder.WonBlockCodec{})

	wonBlocks, err := oldPersistence.Load()
	if err != nil {
		log.WithError(err).Warn("Failed to load legacy won_blocks namespace for migration")
		return
	}

	if len(wonBlocks) == 0 {
		return
	}

	resultsPersistence := db.NewKVPersistence(stateDB, Namespace, ResultCodec{})

	existing, err := resultsPersistence.Load()
	if err != nil {
		log.WithError(err).Warn("Failed to load slot results for won_blocks migration")
		return
	}

	upserts := make(map[phase0.Slot]*SlotResult, len(wonBlocks))
	oldKeys := make([]phase0.Slot, 0, len(wonBlocks))

	for slot, won := range wonBlocks {
		oldKeys = append(oldKeys, slot)

		inclusion := &InclusionResult{
			Source:          won.Source,
			BlockHash:       won.BlockHash,
			NumTransactions: won.NumTransactions,
			NumBlobs:        won.NumBlobs,
			ValueWei:        won.ValueWei,
			ValueETH:        won.ValueETH,
			Timestamp:       time.UnixMilli(won.Timestamp),
		}

		if result, ok := existing[slot]; ok {
			// Merge: only fill a missing inclusion; never overwrite data a
			// newer run already recorded.
			if result.Inclusion != nil {
				continue
			}

			merged := result.Clone()
			merged.Inclusion = inclusion
			merged.UpdatedAt = time.Now()
			upserts[slot] = merged
		} else {
			upserts[slot] = &SlotResult{
				Slot:      slot,
				Epoch:     uint64(slot) / slotsPerEpoch,
				Inclusion: inclusion,
				UpdatedAt: time.Now(),
			}
		}
	}

	if len(upserts) > 0 {
		if err := resultsPersistence.PersistBatch(upserts, nil); err != nil {
			// Keep the old namespace: the migration re-runs on the next start.
			log.WithError(err).Warn("Failed to persist migrated won blocks; keeping legacy namespace")
			return
		}
	}

	if err := oldPersistence.PersistBatch(nil, oldKeys); err != nil {
		log.WithError(err).Warn("Failed to delete legacy won_blocks namespace after migration")
		return
	}

	log.WithFields(logrus.Fields{
		"migrated": len(upserts),
		"total":    len(wonBlocks),
	}).Info("Migrated legacy won_blocks into slot results")
}
