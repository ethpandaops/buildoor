package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// EventType represents the type of event being streamed.
type EventType string

const (
	EventTypeConfig                      EventType = "config"
	EventTypeStatus                      EventType = "status"
	EventTypeSlotStart                   EventType = "slot_start"
	EventTypePayloadAttributes           EventType = "payload_attributes"
	EventTypePayloadBuildStarted         EventType = "payload_build_started"
	EventTypePayloadBuildFailed          EventType = "payload_build_failed"
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
	EventTypeBuilderAPIGetBidRcvd        EventType = "builder_api_get_bid_received"
	EventTypeBuilderAPIGetBidDlvd        EventType = "builder_api_get_bid_delivered"
	EventTypeBuilderAPISubmitBlockRcvd   EventType = "builder_api_submit_block_received"
	EventTypeBuilderAPISubmitBlockDlvd   EventType = "builder_api_submit_block_delivered"
	EventTypeServiceStatus               EventType = "service_status"
	EventTypeLifecycle                   EventType = "lifecycle"
	EventTypeBidIncluded                 EventType = "bid_included"
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

// PayloadBuildStartedStreamEvent is sent when payload building begins, before
// the payload is ready, so the WebUI can render the build as in-progress.
type PayloadBuildStartedStreamEvent struct {
	Slot      uint64 `json:"slot"`
	StartedAt int64  `json:"started_at"`
}

// PayloadAttributesStreamEvent is sent when a payload_attributes event is
// received from the beacon node. It arrives before the slot it targets
// (ProposalSlot), so the WebUI renders it on the parent slot's graph.
type PayloadAttributesStreamEvent struct {
	ProposalSlot       uint64 `json:"proposal_slot"`
	ProposerIndex      uint64 `json:"proposer_index"`
	ParentBlockHash    string `json:"parent_block_hash"`
	ParentBlockRoot    string `json:"parent_block_root"`
	ParentBlockNumber  uint64 `json:"parent_block_number"`
	Timestamp          uint64 `json:"timestamp"`
	FeeRecipient       string `json:"fee_recipient"`
	TargetGasLimit     uint64 `json:"target_gas_limit"`
	WithdrawalsCount   int    `json:"withdrawals_count"`
	ReceivedAt         int64  `json:"received_at"`
	InclusionListCount int    `json:"inclusion_list_count"`
}

// PayloadBuildFailedStreamEvent is sent when a payload build fails, so the WebUI
// can mark the in-progress build as failed.
type PayloadBuildFailedStreamEvent struct {
	Slot     uint64 `json:"slot"`
	Error    string `json:"error"`
	FailedAt int64  `json:"failed_at"`
}

// PayloadReadyStreamEvent is sent when a payload becomes available.
type PayloadReadyStreamEvent struct {
	Slot            uint64 `json:"slot"`
	BlockHash       string `json:"block_hash"`
	ParentBlockHash string `json:"parent_block_hash"`
	BlockValue      string `json:"block_value"`
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
	Warning   string `json:"warning,omitempty"`
}

// HeadReceivedEvent is sent when a head event is received.
type HeadReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	BlockRoot  string `json:"block_root"`
	ReceivedAt int64  `json:"received_at"`
}

