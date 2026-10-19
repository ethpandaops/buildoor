package slot_results

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// Artifact kinds stored in the slot_artifacts table.
const (
	ArtifactKindPayload  = "payload"
	ArtifactKindBid      = "bid"
	ArtifactKindEnvelope = "envelope"
)

const (
	// memoryBufferSlots bounds the in-memory artifact buffer to the newest N
	// distinct slot numbers, keeping the artifact endpoints functional
	// without a state-db and serving hot reads without SQLite round trips.
	memoryBufferSlots = 64

	// writerQueueCap bounds the async writer queue; capture calls fail fast
	// (with an error recorded on the attempt) instead of blocking hot paths.
	writerQueueCap = 256

	// writerFlushInterval batches queued artifacts into one transaction.
	writerFlushInterval = 250 * time.Millisecond
)

// sszMarshaler is the marshaling surface every stored artifact object
// provides (the eth2all wrappers and the legacy SignedBuilderBid all route
// their MarshalSSZ through the global dynssz instance, so preset-dependent
// list limits resolve correctly).
type sszMarshaler interface {
	MarshalSSZ() ([]byte, error)
}

// BidArtifactMeta is the display metadata stored next to a bid artifact.
type BidArtifactMeta struct {
	V                    int    `json:"v"`
	Transport            string `json:"transport"`
	TotalValueGwei       uint64 `json:"total_value_gwei"`
	ExecutionPaymentGwei uint64 `json:"execution_payment_gwei,omitempty"`
	At                   int64  `json:"at"` // unix milliseconds
}

// ArtifactStore captures raw SSZ artifacts per slot: write-through to a
// bounded in-memory buffer (synchronous) and to the slot_artifacts table via
// an asynchronous batching writer (never blocks request/gossip hot paths).
// The buffer and the table are eventually consistent; readers check the
// buffer first, then the database.
type ArtifactStore struct {
	stateDB *db.Database

	mu         sync.Mutex
	buffer     map[phase0.Slot]map[string][]*db.SlotArtifact
	bidIdx     map[phase0.Slot]int // next bid index per slot; lazily seeded from MAX(idx)+1
	payloadIdx map[phase0.Slot]*payloadIdxState

	writeQueue chan []db.SlotArtifact

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewArtifactStore creates the artifact store. stateDB must be non-nil (a
// disabled database is fine — captures then live in the memory buffer only).
func NewArtifactStore(stateDB *db.Database, log logrus.FieldLogger) *ArtifactStore {
	return &ArtifactStore{
		stateDB:    stateDB,
		buffer:     make(map[phase0.Slot]map[string][]*db.SlotArtifact, memoryBufferSlots),
		bidIdx:     make(map[phase0.Slot]int, memoryBufferSlots),
		payloadIdx: make(map[phase0.Slot]*payloadIdxState, memoryBufferSlots),
		writeQueue: make(chan []db.SlotArtifact, writerQueueCap),
		log:        log.WithField("component", "slot-artifacts"),
	}
}

// Start launches the async database writer.
func (s *ArtifactStore) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)

	s.wg.Add(1)

	go s.runWriter()
}

// Stop drains the writer queue into a final batch and stops the writer. Must
// be called before the database closes.
func (s *ArtifactStore) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()
}

