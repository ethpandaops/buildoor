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
	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/epbs/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// EventType represents the type of event being streamed.
type EventType string

const (
	EventTypeConfig                      EventType = "config"
	EventTypeStatus                      EventType = "status"
	EventTypeSlotStart                   EventType = "slot_start"
	EventTypePayloadReady                EventType = "payload_ready"
	EventTypeBidSubmitted                EventType = "bid_submitted"
	EventTypeHeadReceived                EventType = "head_received"
	EventTypeReveal                      EventType = "reveal"
	EventTypeBidEvent                    EventType = "bid_event"
	EventTypeStats                       EventType = "stats"
	EventTypeSlotState                   EventType = "slot_state"
	EventTypePayloadAvailable            EventType = "payload_available"
	EventTypeBuilderInfo                 EventType = "builder_info"
	EventTypeHeadVotes                   EventType = "head_votes"
	EventTypeBidWon                      EventType = "bid_won"
	EventTypeBuilderAPIGetHeaderRcvd     EventType = "builder_api_get_header_received"
	EventTypeBuilderAPIGetHeaderDlvd     EventType = "builder_api_get_header_delivered"
	EventTypeBuilderAPISubmitBlindedRcvd EventType = "builder_api_submit_blinded_received"
	EventTypeBuilderAPISubmitBlindedDlvd EventType = "builder_api_submit_blinded_delivered"
	EventTypeServiceStatus               EventType = "service_status"
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

// PayloadAvailableStreamEvent is sent when a payload becomes available.
type PayloadAvailableStreamEvent struct {
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

// BidWonStreamEvent is sent when a bid is won (block successfully delivered).
type BidWonStreamEvent struct {
	Slot            uint64 `json:"slot"`
	BlockHash       string `json:"block_hash"`
	NumTransactions int    `json:"num_transactions"`
	NumBlobs        int    `json:"num_blobs"`
	ValueETH        string `json:"value_eth"`
	ValueWei        uint64 `json:"value_wei"`
	Timestamp       int64  `json:"timestamp"`
}

// ServiceStatusEvent indicates which services are available and enabled.
type ServiceStatusEvent struct {
	EPBSAvailable         bool   `json:"epbs_available"`
	EPBSEnabled           bool   `json:"epbs_enabled"`
	EPBSRegistrationState string `json:"epbs_registration_state"`
	BuilderAPIAvailable   bool   `json:"builder_api_available"`
	BuilderAPIEnabled     bool   `json:"builder_api_enabled"`
}

// BuilderAPIGetHeaderReceivedEvent is sent when a getHeader request is received.
type BuilderAPIGetHeaderReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	ParentHash string `json:"parent_hash"`
	Pubkey     string `json:"pubkey"`
	ReceivedAt int64  `json:"received_at"`
}

// BuilderAPIGetHeaderDeliveredEvent is sent when a header is successfully delivered.
type BuilderAPIGetHeaderDeliveredEvent struct {
	Slot        uint64 `json:"slot"`
	BlockHash   string `json:"block_hash"`
	BlockValue  string `json:"block_value"`
	DeliveredAt int64  `json:"delivered_at"`
}

// BuilderAPISubmitBlindedReceivedEvent is sent when a submitBlindedBlock request is received.
type BuilderAPISubmitBlindedReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	BlockHash  string `json:"block_hash"`
	ReceivedAt int64  `json:"received_at"`
}

// BuilderAPISubmitBlindedDeliveredEvent is sent when a blinded block is successfully published.
type BuilderAPISubmitBlindedDeliveredEvent struct {
	Slot        uint64 `json:"slot"`
	BlockHash   string `json:"block_hash"`
	DeliveredAt int64  `json:"delivered_at"`
}

// EventStreamManager manages SSE connections and event broadcasting.
type EventStreamManager struct {
	builderSvc    *builder.Service
	epbsSvc       *epbs.Service      // Optional ePBS service for bid events
	lifecycleMgr  *lifecycle.Manager // Optional lifecycle manager for balance info
	chainSvc      chain.Service      // Optional chain service for head vote tracking
	builderAPISvc *builderapi.Server // Optional Builder API server
	clients       map[chan *StreamEvent]struct{}
	mu            sync.RWMutex
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup

	// Track slot states for UI
	slotStates   map[phase0.Slot]*SlotStateEvent
	slotStatesMu sync.RWMutex

	// Track last sent stats to avoid spam
	lastStats   StatsResponse
	lastStatsMu sync.Mutex

	// Track last sent builder info to avoid spam
	lastBuilderInfo   BuilderInfoEvent
	lastBuilderInfoMu sync.Mutex

	// Track last sent service status to avoid spam
	lastServiceStatus   ServiceStatusEvent
	lastServiceStatusMu sync.Mutex
}

