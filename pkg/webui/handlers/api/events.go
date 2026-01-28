package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// EventType represents the type of event being streamed.
type EventType string

const (
	EventTypeConfig          EventType = "config"
	EventTypeStatus          EventType = "status"
	EventTypeSlotStart       EventType = "slot_start"
	EventTypePayloadReady    EventType = "payload_ready"
	EventTypeBidSubmitted    EventType = "bid_submitted"
	EventTypeHeadReceived    EventType = "head_received"
	EventTypeReveal          EventType = "reveal"
	EventTypeBidEvent        EventType = "bid_event"
	EventTypeStats           EventType = "stats"
	EventTypeSlotState       EventType = "slot_state"
	EventTypePayloadEnvelope EventType = "payload_envelope"
	EventTypeBuilderInfo     EventType = "builder_info"
	EventTypeHeadVotes       EventType = "head_votes"
)

// StreamEvent is a wrapper for all event types sent to clients.
type StreamEvent struct {
	Type      EventType `json:"type"`
	Timestamp int64     `json:"timestamp"`
	Data      any       `json:"data"`
}

// SlotStartEvent is sent when a new slot starts.
type SlotStartEvent struct {
	Slot          uint64 `json:"slot"`
	SlotStartTime int64  `json:"slot_start_time"`
}

// PayloadReadyStreamEvent is sent when a payload becomes available.
type PayloadReadyStreamEvent struct {
	Slot            uint64 `json:"slot"`
	BlockHash       string `json:"block_hash"`
	ParentBlockHash string `json:"parent_block_hash"`
	BlockValue      uint64 `json:"block_value"`
	ReadyAt         int64  `json:"ready_at"`
}