func (s *ArtifactStore) runWriter() {
	defer s.wg.Done()

	ticker := time.NewTicker(writerFlushInterval)
	defer ticker.Stop()

	pending := make([]db.SlotArtifact, 0, 64)

	flush := func() {
		if len(pending) == 0 {
			return
		}

		if err := s.stateDB.InsertSlotArtifacts(pending); err != nil {
			s.log.WithError(err).WithField("batch", len(pending)).
				Warn("Failed to persist slot artifacts batch")
		}

		pending = pending[:0]
	}

	for {
		select {
		case <-s.ctx.Done():
			// Drain whatever is still queued, then flush and exit.
			for {
				select {
				case batch := <-s.writeQueue:
					pending = append(pending, batch...)
				default:
					flush()
					return
				}
			}
		case batch := <-s.writeQueue:
			pending = append(pending, batch...)

			if len(pending) >= writerQueueCap {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// PayloadArtifactMeta is the JSON metadata stored alongside a payload
// artifact: the payload's block hash (its key) and the build that produced
// it.
type PayloadArtifactMeta struct {
	V         int    `json:"v"`
	BlockHash string `json:"block_hash,omitempty"`
	// Build is the build kind (engine | testing), Run the testing run.
	Build           string `json:"build,omitempty"`
	Run             int    `json:"run,omitempty"`
	Candidate       string `json:"candidate,omitempty"`
	ParentBlockRoot string `json:"parent_block_root,omitempty"`
	ParentBlockHash string `json:"parent_block_hash,omitempty"`
	// Source is the build of artifacts stored before the build kinds: empty
	// or el for the engine payload, local for the testing payload.
	Source string `json:"source,omitempty"`
	At     int64  `json:"at"` // unix milliseconds
}

// custom reports whether the payload was built by a testing build: its
// transactions may come from the private pool.
func (m *PayloadArtifactMeta) custom() bool {
	if m.Build != "" {
		return m.Build == payload_builder.BuildKindTesting
	}

	return m.Source != "" && m.Source != "el"
}

// StorePayload captures one built execution payload under its block hash
// (meta.BlockHash): each payload gets its own per-slot artifact index, a
// payload stored again replaces its artifact. Index allocation is
// restart-safe (seeded from MAX(idx)+1).
func (s *ArtifactStore) StorePayload(slot phase0.Slot, fork version.DataVersion,
	payload sszMarshaler, meta PayloadArtifactMeta) error {
	meta.V = 2

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to encode payload artifact meta: %w", err)
	}

	s.mu.Lock()

	state := s.payloadIdx[slot]
	if state == nil {
		next := 0

		maxIdx, exists, err := s.stateDB.GetMaxSlotArtifactIdx(uint64(slot), ArtifactKindPayload)
		if err != nil {
			s.mu.Unlock()

			return fmt.Errorf("failed to seed payload artifact index: %w", err)
		}

		if exists {
			next = maxIdx + 1
		}

		state = &payloadIdxState{next: next, byHash: make(map[string]int, 2)}
		s.payloadIdx[slot] = state
	}

	idx, seen := state.byHash[meta.BlockHash]
	if !seen {
		idx = state.next
		state.next++
		state.byHash[meta.BlockHash] = idx
	}
	s.mu.Unlock()

	return s.store(slot, ArtifactKindPayload, idx, fork, string(metaJSON), payload)
}

// payloadIdxState allocates per-slot payload artifact indices: one index per
// block hash.
type payloadIdxState struct {
	next   int
	byHash map[string]int
}

// StoreBid captures one signed bid and returns its per-slot artifact index.
// Index allocation is restart-safe: the counter seeds from MAX(idx)+1 in the
// table so earlier bids are never overwritten.
func (s *ArtifactStore) StoreBid(slot phase0.Slot, fork version.DataVersion,
	bid sszMarshaler, meta BidArtifactMeta) (int, error) {
	meta.V = 1

	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return 0, fmt.Errorf("failed to encode bid artifact meta: %w", err)
	}

	s.mu.Lock()

	idx, seeded := s.bidIdx[slot]
	if !seeded {
		maxIdx, exists, err := s.stateDB.GetMaxSlotArtifactIdx(uint64(slot), ArtifactKindBid)
		if err != nil {
			s.mu.Unlock()

			return 0, fmt.Errorf("failed to seed bid artifact index: %w", err)
		}

		if exists {
			idx = maxIdx + 1
		}
	}

	s.bidIdx[slot] = idx + 1
	s.mu.Unlock()

	if err := s.store(slot, ArtifactKindBid, idx, fork, string(metaJSON), bid); err != nil {
		return 0, err
	}

	return idx, nil
}

// EnvelopeArtifactMeta is the JSON metadata stored alongside an envelope
// artifact: the block hash of the payload it reveals.
type EnvelopeArtifactMeta struct {
	BlockHash string `json:"block_hash"`
}

// StoreEnvelope captures the slot's signed payload envelope (idx 0; stored at
// construction time so failed publishes remain inspectable).
func (s *ArtifactStore) StoreEnvelope(slot phase0.Slot, fork version.DataVersion,
	envelope sszMarshaler, meta EnvelopeArtifactMeta) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to encode envelope artifact meta: %w", err)
	}

	return s.store(slot, ArtifactKindEnvelope, 0, fork, string(metaJSON), envelope)
}