// NewEventStreamManager creates a new event stream manager.
func NewEventStreamManager(
	builderSvc *builder.Service,
	epbsSvc *epbs.Service,
	lifecycleMgr *lifecycle.Manager,
	chainSvc chain.Service,
	builderAPISvc *builderapi.Server,
) *EventStreamManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &EventStreamManager{
		builderSvc:    builderSvc,
		epbsSvc:       epbsSvc,
		lifecycleMgr:  lifecycleMgr,
		chainSvc:      chainSvc,
		builderAPISvc: builderAPISvc,
		clients:       make(map[chan *StreamEvent]struct{}, 8),
		ctx:           ctx,
		cancel:        cancel,
		slotStates:    make(map[phase0.Slot]*SlotStateEvent, 16),
	}
}

// Start begins the event stream manager.
func (m *EventStreamManager) Start() {
	// Subscribe to payload ready events
	payloadSub := m.builderSvc.SubscribePayloadReady(16)

	// Subscribe to beacon events
	headSub := m.builderSvc.GetCLClient().Events().SubscribeHead()
	bidSub := m.builderSvc.GetCLClient().Events().SubscribeBids()
	payloadAvailSub := m.builderSvc.GetCLClient().Events().SubscribePayloadAvailable()

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
		defer payloadAvailSub.Unsubscribe()

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

			case event := <-payloadAvailSub.Channel():
				m.handlePayloadAvailableEvent(event)

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
				// Periodically send stats, builder info, and service status
				m.sendStats()
				m.sendBuilderInfo()
				m.sendServiceStatus()
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

func (m *EventStreamManager) handlePayloadAvailableEvent(event *beacon.PayloadAvailableEvent) {
	// The execution_payload_available event only contains slot and block_root.
	// Fetch the full envelope from the beacon API to get block_hash and builder_index.
	blockRootHex := fmt.Sprintf("0x%x", event.BlockRoot[:])

	streamEvent := PayloadAvailableStreamEvent{
		Slot:       uint64(event.Slot),
		BlockRoot:  blockRootHex,
		ReceivedAt: event.ReceivedAt.UnixMilli(),
	}

	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()

	envelope, err := m.builderSvc.GetCLClient().GetExecutionPayloadEnvelope(ctx, blockRootHex)
	if err == nil {
		streamEvent.BlockHash = fmt.Sprintf("0x%x", envelope.BlockHash[:])
		streamEvent.BuilderIndex = envelope.BuilderIndex
	}

	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadAvailable,
		Timestamp: time.Now().UnixMilli(),
		Data:      streamEvent,
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

func (m *EventStreamManager) buildStatsResponse() StatsResponse {
	stats := m.builderSvc.GetStats()
	resp := StatsResponse{
		SlotsBuilt:     stats.SlotsBuilt,
		BlocksIncluded: stats.BlocksIncluded,
		BidsSubmitted:  stats.BidsSubmitted,
		BidsWon:        stats.BidsWon,
		TotalPaid:      stats.TotalPaid,
		RevealsSuccess: stats.RevealsSuccess,
		RevealsFailed:  stats.RevealsFailed,
		RevealsSkipped: stats.RevealsSkipped,
	}

	if m.builderAPISvc != nil {
		apiStats := m.builderAPISvc.GetRequestStats()
		resp.BuilderAPIHeadersRequested = apiStats.HeadersRequested
		resp.BuilderAPIBlocksPublished = apiStats.BlocksPublished
		resp.BuilderAPIRegisteredValidators = apiStats.ValidatorCount
	}

	return resp
}

func (m *EventStreamManager) sendStats() {
	resp := m.buildStatsResponse()

	// Only send if stats changed
	m.lastStatsMu.Lock()
	changed := resp != m.lastStats
	if changed {
		m.lastStats = resp
	}
	m.lastStatsMu.Unlock()

	if !changed {
		return
	}

	m.Broadcast(&StreamEvent{
		Type:      EventTypeStats,
		Timestamp: time.Now().UnixMilli(),
		Data:      resp,
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

func (m *EventStreamManager) getServiceStatus() ServiceStatusEvent {
	regState := "unknown"
	if m.epbsSvc != nil {
		regState = epbs.RegistrationStateName(m.epbsSvc.GetRegistrationState())
	}

	return ServiceStatusEvent{
		EPBSAvailable:         m.epbsSvc != nil,
		EPBSEnabled:           m.epbsSvc != nil && m.epbsSvc.IsEnabled(),
		EPBSRegistrationState: regState,
		BuilderAPIAvailable:   m.builderAPISvc != nil,
		BuilderAPIEnabled:     m.builderAPISvc != nil && m.builderAPISvc.IsEnabled(),
	}
}

func (m *EventStreamManager) sendServiceStatus() {
	status := m.getServiceStatus()

	m.lastServiceStatusMu.Lock()
	changed := status != m.lastServiceStatus
	if changed {
		m.lastServiceStatus = status
	}
	m.lastServiceStatusMu.Unlock()

	if !changed {
		return
	}

	m.Broadcast(&StreamEvent{
		Type:      EventTypeServiceStatus,
		Timestamp: time.Now().UnixMilli(),
		Data:      status,
	})
}

// BroadcastServiceStatus broadcasts the current service status.
func (m *EventStreamManager) BroadcastServiceStatus() {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeServiceStatus,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.getServiceStatus(),
	})
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

	// Send service status
	ch <- &StreamEvent{
		Type:      EventTypeServiceStatus,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.getServiceStatus(),
	}

	// Send current stats
	ch <- &StreamEvent{
		Type:      EventTypeStats,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.buildStatsResponse(),
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

// BroadcastBuilderAPIGetHeaderReceived broadcasts when a getHeader request is received.
func (m *EventStreamManager) BroadcastBuilderAPIGetHeaderReceived(slot uint64, parentHash, pubkey string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPIGetHeaderRcvd,
		Timestamp: now,
		Data: BuilderAPIGetHeaderReceivedEvent{
			Slot:       slot,
			ParentHash: parentHash,
			Pubkey:     pubkey,
			ReceivedAt: now,
		},
	})
}

// BroadcastBuilderAPIGetHeaderDelivered broadcasts when a header is successfully delivered.
func (m *EventStreamManager) BroadcastBuilderAPIGetHeaderDelivered(slot uint64, blockHash, blockValue string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPIGetHeaderDlvd,
		Timestamp: now,
		Data: BuilderAPIGetHeaderDeliveredEvent{
			Slot:        slot,
			BlockHash:   blockHash,
			BlockValue:  blockValue,
			DeliveredAt: now,
		},
	})
}

// BroadcastBuilderAPISubmitBlindedReceived broadcasts when a submitBlindedBlock request is received.
func (m *EventStreamManager) BroadcastBuilderAPISubmitBlindedReceived(slot uint64, blockHash string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPISubmitBlindedRcvd,
		Timestamp: now,
		Data: BuilderAPISubmitBlindedReceivedEvent{
			Slot:       slot,
			BlockHash:  blockHash,
			ReceivedAt: now,
		},
	})
}

// BroadcastBuilderAPISubmitBlindedDelivered broadcasts when a blinded block is successfully published.
func (m *EventStreamManager) BroadcastBuilderAPISubmitBlindedDelivered(slot uint64, blockHash string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPISubmitBlindedDlvd,
		Timestamp: now,
		Data: BuilderAPISubmitBlindedDeliveredEvent{
			Slot:        slot,
			BlockHash:   blockHash,
			DeliveredAt: now,
		},
	})
}

// BroadcastBidWon broadcasts a bid won event when a block is successfully delivered.
func (m *EventStreamManager) BroadcastBidWon(slot uint64, blockHash string, numTxs, numBlobs int, valueETH string, valueWei uint64) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidWon,
		Timestamp: now,
		Data: BidWonStreamEvent{
			Slot:            slot,
			BlockHash:       blockHash,
			NumTransactions: numTxs,
			NumBlobs:        numBlobs,
			ValueETH:        valueETH,
			ValueWei:        valueWei,
			Timestamp:       now,
		},
	})
}