// BidSubmittedEvent is sent when we submit a bid (success or failure).
type BidSubmittedEvent struct {
	Slot      uint64 `json:"slot"`
	BlockHash string `json:"block_hash"`
	Value     uint64 `json:"value"`
	BidCount  int    `json:"bid_count"`
	Timestamp int64  `json:"timestamp"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// HeadReceivedEvent is sent when a head event is received.
type HeadReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	BlockRoot  string `json:"block_root"`
	ReceivedAt int64  `json:"received_at"`
}

// RevealStreamEvent is sent when we submit or skip a reveal.
type RevealStreamEvent struct {
	Slot      uint64 `json:"slot"`
	Success   bool   `json:"success"`
	Skipped   bool   `json:"skipped"`
	Timestamp int64  `json:"timestamp"`
}

// BidStreamEvent represents a bid from any builder.
type BidStreamEvent struct {
	Slot         uint64 `json:"slot"`
	BuilderIndex uint64 `json:"builder_index"`
	Value        uint64 `json:"value"`
	BlockHash    string `json:"block_hash"`
	IsOurs       bool   `json:"is_ours"`
	ReceivedAt   int64  `json:"received_at"`
}

// SlotStateEvent represents the current state of a slot.
type SlotStateEvent struct {
	Slot           uint64 `json:"slot"`
	PayloadReady   bool   `json:"payload_ready"`
	BidCount       int    `json:"bid_count"`
	BidsClosed     bool   `json:"bids_closed"`
	BidIncluded    bool   `json:"bid_included"`
	Revealed       bool   `json:"revealed"`
	HighestBidOurs bool   `json:"highest_bid_ours"`
	HighestBid     uint64 `json:"highest_bid"`
	OurBid         uint64 `json:"our_bid"`
}

// PayloadEnvelopeStreamEvent is sent when a payload envelope is received.
type PayloadEnvelopeStreamEvent struct {
	Slot         uint64 `json:"slot"`
	BlockRoot    string `json:"block_root"`
	BlockHash    string `json:"block_hash"`
	BuilderIndex uint64 `json:"builder_index"`
	ReceivedAt   int64  `json:"received_at"`
}

// BuilderInfoEvent contains builder identity and balance information.
type BuilderInfoEvent struct {
	BuilderPubkey     string `json:"builder_pubkey"`
	BuilderIndex      uint64 `json:"builder_index"`
	IsRegistered      bool   `json:"is_registered"`
	CLBalance         uint64 `json:"cl_balance_gwei"`
	PendingPayments   uint64 `json:"pending_payments_gwei"`
	EffectiveBalance  uint64 `json:"effective_balance_gwei"`
	LifecycleEnabled  bool   `json:"lifecycle_enabled"`
	WalletAddress     string `json:"wallet_address,omitempty"`
	WalletBalance     string `json:"wallet_balance_wei,omitempty"`
	PendingDeposit    uint64 `json:"pending_deposit_gwei,omitempty"`
	DepositEpoch      uint64 `json:"deposit_epoch"`
	WithdrawableEpoch uint64 `json:"withdrawable_epoch"`
}

// HeadVotesStreamEvent is sent when head vote participation changes.
type HeadVotesStreamEvent struct {
	Slot             uint64  `json:"slot"`
	ParticipationPct float64 `json:"participation_pct"`
	ParticipationETH uint64  `json:"participation_eth"`
	TotalSlotETH     uint64  `json:"total_slot_eth"`
	Timestamp        int64   `json:"timestamp"`
}

// EventStreamManager manages SSE connections and event broadcasting.
type EventStreamManager struct {
	builderSvc   *builder.Service
	epbsSvc      *epbs.Service      // Optional ePBS service for bid events
	lifecycleMgr *lifecycle.Manager // Optional lifecycle manager for balance info
	chainSvc     chain.Service      // Optional chain service for head vote tracking
	clients      map[chan *StreamEvent]struct{}
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup

	// Track slot states for UI
	slotStates   map[phase0.Slot]*SlotStateEvent
	slotStatesMu sync.RWMutex

	// Track last sent stats to avoid spam
	lastStats   builder.BuilderStats
	lastStatsMu sync.Mutex

	// Track last sent builder info to avoid spam
	lastBuilderInfo   BuilderInfoEvent
	lastBuilderInfoMu sync.Mutex
}

// NewEventStreamManager creates a new event stream manager.
func NewEventStreamManager(
	builderSvc *builder.Service,
	epbsSvc *epbs.Service,
	lifecycleMgr *lifecycle.Manager,
	chainSvc chain.Service,
) *EventStreamManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &EventStreamManager{
		builderSvc:   builderSvc,
		epbsSvc:      epbsSvc,
		lifecycleMgr: lifecycleMgr,
		chainSvc:     chainSvc,
		clients:      make(map[chan *StreamEvent]struct{}, 8),
		ctx:          ctx,
		cancel:       cancel,
		slotStates:   make(map[phase0.Slot]*SlotStateEvent, 16),
	}
}

// Start begins the event stream manager.
func (m *EventStreamManager) Start() {
	// Subscribe to payload ready events
	payloadSub := m.builderSvc.SubscribePayloadReady(16)

	// Subscribe to beacon events
	headSub := m.builderSvc.GetCLClient().Events().SubscribeHead()
	bidSub := m.builderSvc.GetCLClient().Events().SubscribeBids()
	envelopeSub := m.builderSvc.GetCLClient().Events().SubscribePayloadEnvelope()

	// Subscribe to bid submission events from ePBS service (if available)
	var bidSubmitChan <-chan *epbs.BidSubmissionEvent
	if m.epbsSvc != nil {
		bidSubmitSub := m.epbsSvc.SubscribeBidSubmissions(16)
		bidSubmitChan = bidSubmitSub.Channel()
		defer bidSubmitSub.Unsubscribe()
	}

	// Subscribe to head vote updates (if chain service available)
	var headVoteChan <-chan *chain.HeadVoteUpdate
	if m.chainSvc != nil {
		if tracker := m.chainSvc.GetHeadVoteTracker(); tracker != nil {
			hvSub := tracker.SubscribeUpdates()
			headVoteChan = hvSub.Channel()
			defer hvSub.Unsubscribe()
		}
	}

	m.wg.Add(1)

	go func() {
		defer m.wg.Done()
		defer payloadSub.Unsubscribe()
		defer headSub.Unsubscribe()
		defer bidSub.Unsubscribe()
		defer envelopeSub.Unsubscribe()

		// Slot tracking ticker
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		var lastSlot phase0.Slot

		for {
			select {
			case <-m.ctx.Done():
				return

			case event := <-payloadSub.Channel():
				m.handlePayloadReady(event)

			case event := <-headSub.Channel():
				m.handleHeadEvent(event)

			case event := <-bidSub.Channel():
				m.handleBidEvent(event)

			case event := <-envelopeSub.Channel():
				m.handlePayloadEnvelopeEvent(event)

			case event, ok := <-bidSubmitChan:
				if ok {
					m.handleBidSubmissionEvent(event)
				}

			case event, ok := <-headVoteChan:
				if ok {
					m.handleHeadVoteUpdate(event)
				}

			case <-ticker.C:
				currentSlot := m.builderSvc.GetCurrentSlot()
				if currentSlot != lastSlot {
					lastSlot = currentSlot
					m.handleSlotStart(currentSlot)
				}
				// Periodically send stats and builder info
				m.sendStats()
				m.sendBuilderInfo()
			}
		}
	}()
}

// Stop stops the event stream manager.
func (m *EventStreamManager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// AddClient adds a new SSE client.
func (m *EventStreamManager) AddClient(ch chan *StreamEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.clients[ch] = struct{}{}
}

// RemoveClient removes an SSE client.
func (m *EventStreamManager) RemoveClient(ch chan *StreamEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.clients, ch)
	close(ch)
}

// Broadcast sends an event to all connected clients.
func (m *EventStreamManager) Broadcast(event *StreamEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for ch := range m.clients {
		select {
		case ch <- event:
		default:
			// Client is slow, skip
		}
	}
}

func (m *EventStreamManager) handleSlotStart(slot phase0.Slot) {
	genesis := m.builderSvc.GetGenesis()
	if genesis == nil {
		return
	}

	chainSpec := m.builderSvc.GetChainSpec()
	slotStartTime := genesis.GenesisTime.Add(time.Duration(slot) * chainSpec.SecondsPerSlot)

	m.Broadcast(&StreamEvent{
		Type:      EventTypeSlotStart,
		Timestamp: time.Now().UnixMilli(),
		Data: SlotStartEvent{
			Slot:          uint64(slot),
			SlotStartTime: slotStartTime.UnixMilli(),
		},
	})

	// Initialize slot state
	m.slotStatesMu.Lock()
	if _, ok := m.slotStates[slot]; !ok {
		m.slotStates[slot] = &SlotStateEvent{
			Slot: uint64(slot),
		}
	}
	m.slotStatesMu.Unlock()

	// Cleanup old states
	m.cleanupOldSlots(slot)
}

func (m *EventStreamManager) handlePayloadReady(event *builder.PayloadReadyEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadReady,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadReadyStreamEvent{
			Slot:            uint64(event.Slot),
			BlockHash:       fmt.Sprintf("0x%x", event.BlockHash[:]),
			ParentBlockHash: fmt.Sprintf("0x%x", event.ParentBlockHash[:]),
			BlockValue:      event.BlockValue,
			ReadyAt:         event.ReadyAt.UnixMilli(),
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	if state, ok := m.slotStates[event.Slot]; ok {
		state.PayloadReady = true
	} else {
		m.slotStates[event.Slot] = &SlotStateEvent{
			Slot:         uint64(event.Slot),
			PayloadReady: true,
		}
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(event.Slot)
}

func (m *EventStreamManager) handleBidSubmissionEvent(event *epbs.BidSubmissionEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidSubmitted,
		Timestamp: time.Now().UnixMilli(),
		Data: BidSubmittedEvent{
			Slot:      uint64(event.Slot),
			BlockHash: fmt.Sprintf("0x%x", event.BlockHash[:]),
			Value:     event.Value,
			BidCount:  event.BidCount,
			Timestamp: time.Now().UnixMilli(),
			Success:   event.Success,
			Error:     event.Error,
		},
	})

	// Update slot state only on success
	if event.Success {
		m.slotStatesMu.Lock()
		state, ok := m.slotStates[event.Slot]
		if ok {
			state.BidCount = event.BidCount
			state.OurBid = event.Value
		}
		m.slotStatesMu.Unlock()

		m.broadcastSlotState(event.Slot)
	}
}

func (m *EventStreamManager) handleHeadEvent(event *beacon.HeadEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeHeadReceived,
		Timestamp: time.Now().UnixMilli(),
		Data: HeadReceivedEvent{
			Slot:       uint64(event.Slot),
			BlockRoot:  fmt.Sprintf("0x%x", event.Block[:]),
			ReceivedAt: time.Now().UnixMilli(),
		},
	})

	// Update slot state - bidding closed
	m.slotStatesMu.Lock()
	if state, ok := m.slotStates[event.Slot]; ok {
		state.BidsClosed = true
	} else {
		m.slotStates[event.Slot] = &SlotStateEvent{
			Slot:       uint64(event.Slot),
			BidsClosed: true,
		}
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(event.Slot)
}

func (m *EventStreamManager) handleBidEvent(event *beacon.BidEvent) {
	// Determine if this is our bid
	isOurs := false
	// We'd need epbsSvc here to check builder index, for now just broadcast all

	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidEvent,
		Timestamp: time.Now().UnixMilli(),
		Data: BidStreamEvent{
			Slot:         uint64(event.Slot),
			BuilderIndex: event.BuilderIndex,
			Value:        event.Value,
			BlockHash:    fmt.Sprintf("0x%x", event.BlockHash[:]),
			IsOurs:       isOurs,
			ReceivedAt:   event.ReceivedAt.UnixMilli(),
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	state, ok := m.slotStates[event.Slot]
	if !ok {
		state = &SlotStateEvent{
			Slot: uint64(event.Slot),
		}
		m.slotStates[event.Slot] = state
	}

	if event.Value > state.HighestBid {
		state.HighestBid = event.Value
	}

	if isOurs {
		state.OurBid = event.Value
		state.BidCount++
		state.HighestBidOurs = event.Value >= state.HighestBid
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(event.Slot)
}

func (m *EventStreamManager) handlePayloadEnvelopeEvent(event *beacon.PayloadEnvelopeEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadEnvelope,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadEnvelopeStreamEvent{
			Slot:         uint64(event.Slot),
			BlockRoot:    fmt.Sprintf("0x%x", event.BlockRoot[:]),
			BlockHash:    fmt.Sprintf("0x%x", event.BlockHash[:]),
			BuilderIndex: event.BuilderIndex,
			ReceivedAt:   event.ReceivedAt.UnixMilli(),
		},
	})
}

func (m *EventStreamManager) handleHeadVoteUpdate(event *chain.HeadVoteUpdate) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeHeadVotes,
		Timestamp: time.Now().UnixMilli(),
		Data: HeadVotesStreamEvent{
			Slot:             uint64(event.Slot),
			ParticipationPct: event.ParticipationPct,
			ParticipationETH: event.ParticipationETH,
			TotalSlotETH:     event.TotalSlotETH,
			Timestamp:        event.Timestamp,
		},
	})
}

func (m *EventStreamManager) broadcastSlotState(slot phase0.Slot) {
	m.slotStatesMu.RLock()
	state, ok := m.slotStates[slot]
	if !ok {
		m.slotStatesMu.RUnlock()
		return
	}
	// Copy to avoid race
	stateCopy := *state
	m.slotStatesMu.RUnlock()

	m.Broadcast(&StreamEvent{
		Type:      EventTypeSlotState,
		Timestamp: time.Now().UnixMilli(),
		Data:      stateCopy,
	})
}

func (m *EventStreamManager) sendStats() {
	stats := m.builderSvc.GetStats()

	// Only send if stats changed
	m.lastStatsMu.Lock()
	changed := stats != m.lastStats
	if changed {
		m.lastStats = stats
	}
	m.lastStatsMu.Unlock()

	if !changed {
		return
	}

	m.Broadcast(&StreamEvent{
		Type:      EventTypeStats,
		Timestamp: time.Now().UnixMilli(),
		Data: StatsResponse{
			SlotsBuilt:     stats.SlotsBuilt,
			BidsSubmitted:  stats.BidsSubmitted,
			BidsWon:        stats.BidsWon,
			TotalPaid:      stats.TotalPaid,
			RevealsSuccess: stats.RevealsSuccess,
			RevealsFailed:  stats.RevealsFailed,
			RevealsSkipped: stats.RevealsSkipped,
		},
	})
}

func (m *EventStreamManager) sendBuilderInfo() {
	info := m.getBuilderInfo()

	// Only send if info changed
	m.lastBuilderInfoMu.Lock()
	changed := info != m.lastBuilderInfo
	if changed {
		m.lastBuilderInfo = info
	}
	m.lastBuilderInfoMu.Unlock()

	if !changed {
		return
	}

	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderInfo,
		Timestamp: time.Now().UnixMilli(),
		Data:      info,
	})
}

func (m *EventStreamManager) getBuilderInfo() BuilderInfoEvent {
	info := BuilderInfoEvent{}

	// Get builder pubkey and index from ePBS service
	if m.epbsSvc != nil {
		pubkey := m.epbsSvc.GetBuilderPubkey()
		info.BuilderPubkey = pubkey.String()
		info.BuilderIndex = m.epbsSvc.GetBuilderIndex()

		// Get pending payments from bid tracker
		if tracker := m.epbsSvc.GetBidTracker(); tracker != nil {
			info.PendingPayments = tracker.GetTotalPendingPayments()
		}
	}

	// Get lifecycle info if available
	if m.lifecycleMgr != nil {
		info.LifecycleEnabled = true
		state := m.lifecycleMgr.GetBuilderState()

		if state != nil {
			info.IsRegistered = state.IsRegistered
			info.CLBalance = state.Balance
			info.DepositEpoch = state.DepositEpoch
			info.WithdrawableEpoch = state.WithdrawableEpoch

			// Calculate effective balance
			if info.CLBalance > info.PendingPayments {
				info.EffectiveBalance = info.CLBalance - info.PendingPayments
			}
		}

		// Get wallet info
		if wallet := m.lifecycleMgr.GetWallet(); wallet != nil {
			info.WalletAddress = wallet.Address().Hex()

			// Get wallet balance (async-safe, we just use cached value or fetch)
			if balance, err := wallet.GetBalance(m.ctx); err == nil && balance != nil {
				info.WalletBalance = balance.String()
			}
		}
	}

	return info
}

func (m *EventStreamManager) cleanupOldSlots(currentSlot phase0.Slot) {
	m.slotStatesMu.Lock()
	defer m.slotStatesMu.Unlock()

	const keepSlots = 10

	for slot := range m.slotStates {
		if currentSlot > phase0.Slot(keepSlots) && slot < currentSlot-phase0.Slot(keepSlots) {
			delete(m.slotStates, slot)
		}
	}
}

// SendInitialState sends the current state to a newly connected client.
func (m *EventStreamManager) SendInitialState(ch chan *StreamEvent) {
	// Send current config
	cfg := m.builderSvc.GetConfig()
	ch <- &StreamEvent{
		Type:      EventTypeConfig,
		Timestamp: time.Now().UnixMilli(),
		Data:      cfg,
	}

	// Send current status
	ch <- &StreamEvent{
		Type:      EventTypeStatus,
		Timestamp: time.Now().UnixMilli(),
		Data: StatusResponse{
			Running:     true,
			CurrentSlot: uint64(m.builderSvc.GetCurrentSlot()),
		},
	}

	// Send current stats
	stats := m.builderSvc.GetStats()
	ch <- &StreamEvent{
		Type:      EventTypeStats,
		Timestamp: time.Now().UnixMilli(),
		Data: StatsResponse{
			SlotsBuilt:     stats.SlotsBuilt,
			BidsSubmitted:  stats.BidsSubmitted,
			BidsWon:        stats.BidsWon,
			TotalPaid:      stats.TotalPaid,
			RevealsSuccess: stats.RevealsSuccess,
			RevealsFailed:  stats.RevealsFailed,
			RevealsSkipped: stats.RevealsSkipped,
		},
	}

	// Send builder info
	ch <- &StreamEvent{
		Type:      EventTypeBuilderInfo,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.getBuilderInfo(),
	}

	// Send genesis and chain spec info
	genesis := m.builderSvc.GetGenesis()
	chainSpec := m.builderSvc.GetChainSpec()

	if genesis != nil && chainSpec != nil {
		ch <- &StreamEvent{
			Type:      "chain_info",
			Timestamp: time.Now().UnixMilli(),
			Data: map[string]any{
				"genesis_time":     genesis.GenesisTime.UnixMilli(),
				"seconds_per_slot": int64(chainSpec.SecondsPerSlot.Milliseconds()),
			},
		}
	}

	// Send recent slot states
	m.slotStatesMu.RLock()
	for _, state := range m.slotStates {
		ch <- &StreamEvent{
			Type:      EventTypeSlotState,
			Timestamp: time.Now().UnixMilli(),
			Data:      *state,
		}
	}
	m.slotStatesMu.RUnlock()
}

// EventStream handles the SSE endpoint for real-time events.
func (h *APIHandler) EventStream(w http.ResponseWriter, r *http.Request) {
	// Check if event stream manager is available
	if h.eventStreamMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream not available")
		return
	}

	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create channel for this client
	clientCh := make(chan *StreamEvent, 32)

	// Add client
	h.eventStreamMgr.AddClient(clientCh)
	defer h.eventStreamMgr.RemoveClient(clientCh)

	// Send initial state
	h.eventStreamMgr.SendInitialState(clientCh)

	// Get the flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return

		case event, ok := <-clientCh:
			if !ok {
				return
			}

			data, err := json.Marshal(event)
			if err != nil {
				continue
			}

			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// BroadcastBidSubmitted broadcasts a bid submitted event (success).
func (m *EventStreamManager) BroadcastBidSubmitted(slot uint64, blockHash string, value uint64, bidCount int) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidSubmitted,
		Timestamp: time.Now().UnixMilli(),
		Data: BidSubmittedEvent{
			Slot:      slot,
			BlockHash: blockHash,
			Value:     value,
			BidCount:  bidCount,
			Timestamp: time.Now().UnixMilli(),
			Success:   true,
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	state, ok := m.slotStates[phase0.Slot(slot)]
	if ok {
		state.BidCount = bidCount
		state.OurBid = value
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(phase0.Slot(slot))
}

// BroadcastBidFailed broadcasts a bid submission failure event.
func (m *EventStreamManager) BroadcastBidFailed(slot uint64, blockHash string, value uint64, bidCount int, errMsg string) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidSubmitted,
		Timestamp: time.Now().UnixMilli(),
		Data: BidSubmittedEvent{
			Slot:      slot,
			BlockHash: blockHash,
			Value:     value,
			BidCount:  bidCount,
			Timestamp: time.Now().UnixMilli(),
			Success:   false,
			Error:     errMsg,
		},
	})
}

// BroadcastReveal broadcasts a reveal event.
func (m *EventStreamManager) BroadcastReveal(slot uint64, success, skipped bool) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeReveal,
		Timestamp: time.Now().UnixMilli(),
		Data: RevealStreamEvent{
			Slot:      slot,
			Success:   success,
			Skipped:   skipped,
			Timestamp: time.Now().UnixMilli(),
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	state, ok := m.slotStates[phase0.Slot(slot)]
	if ok {
		state.Revealed = success
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(phase0.Slot(slot))
}

// BroadcastConfigUpdate broadcasts a config update event.
func (m *EventStreamManager) BroadcastConfigUpdate() {
	cfg := m.builderSvc.GetConfig()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeConfig,
		Timestamp: time.Now().UnixMilli(),
		Data:      cfg,
	})
}