func (s *ArtifactStore) store(slot phase0.Slot, kind string, idx int,
	fork version.DataVersion, meta string, obj sszMarshaler) error {
	if obj == nil {
		return fmt.Errorf("cannot store nil %s artifact", kind)
	}

	data, err := obj.MarshalSSZ()
	if err != nil {
		return fmt.Errorf("failed to SSZ-encode %s artifact: %w", kind, err)
	}

	artifact := db.SlotArtifact{
		Slot:      uint64(slot),
		Kind:      kind,
		Idx:       idx,
		Fork:      int64(fork),
		Meta:      meta,
		Data:      data,
		CreatedAt: time.Now().UnixMilli(),
	}

	s.insertBuffer(slot, &artifact)

	if !s.stateDB.Enabled() {
		return nil
	}

	select {
	case s.writeQueue <- []db.SlotArtifact{artifact}:
		return nil
	default:
		return fmt.Errorf("artifact writer queue full, %s artifact for slot %d not persisted", kind, slot)
	}
}

// insertBuffer adds the artifact to the memory buffer, evicting the oldest
// slots beyond the bound (newest N distinct slot numbers).
func (s *ArtifactStore) insertBuffer(slot phase0.Slot, artifact *db.SlotArtifact) {
	s.mu.Lock()
	defer s.mu.Unlock()

	kinds, ok := s.buffer[slot]
	if !ok {
		kinds = make(map[string][]*db.SlotArtifact, 3)
		s.buffer[slot] = kinds
	}

	// Payload/envelope replace idx 0; bids append by allocated index.
	replaced := false

	for i, existing := range kinds[artifact.Kind] {
		if existing.Idx == artifact.Idx {
			kinds[artifact.Kind][i] = artifact
			replaced = true

			break
		}
	}

	if !replaced {
		kinds[artifact.Kind] = append(kinds[artifact.Kind], artifact)
	}

	if len(s.buffer) > memoryBufferSlots {
		slots := make([]phase0.Slot, 0, len(s.buffer))
		for bufferedSlot := range s.buffer {
			slots = append(slots, bufferedSlot)
		}

		slices.Sort(slots)

		for _, evict := range slots[:len(s.buffer)-memoryBufferSlots] {
			delete(s.buffer, evict)
			delete(s.bidIdx, evict)
			delete(s.payloadIdx, evict)
		}
	}
}

// Get returns one artifact (data blob copied), or nil when it does not exist
// in the buffer or the database.
func (s *ArtifactStore) Get(slot phase0.Slot, kind string, idx int) (*db.SlotArtifact, error) {
	s.mu.Lock()

	if kinds, ok := s.buffer[slot]; ok {
		for _, artifact := range kinds[kind] {
			if artifact.Idx == idx {
				clone := *artifact
				clone.Data = append([]byte(nil), artifact.Data...)
				s.mu.Unlock()

				return &clone, nil
			}
		}
	}
	s.mu.Unlock()

	return s.stateDB.GetSlotArtifact(uint64(slot), kind, idx)
}

// LatestByKind returns the artifact of the given kind (idx 0) for the highest
// slot currently held in the memory buffer, for use as a live-test sample.
// Returns ok=false when no such artifact is buffered (the caller may fall back
// to a template); it deliberately scans only the buffer (the most recent
// slots), not the full database.
func (s *ArtifactStore) LatestByKind(kind string) (*db.SlotArtifact, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		bestSlot phase0.Slot
		best     *db.SlotArtifact
	)

	for slot, kinds := range s.buffer {
		if best != nil && slot <= bestSlot {
			continue
		}

		for _, artifact := range kinds[kind] {
			if artifact.Idx == 0 {
				bestSlot = slot
				best = artifact

				break
			}
		}
	}

	if best == nil {
		return nil, false
	}

	clone := *best
	clone.Data = append([]byte(nil), best.Data...)

	return &clone, true
}

// ListBids lists the slot's bid artifact metadata (no data blobs),
// idx-ascending.
func (s *ArtifactStore) ListBids(slot phase0.Slot) ([]db.SlotArtifact, error) {
	s.mu.Lock()

	if kinds, ok := s.buffer[slot]; ok && len(kinds[ArtifactKindBid]) > 0 {
		metas := make([]db.SlotArtifact, 0, len(kinds[ArtifactKindBid]))

		for _, artifact := range kinds[ArtifactKindBid] {
			meta := *artifact
			meta.Data = nil
			metas = append(metas, meta)
		}

		s.mu.Unlock()
		sort.Slice(metas, func(i, j int) bool { return metas[i].Idx < metas[j].Idx })

		return metas, nil
	}
	s.mu.Unlock()

	return s.stateDB.GetSlotArtifactMetas(uint64(slot), ArtifactKindBid)
}

