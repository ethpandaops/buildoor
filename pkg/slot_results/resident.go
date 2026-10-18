package slot_results

import (
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// fullResidentEpochs is how long a slot's full record stays in memory: past
// it a record hardly changes any more (the late verdicts land within a few
// dozen slots), so memory keeps its summary and the state-db the full
// record, read back on demand.
const fullResidentEpochs = 2

// residentBefore is the first slot whose full record stays in memory.
func (t *Tracker) residentBefore(current phase0.Slot) phase0.Slot {
	window := phase0.Slot(fullResidentEpochs * t.chainSvc.GetChainSpec().SlotsPerEpoch)
	if current <= window {
		return 0
	}

	return current - window
}

// compactBefore replaces the in-memory records of the slots before cutoff
// with their summaries. Only records whose latest change already reached the
// state-db are replaced; without a state-db nothing is.
func (t *Tracker) compactBefore(cutoff phase0.Slot) {
	if t.kv == nil || cutoff == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	compacted := 0

	for slot, result := range t.store.Entries() {
		if slot >= cutoff || result.resident {
			continue
		}

		summary := result.Summary()
		summary.resident = true

		if t.store.ReplaceResident(slot, summary) {
			compacted++
		}
	}

	if compacted > 0 {
		t.log.WithFields(logrus.Fields{
			"records": compacted,
			"before":  cutoff,
		}).Debug("Slot results moved to the state-db, summaries kept in memory")
	}
}

// full returns the slot's full record: the in-memory one, or for a resident
// summary the record read back from the state-db. ok is false when a
// summary's full record cannot be read.
func (t *Tracker) full(slot phase0.Slot, result *SlotResult) (*SlotResult, bool) {
	if !result.resident {
		return result, true
	}

	stored, found, err := t.kv.Get(slot)
	if err != nil || !found {
		t.log.WithError(err).WithField("slot", slot).Warn("Cannot read a slot result back from the state-db")

		return result, false
	}

	return stored, true
}
