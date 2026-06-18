package payload_builder

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// Payload is the canonical built payload, produced once by the builder and
// referenced (never copied) throughout the stack — it is a large object (blobs,
// transactions), so downstream consumers hold the same *Payload rather than
// copying it. The build outputs are immutable; the bid/reveal activity log is
// appended by the payload_bidder as bids are produced and the payload revealed.
//
// Anything derivable from the build objects (slot, parent hashes, timestamp,
// gas limit, ...) is read through them rather than duplicated here.
type Payload struct {
	// Attributes is the payload_attributes event this build was triggered by.
	Attributes *beacon.PayloadAttributesEvent
	// ExecutionPayload is the fork-agnostic beacon execution payload.
	ExecutionPayload *eth2all.ExecutionPayload
	// BlobsBundle holds the blobs/commitments/proofs (Deneb+), nil if none.
	BlobsBundle *BlobsBundle
	// ExecutionRequests are the parsed execution requests (Electra+).
	ExecutionRequests *electra.ExecutionRequests

	// Metadata not carried by the objects above.
	BlockHash    phase0.Hash32  // block hash after extra-data injection
	FeeRecipient common.Address // resolved proposer fee recipient for the bid
	BlockValue   *big.Int       // EL-reported block value (wei)
	ReadyAt      time.Time      // when the payload became ready

	// activity is the bid/reveal log, appended by the payload_bidder and read by
	// the WebUI. The mutex also makes Payload copy-unsafe, enforcing the
	// pass-by-pointer rule.
	activity payloadActivity
}

// BidTransport identifies which submitter produced a bid or reveal.
type BidTransport string

const (
	// BidTransportP2P is the ePBS p2p gossip submitter.
	BidTransportP2P BidTransport = "p2p"
	// BidTransportBuilderAPI is the HTTP Builder API submitter.
	BidTransportBuilderAPI BidTransport = "builder-api"
)

// BidRecord is a lightweight record of a bid produced for this payload (not the
// heavy payload itself).
type BidRecord struct {
	Transport        BidTransport
	Value            phase0.Gwei
	ExecutionPayment phase0.Gwei
	At               time.Time
}

// RevealRecord records that the payload's execution envelope was revealed.
type RevealRecord struct {
	Transport       BidTransport
	BeaconBlockRoot phase0.Root
	At              time.Time
}

type payloadActivity struct {
	mu     sync.RWMutex
	bids   []BidRecord
	reveal *RevealRecord
}

// AddBid appends a bid record to the payload's activity log.
func (p *Payload) AddBid(rec BidRecord) {
	p.activity.mu.Lock()
	defer p.activity.mu.Unlock()

	p.activity.bids = append(p.activity.bids, rec)
}

// Bids returns a snapshot copy of the bids recorded for this payload.
func (p *Payload) Bids() []BidRecord {
	p.activity.mu.RLock()
	defer p.activity.mu.RUnlock()

	out := make([]BidRecord, len(p.activity.bids))
	copy(out, p.activity.bids)

	return out
}

// MarkRevealed records the reveal of this payload's envelope. The first reveal
// wins; subsequent calls are ignored.
func (p *Payload) MarkRevealed(rec RevealRecord) {
	p.activity.mu.Lock()
	defer p.activity.mu.Unlock()

	if p.activity.reveal == nil {
		p.activity.reveal = &rec
	}
}

// Reveal returns the reveal record if the payload has been revealed, else nil.
func (p *Payload) Reveal() *RevealRecord {
	p.activity.mu.RLock()
	defer p.activity.mu.RUnlock()

	return p.activity.reveal
}