// RevealStreamEvent is sent when we submit or skip a reveal (one per attempt).
type RevealStreamEvent struct {
	Slot        uint64 `json:"slot"`
	Success     bool   `json:"success"`
	Skipped     bool   `json:"skipped"`
	Error       string `json:"error,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	Timestamp   int64  `json:"timestamp"`
}

// BidStreamEvent represents a bid from any payload_builder.
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
	ValueWei        string `json:"value_wei"`
	Timestamp       int64  `json:"timestamp"`
}

// ServiceStatusEvent indicates which services are available and enabled.
type ServiceStatusEvent struct {
	EPBSAvailable         bool   `json:"epbs_available"`
	EPBSEnabled           bool   `json:"epbs_enabled"`
	EPBSRegistrationState string `json:"epbs_registration_state"`
	BuilderAPIAvailable   bool   `json:"builder_api_available"`
	BuilderAPIEnabled     bool   `json:"builder_api_enabled"`
	LifecycleAvailable    bool   `json:"lifecycle_available"`
	LifecycleEnabled      bool   `json:"lifecycle_enabled"`
}

// LifecycleStreamEvent is sent when a lifecycle action occurs (deposit, topup, exit, state change).
type LifecycleStreamEvent struct {
	Action  string `json:"action"`  // "deposit", "topup", "exit", "state_change", "waiting_gloas", "balance_topup"
	Message string `json:"message"` // Human-readable description
	Status  string `json:"status"`  // "info", "success", "warning", "error"
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

// BuilderAPIGetBidReceivedEvent is sent when a Gloas getExecutionPayloadBid request is received.
type BuilderAPIGetBidReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	ParentHash string `json:"parent_hash"`
	Pubkey     string `json:"pubkey"`
	ReceivedAt int64  `json:"received_at"`
}

// BuilderAPIGetBidDeliveredEvent is sent when a Gloas execution payload bid is delivered.
type BuilderAPIGetBidDeliveredEvent struct {
	Slot        uint64 `json:"slot"`
	BlockHash   string `json:"block_hash"`
	BlockValue  string `json:"block_value"`
	DeliveredAt int64  `json:"delivered_at"`
}

// BuilderAPISubmitBlockReceivedEvent is sent when a Gloas submitSignedBeaconBlock request is received.
type BuilderAPISubmitBlockReceivedEvent struct {
	Slot       uint64 `json:"slot"`
	BlockHash  string `json:"block_hash"`
	ReceivedAt int64  `json:"received_at"`
}

// BuilderAPISubmitBlockDeliveredEvent is sent when a Gloas envelope is successfully published.
type BuilderAPISubmitBlockDeliveredEvent struct {
	Slot        uint64 `json:"slot"`
	BlockHash   string `json:"block_hash"`
	DeliveredAt int64  `json:"delivered_at"`
}

// EventStreamManager manages SSE connections and event broadcasting.
type EventStreamManager struct {
	builderSvc    *payload_builder.Service
	epbsSvc       *p2p_bidder.Service // Optional ePBS service for bid events
	lifecycleMgr  *lifecycle.Manager  // Optional lifecycle manager for balance info
	chainSvc      chain.Service       // Optional chain service for head vote tracking
	builderAPISvc *builderapi.Server  // Optional Builder API server

	revealSvc        *payload_bidder.RevealService    // Optional shared reveal service (Gloas+)
	inclusionTracker *payload_bidder.InclusionTracker // Optional shared inclusion tracker
	payments         *payload_bidder.PaymentTracker   // Optional shared payment tracker (Gloas+)

	clients map[chan *StreamEvent]struct{}
	mu      sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

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
	builderSvc *payload_builder.Service,
	epbsSvc *p2p_bidder.Service,
	lifecycleMgr *lifecycle.Manager,
	chainSvc chain.Service,
	builderAPISvc *builderapi.Server,
	revealSvc *payload_bidder.RevealService,
	inclusionTracker *payload_bidder.InclusionTracker,
	payments *payload_bidder.PaymentTracker,
) *EventStreamManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &EventStreamManager{
		builderSvc:       builderSvc,
		epbsSvc:          epbsSvc,
		lifecycleMgr:     lifecycleMgr,
		chainSvc:         chainSvc,
		builderAPISvc:    builderAPISvc,
		revealSvc:        revealSvc,
		inclusionTracker: inclusionTracker,
		payments:         payments,
		clients:          make(map[chan *StreamEvent]struct{}, 8),
		ctx:              ctx,
		cancel:           cancel,
		slotStates:       make(map[phase0.Slot]*SlotStateEvent, 16),
	}
}

// Start begins the event stream manager.
func (m *EventStreamManager) Start() {
	// Subscribe to payload ready events
	payloadSub := m.builderSvc.SubscribePayloadReady(16)

	// Subscribe to payload build started events (in-progress rendering)
	buildStartedSub := m.builderSvc.SubscribePayloadBuildStarted(16)

	// Subscribe to payload build failed events (mark builds as failed)
	buildFailedSub := m.builderSvc.SubscribePayloadBuildFailed(16)

	// Subscribe to beacon events
	headSub := m.builderSvc.GetCLClient().Events().SubscribeHead()
	bidSub := m.builderSvc.GetCLClient().Events().SubscribeBids()
	payloadAvailSub := m.builderSvc.GetCLClient().Events().SubscribePayloadAvailable()
	payloadAttrSub := m.builderSvc.GetCLClient().Events().SubscribePayloadAttributes()

	// Subscribe to bid submission events from ePBS service (if available).
	// The subscriptions below must stay alive for the event loop's lifetime,
	// so they are unsubscribed in the goroutine's teardown, not here.
	var bidSubmitSub *utils.Subscription[*p2p_bidder.BidSubmissionEvent]

	var bidSubmitChan <-chan *p2p_bidder.BidSubmissionEvent

	if m.epbsSvc != nil {
		bidSubmitSub = m.epbsSvc.SubscribeBidSubmissions(16, false)
		bidSubmitChan = bidSubmitSub.Channel()
	}

	// Subscribe to reveal results from the shared reveal service (if available)
	var revealSub *utils.Subscription[*payload_bidder.RevealResult]

	var revealChan <-chan *payload_bidder.RevealResult

	if m.revealSvc != nil {
		revealSub = m.revealSvc.SubscribeResults(16)
		revealChan = revealSub.Channel()
	}

	// Subscribe to payload inclusion events from the shared inclusion tracker (if available)
	var bidIncludedSub *utils.Subscription[*payload_bidder.PayloadIncludedEvent]

	var bidIncludedChan <-chan *payload_bidder.PayloadIncludedEvent

	if m.inclusionTracker != nil {
		bidIncludedSub = m.inclusionTracker.SubscribeIncluded(16)
		bidIncludedChan = bidIncludedSub.Channel()
	}

	// Wire lifecycle event callback (if lifecycle manager available)
	if m.lifecycleMgr != nil {
		m.lifecycleMgr.SetEventCallback(func(event *lifecycle.LifecycleEvent) {
			m.BroadcastLifecycle(event.Action, event.Message, event.Status)
		})
	}

	// Subscribe to head vote updates (if chain service available)
	var hvSub *utils.Subscription[*chain.HeadVoteUpdate]

	var headVoteChan <-chan *chain.HeadVoteUpdate

	if m.chainSvc != nil {
		if tracker := m.chainSvc.GetHeadVoteTracker(); tracker != nil {
			hvSub = tracker.SubscribeUpdates()
			headVoteChan = hvSub.Channel()
		}
	}

	m.wg.Add(1)

	go func() {
		defer m.wg.Done()
		defer payloadSub.Unsubscribe()
		defer buildStartedSub.Unsubscribe()
		defer buildFailedSub.Unsubscribe()
		defer headSub.Unsubscribe()
		defer bidSub.Unsubscribe()
		defer payloadAvailSub.Unsubscribe()
		defer payloadAttrSub.Unsubscribe()

		if bidSubmitSub != nil {
			defer bidSubmitSub.Unsubscribe()
		}

		if revealSub != nil {
			defer revealSub.Unsubscribe()
		}

		if bidIncludedSub != nil {
			defer bidIncludedSub.Unsubscribe()
		}

		if hvSub != nil {
			defer hvSub.Unsubscribe()
		}

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

			case event := <-buildStartedSub.Channel():
				m.handlePayloadBuildStarted(event)

			case event := <-buildFailedSub.Channel():
				m.handlePayloadBuildFailed(event)

			case event := <-headSub.Channel():
				m.handleHeadEvent(event)

			case event := <-bidSub.Channel():
				m.handleBidEvent(event)

			case event := <-payloadAvailSub.Channel():
				m.handlePayloadAvailableEvent(event)

			case event := <-payloadAttrSub.Channel():
				m.handlePayloadAttributesEvent(event)

			case event, ok := <-bidSubmitChan:
				if !ok {
					// Channel closed: disable this select case (nil channels block forever).
					bidSubmitChan = nil
					continue
				}

				m.handleBidSubmissionEvent(event)

			case event, ok := <-headVoteChan:
				if !ok {
					headVoteChan = nil
					continue
				}

				m.handleHeadVoteUpdate(event)

			case event, ok := <-revealChan:
				if !ok {
					revealChan = nil
					continue
				}

				m.BroadcastReveal(event)

			case event, ok := <-bidIncludedChan:
				if !ok {
					bidIncludedChan = nil
					continue
				}

				m.Broadcast(&StreamEvent{
					Type:      EventTypeBidIncluded,
					Timestamp: time.Now().UnixMilli(),
					Data: map[string]any{
						"slot":       uint64(event.Payload.Attributes.ProposalSlot),
						"block_hash": fmt.Sprintf("0x%x", event.BlockInfo.ExecutionBlockHash[:]),
						"bid_value":  event.BidValueGwei,
					},
				})

				// The inclusion tracker's won-block record doubles as the
				// bid_won event (Builder API and p2p wins alike).
				if event.WonBlock != nil {
					m.BroadcastBidWon(event.WonBlock)
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

func (m *EventStreamManager) handlePayloadBuildStarted(event *payload_builder.PayloadBuildStartedEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadBuildStarted,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadBuildStartedStreamEvent{
			Slot:      uint64(event.Slot),
			StartedAt: event.StartedAt.UnixMilli(),
		},
	})
}

func (m *EventStreamManager) handlePayloadAttributesEvent(event *beacon.PayloadAttributesEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadAttributes,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadAttributesStreamEvent{
			ProposalSlot:       uint64(event.ProposalSlot),
			ProposerIndex:      uint64(event.ProposerIndex),
			ParentBlockHash:    fmt.Sprintf("0x%x", event.ParentBlockHash[:]),
			ParentBlockRoot:    fmt.Sprintf("0x%x", event.ParentBlockRoot[:]),
			ParentBlockNumber:  event.ParentBlockNumber,
			Timestamp:          event.Timestamp,
			FeeRecipient:       event.SuggestedFeeRecipient.Hex(),
			TargetGasLimit:     event.TargetGasLimit,
			InclusionListCount: len(event.InclusionListTransactions),
			WithdrawalsCount:   len(event.Withdrawals),
			ReceivedAt:         time.Now().UnixMilli(),
		},
	})
}

func (m *EventStreamManager) handlePayloadBuildFailed(event *payload_builder.PayloadBuildFailedEvent) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadBuildFailed,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadBuildFailedStreamEvent{
			Slot:     uint64(event.Slot),
			Error:    event.Error,
			FailedAt: event.FailedAt.UnixMilli(),
		},
	})
}

func (m *EventStreamManager) handlePayloadReady(event *payload_builder.Payload) {
	slot := event.Attributes.ProposalSlot

	m.Broadcast(&StreamEvent{
		Type:      EventTypePayloadReady,
		Timestamp: time.Now().UnixMilli(),
		Data: PayloadReadyStreamEvent{
			Slot:            uint64(slot),
			BlockHash:       fmt.Sprintf("0x%x", event.BlockHash[:]),
			ParentBlockHash: fmt.Sprintf("0x%x", event.Attributes.ParentBlockHash[:]),
			BlockValue:      event.BlockValue.String(),
			ReadyAt:         event.ReadyAt.UnixMilli(),
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	if state, ok := m.slotStates[slot]; ok {
		state.PayloadReady = true
	} else {
		m.slotStates[slot] = &SlotStateEvent{
			Slot:         uint64(slot),
			PayloadReady: true,
		}
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(slot)
}

func (m *EventStreamManager) handleBidSubmissionEvent(event *p2p_bidder.BidSubmissionEvent) {
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
			Warning:   event.Warning,
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
	// Determine if this is our own bid. The beacon node echoes our submitted bid
	// back over the SSE stream, so without this it would be shown as an external
	// bid. Match on builder index, the same way the ePBS service classifies bids.
	isOurs := m.epbsSvc != nil && event.BuilderIndex == m.epbsSvc.GetBuilderIndex()

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

	// Get builder identity, balance, and pending payments
	if m.epbsSvc != nil {
		pubkey := m.epbsSvc.GetBuilderPubkey()
		info.BuilderPubkey = pubkey.String()
		info.BuilderIndex = m.epbsSvc.GetBuilderIndex()
		info.IsRegistered = m.epbsSvc.IsRegistered()

		// Get balance and pending payments from chain state
		if m.chainSvc != nil {
			if builderInfo := m.chainSvc.GetBuilderByPubkey(pubkey); builderInfo != nil {
				info.CLBalance = builderInfo.Balance
				info.PendingPayments = builderInfo.PendingPayments
				info.DepositEpoch = builderInfo.DepositEpoch
				info.WithdrawableEpoch = builderInfo.WithdrawableEpoch
			}
		}

		// Apply local balance adjustment (topups + revealed bid deductions since last state refresh)
		if m.payments != nil {
			adjustment := m.payments.GetBalanceAdjustment()
			adjusted := int64(info.CLBalance) + adjustment
			if adjusted < 0 {
				adjusted = 0
			}

			info.CLBalance = uint64(adjusted)
		}
	}

	// Get wallet info from lifecycle manager (only when lifecycle is enabled)
	if m.lifecycleMgr != nil {
		info.LifecycleEnabled = true

		if wallet := m.lifecycleMgr.GetWallet(); wallet != nil {
			info.WalletAddress = wallet.Address().Hex()

			if balance, err := wallet.GetBalance(m.ctx); err == nil && balance != nil {
				info.WalletBalance = balance.String()
			}
		}
	}

	// Calculate effective balance (live balance minus unrevealed pending payments)
	if info.CLBalance > info.PendingPayments {
		info.EffectiveBalance = info.CLBalance - info.PendingPayments
	}

	return info
}

func (m *EventStreamManager) getServiceStatus() ServiceStatusEvent {
	regState := "unknown"
	if m.epbsSvc != nil {
		regState = p2p_bidder.RegistrationStateName(m.epbsSvc.GetRegistrationState())
	}

	return ServiceStatusEvent{
		EPBSAvailable:         m.epbsSvc != nil,
		EPBSEnabled:           m.epbsSvc != nil && m.epbsSvc.IsEnabled(),
		EPBSRegistrationState: regState,
		BuilderAPIAvailable:   m.builderAPISvc != nil,
		BuilderAPIEnabled:     m.builderAPISvc != nil && m.builderAPISvc.IsEnabled(),
		LifecycleAvailable:    m.lifecycleMgr != nil,
		LifecycleEnabled:      m.lifecycleMgr != nil && m.lifecycleMgr.IsEnabled(),
	}
}

func (m *EventStreamManager) sendServiceStatus() {
	status := m.getServiceStatus()

	m.lastServiceStatusMu.Lock()
	changed := status != m.lastServiceStatus
	prevRegState := m.lastServiceStatus.EPBSRegistrationState
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

	// Emit lifecycle log event when registration state changes
	if prevRegState != "" && prevRegState != status.EPBSRegistrationState {
		m.emitRegistrationStateChange(prevRegState, status.EPBSRegistrationState)
	}
}

// emitRegistrationStateChange emits a lifecycle event when the ePBS registration state transitions.
func (m *EventStreamManager) emitRegistrationStateChange(from, to string) {
	var message string
	var logStatus string

	switch to {
	case "unregistered":
		message = "Builder not registered on beacon chain"
		logStatus = "info"
	case "pending":
		message = "Builder deposit submitted, waiting for beacon chain inclusion"
		logStatus = "info"
	case "pending_finalization":
		message = "Builder deposit included in beacon state, waiting for finalization"
		logStatus = "info"
	case "registered":
		message = "Builder deposit finalized, builder is now active"
		logStatus = "success"
	case "exiting":
		message = "Builder exit initiated, waiting for withdrawable epoch"
		logStatus = "warning"
	case "exited":
		message = "Builder has exited"
		logStatus = "warning"
	case "waiting_gloas":
		message = "Waiting for Gloas fork activation"
		logStatus = "info"
	default:
		message = fmt.Sprintf("Registration state changed: %s -> %s", from, to)
		logStatus = "info"
	}

	m.BroadcastLifecycle("state_change", message, logStatus)
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
// Sends are ctx-aware: if the client disconnects (or shutdown is signalled)
// while the channel buffer is full, the goroutine bails out instead of
// blocking forever. This is what lets the caller safely run this in a
// goroutine alongside the SSE read loop.
func (m *EventStreamManager) SendInitialState(ctx context.Context, ch chan *StreamEvent) {
	send := func(ev *StreamEvent) bool {
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Send current config
	if !send(&StreamEvent{
		Type:      EventTypeConfig,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.builderSvc.GetConfig(),
	}) {
		return
	}

	// Send current status
	if !send(&StreamEvent{
		Type:      EventTypeStatus,
		Timestamp: time.Now().UnixMilli(),
		Data: StatusResponse{
			Running:     true,
			CurrentSlot: uint64(m.builderSvc.GetCurrentSlot()),
		},
	}) {
		return
	}

	// Send service status
	if !send(&StreamEvent{
		Type:      EventTypeServiceStatus,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.getServiceStatus(),
	}) {
		return
	}

	// Send current stats
	if !send(&StreamEvent{
		Type:      EventTypeStats,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.buildStatsResponse(),
	}) {
		return
	}

	// Send builder info
	if !send(&StreamEvent{
		Type:      EventTypeBuilderInfo,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.getBuilderInfo(),
	}) {
		return
	}

	// Send genesis and chain spec info
	genesis := m.builderSvc.GetGenesis()
	chainSpec := m.builderSvc.GetChainSpec()

	if genesis != nil && chainSpec != nil {
		if !send(&StreamEvent{
			Type:      "chain_info",
			Timestamp: time.Now().UnixMilli(),
			Data: map[string]any{
				"genesis_time":     genesis.GenesisTime.UnixMilli(),
				"seconds_per_slot": int64(chainSpec.SecondsPerSlot.Milliseconds()),
			},
		}) {
			return
		}
	}

	// Send recent slot states.
	// Snapshot under lock then release before sending: a blocking channel send
	// while holding RLock would wedge handleHeadEvent's Lock() and freeze the
	// entire event-processing goroutine.
	m.slotStatesMu.RLock()
	states := make([]SlotStateEvent, 0, len(m.slotStates))
	for _, state := range m.slotStates {
		states = append(states, *state)
	}
	m.slotStatesMu.RUnlock()

	for _, state := range states {
		if !send(&StreamEvent{
			Type:      EventTypeSlotState,
			Timestamp: time.Now().UnixMilli(),
			Data:      state,
		}) {
			return
		}
	}
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
	// Disable proxy buffering (nginx) so events flush immediately to clients.
	w.Header().Set("X-Accel-Buffering", "no")

	// Get the flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Flush response headers and a leading comment to the client immediately.
	// This makes nginx parse X-Accel-Buffering before SendInitialState has a chance
	// to block, ensuring buffering is disabled for the rest of the stream.
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Create channel for this client
	clientCh := make(chan *StreamEvent, 32)

	// Add client
	h.eventStreamMgr.AddClient(clientCh)
	// RemoveClient closes the channel, so it must run *after* SendInitialState
	// has stopped writing to it. Defers run LIFO, and we install the
	// initial-state wait below — that wait will execute first.
	defer h.eventStreamMgr.RemoveClient(clientCh)

	// Send initial state in a goroutine so the read loop below can drain the
	// channel concurrently. SendInitialState performs blocking sends; if it
	// ran inline and the 32-slot buffer filled (e.g. broadcasts piling on
	// while we're still emitting initial events), the connection would stall
	// before ever reaching the reader.
	initDone := make(chan struct{})
	go func() {
		defer close(initDone)
		h.eventStreamMgr.SendInitialState(r.Context(), clientCh)
	}()
	defer func() { <-initDone }()

	// Heartbeat keeps the connection alive past proxy idle timeouts and ensures
	// regular flushes so any intermediate buffers don't stall the stream.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Stream events
	for {
		select {
		case <-r.Context().Done():
			return

		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()

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

// BroadcastReveal broadcasts a reveal event (one per attempt, success or failure).
func (m *EventStreamManager) BroadcastReveal(event *payload_bidder.RevealResult) {
	slot := uint64(event.Slot)

	m.Broadcast(&StreamEvent{
		Type:      EventTypeReveal,
		Timestamp: time.Now().UnixMilli(),
		Data: RevealStreamEvent{
			Slot:        slot,
			Success:     event.Success,
			Skipped:     event.Skipped,
			Error:       event.Error,
			Attempt:     event.Attempt,
			MaxAttempts: event.MaxAttempts,
			Timestamp:   time.Now().UnixMilli(),
		},
	})

	// Update slot state
	m.slotStatesMu.Lock()
	state, ok := m.slotStates[event.Slot]
	if ok {
		state.Revealed = event.Success
	}
	m.slotStatesMu.Unlock()

	m.broadcastSlotState(event.Slot)
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

// BroadcastBuilderAPIGetBidReceived broadcasts when a Gloas getExecutionPayloadBid request is received.
func (m *EventStreamManager) BroadcastBuilderAPIGetBidReceived(slot uint64, parentHash, pubkey string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPIGetBidRcvd,
		Timestamp: now,
		Data: BuilderAPIGetBidReceivedEvent{
			Slot:       slot,
			ParentHash: parentHash,
			Pubkey:     pubkey,
			ReceivedAt: now,
		},
	})
}

// BroadcastBuilderAPIGetBidDelivered broadcasts when a Gloas execution payload bid is delivered.
func (m *EventStreamManager) BroadcastBuilderAPIGetBidDelivered(slot uint64, blockHash, blockValue string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPIGetBidDlvd,
		Timestamp: now,
		Data: BuilderAPIGetBidDeliveredEvent{
			Slot:        slot,
			BlockHash:   blockHash,
			BlockValue:  blockValue,
			DeliveredAt: now,
		},
	})
}

// BroadcastBuilderAPISubmitBlockReceived broadcasts when a Gloas submitSignedBeaconBlock request is received.
func (m *EventStreamManager) BroadcastBuilderAPISubmitBlockReceived(slot uint64, blockHash string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPISubmitBlockRcvd,
		Timestamp: now,
		Data: BuilderAPISubmitBlockReceivedEvent{
			Slot:       slot,
			BlockHash:  blockHash,
			ReceivedAt: now,
		},
	})
}

// BroadcastBuilderAPISubmitBlockDelivered broadcasts when a Gloas envelope is successfully published.
func (m *EventStreamManager) BroadcastBuilderAPISubmitBlockDelivered(slot uint64, blockHash string) {
	now := time.Now().UnixMilli()
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBuilderAPISubmitBlockDlvd,
		Timestamp: now,
		Data: BuilderAPISubmitBlockDeliveredEvent{
			Slot:        slot,
			BlockHash:   blockHash,
			DeliveredAt: now,
		},
	})
}

// BroadcastBidWon broadcasts a bid won event when one of our blocks is seen
// included at the head (fed by the inclusion tracker's won-block record).
func (m *EventStreamManager) BroadcastBidWon(wonBlock *payload_bidder.WonBlock) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeBidWon,
		Timestamp: time.Now().UnixMilli(),
		Data: BidWonStreamEvent{
			Slot:            wonBlock.Slot,
			BlockHash:       wonBlock.BlockHash,
			NumTransactions: wonBlock.NumTransactions,
			NumBlobs:        wonBlock.NumBlobs,
			ValueETH:        wonBlock.ValueETH,
			ValueWei:        wonBlock.ValueWei,
			Timestamp:       wonBlock.Timestamp,
		},
	})
}

// BroadcastLifecycle broadcasts a lifecycle event (deposit, exit, state change, etc.).
func (m *EventStreamManager) BroadcastLifecycle(action, message, status string) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeLifecycle,
		Timestamp: time.Now().UnixMilli(),
		Data: LifecycleStreamEvent{
			Action:  action,
			Message: message,
			Status:  status,
		},
	})
}