// payloadMetas returns the slot's payload artifacts without their data, from
// the memory buffer or else the state-db.
func (s *ArtifactStore) payloadMetas(slot phase0.Slot) []db.SlotArtifact {
	s.mu.Lock()

	metas := make([]db.SlotArtifact, 0, 4)

	if kinds, ok := s.buffer[slot]; ok {
		for _, artifact := range kinds[ArtifactKindPayload] {
			meta := *artifact
			meta.Data = nil
			metas = append(metas, meta)
		}
	}
	s.mu.Unlock()

	if len(metas) > 0 {
		return metas
	}

	stored, err := s.stateDB.GetSlotArtifactMetas(uint64(slot), ArtifactKindPayload)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).
			Debug("Failed to list payload artifact metas")

		return nil
	}

	return stored
}

// IsCustomPayloadArtifact reports whether a payload artifact was built by a
// testing build: its transactions may come from the private pool, so it must
// not be served to unauthenticated readers.
func IsCustomPayloadArtifact(artifact *db.SlotArtifact) bool {
	if artifact == nil || artifact.Kind != ArtifactKindPayload {
		return false
	}

	var meta PayloadArtifactMeta
	if err := json.Unmarshal([]byte(artifact.Meta), &meta); err != nil {
		return false
	}

	return meta.custom()
}

// PayloadByHash returns the slot's payload artifact with the given block
// hash (0x-hex), or nil when none was captured. Artifacts stored before the
// block hash was recorded are matched by decoding them.
func (s *ArtifactStore) PayloadByHash(slot phase0.Slot, blockHash string) (*db.SlotArtifact, error) {
	metas := s.payloadMetas(slot)
	legacy := make([]int, 0, len(metas))

	for _, artifact := range metas {
		var meta PayloadArtifactMeta
		if err := json.Unmarshal([]byte(artifact.Meta), &meta); err != nil {
			continue
		}

		if meta.BlockHash == "" {
			legacy = append(legacy, artifact.Idx)

			continue
		}

		if strings.EqualFold(meta.BlockHash, blockHash) {
			return s.Get(slot, ArtifactKindPayload, artifact.Idx)
		}
	}

	for _, idx := range legacy {
		artifact, err := s.Get(slot, ArtifactKindPayload, idx)
		if err != nil || artifact == nil {
			continue
		}

		payload := &eth2all.ExecutionPayload{Version: version.DataVersion(artifact.Fork)}
		if err := payload.UnmarshalSSZ(artifact.Data); err != nil {
			continue
		}

		if strings.EqualFold(fmt.Sprintf("%#x", payload.BlockHash), blockHash) {
			return artifact, nil
		}
	}

	return nil, nil
}

// EnvelopeIsPrivate reports whether the slot's envelope reveals a custom
// (testing build) payload: it then carries the payload's transactions,
// which may come from the private pool. Envelopes stored before the revealed
// block hash was recorded count as private when the slot captured any
// custom payload.
func (s *ArtifactStore) EnvelopeIsPrivate(slot phase0.Slot, envelope *db.SlotArtifact) bool {
	if envelope == nil {
		return false
	}

	var meta EnvelopeArtifactMeta
	if err := json.Unmarshal([]byte(envelope.Meta), &meta); err == nil && meta.BlockHash != "" {
		payload, err := s.PayloadByHash(slot, meta.BlockHash)
		if err != nil {
			return true
		}

		// A revealed payload that was never captured is decided by the
		// envelope itself: unknown means private.
		return payload == nil || IsCustomPayloadArtifact(payload)
	}

	for _, artifact := range s.payloadMetas(slot) {
		if IsCustomPayloadArtifact(&artifact) {
			return true
		}
	}

	return false
}

// PruneBefore drops all artifacts for slots below the cutoff from the buffer
// and the database.
func (s *ArtifactStore) PruneBefore(cutoff phase0.Slot) {
	s.mu.Lock()
	for slot := range s.buffer {
		if slot < cutoff {
			delete(s.buffer, slot)
			delete(s.bidIdx, slot)
			delete(s.payloadIdx, slot)
		}
	}
	s.mu.Unlock()

	deleted, err := s.stateDB.DeleteSlotArtifactsBefore(uint64(cutoff))
	if err != nil {
		s.log.WithError(err).Warn("Failed to prune slot artifacts")
		return
	}

	if deleted > 0 {
		// Note: SQLite does not shrink the file on delete without a vacuum.
		s.log.WithFields(logrus.Fields{
			"cutoff":  cutoff,
			"deleted": deleted,
		}).Debug("Pruned slot artifacts")
	}
}
