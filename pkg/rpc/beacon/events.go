package beacon

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethpandaops/buildoor/pkg/utils"
)

// HeadEvent represents a head event from the beacon node.
type HeadEvent struct {
	Slot                      phase0.Slot
	Block                     phase0.Root
	State                     phase0.Root
	EpochTransition           bool
	ExecutionOptimistic       bool
	PreviousDutyDependentRoot phase0.Root
	CurrentDutyDependentRoot  phase0.Root
}

// headEventJSON is used for JSON unmarshaling of head events.
type headEventJSON struct {
	Slot                      string `json:"slot"`
	Block                     string `json:"block"`
	State                     string `json:"state"`
	EpochTransition           bool   `json:"epoch_transition"`
	ExecutionOptimistic       bool   `json:"execution_optimistic"`
	PreviousDutyDependentRoot string `json:"previous_duty_dependent_root"`
	CurrentDutyDependentRoot  string `json:"current_duty_dependent_root"`
}

// BidEvent represents an execution payload bid event.
type BidEvent struct {
	Slot               phase0.Slot
	ParentBlockHash    phase0.Hash32
	ParentBlockRoot    phase0.Root
	BlockHash          phase0.Hash32
	FeeRecipient       [20]byte
	GasLimit           uint64
	BuilderIndex       uint64
	Value              uint64
	ExecutionPayment   uint64
	BlobKZGCommitments [][]byte
	Signature          phase0.BLSSignature
	ReceivedAt         time.Time
}

// PayloadAvailableEvent represents an execution_payload_available event (Gloas).
// This is emitted when the node has verified that the execution payload and blobs
// for a block are available and ready for payload attestation.
type PayloadAvailableEvent struct {
	Slot       phase0.Slot
	BlockRoot  phase0.Root // Beacon block root this payload belongs to
	ReceivedAt time.Time
}

// PayloadAttributesEvent represents a payload_attributes event from the beacon node.
// This is emitted when a validator is scheduled to propose and contains all parameters
// needed for building an execution payload.
type PayloadAttributesEvent struct {
	Version               string
	ProposalSlot          phase0.Slot
	ProposerIndex         phase0.ValidatorIndex
	ParentBlockRoot       phase0.Root
	ParentBlockNumber     uint64
	ParentBlockHash       phase0.Hash32
	Timestamp             uint64
	PrevRandao            phase0.Root
	SuggestedFeeRecipient common.Address
	Withdrawals           []*capella.Withdrawal
	ParentBeaconBlockRoot phase0.Root
}

// payloadAttributesEventJSON is used for JSON unmarshaling of payload_attributes events.
type payloadAttributesEventJSON struct {
	Version string `json:"version"`
	Data    struct {
		ProposalSlot      string `json:"proposal_slot"`
		ProposerIndex     string `json:"proposer_index"`
		ParentBlockRoot   string `json:"parent_block_root"`
		ParentBlockNumber string `json:"parent_block_number"`
		ParentBlockHash   string `json:"parent_block_hash"`
		PayloadAttributes struct {
			Timestamp             string `json:"timestamp"`
			PrevRandao            string `json:"prev_randao"`
			SuggestedFeeRecipient string `json:"suggested_fee_recipient"`
			Withdrawals           []struct {
				Index          string `json:"index"`
				ValidatorIndex string `json:"validator_index"`
				Address        string `json:"address"`
				Amount         string `json:"amount"`
			} `json:"withdrawals"`
			ParentBeaconBlockRoot string `json:"parent_beacon_block_root"`
		} `json:"payload_attributes"`
	} `json:"data"`
}

// AttestationEvent represents an attestation event from the beacon node.
type AttestationEvent struct {
	AggregationBits []byte
	Slot            phase0.Slot
	Index           uint64
	BeaconBlockRoot phase0.Root
	CommitteeBits   []byte // nil for pre-Electra
	ReceivedAt      time.Time
}

// attestationEventJSON is used for JSON unmarshaling of attestation events.
type attestationEventJSON struct {
	AggregationBits string `json:"aggregation_bits"`
	Data            struct {
		Slot            string `json:"slot"`
		Index           string `json:"index"`
		BeaconBlockRoot string `json:"beacon_block_root"`
	} `json:"data"`
	CommitteeBits string `json:"committee_bits,omitempty"`
}

// payloadAvailableEventJSON is used for JSON unmarshaling of execution_payload_available events.
type payloadAvailableEventJSON struct {
	Slot      string `json:"slot"`
	BlockRoot string `json:"block_root"`
}

// bidEventJSON is used for JSON unmarshaling of bid events.
// The beacon node sends bids in SignedExecutionPayloadBid format with a message/signature wrapper.
type bidEventJSON struct {
	Message struct {
		Slot               string   `json:"slot"`
		ParentBlockHash    string   `json:"parent_block_hash"`
		ParentBlockRoot    string   `json:"parent_block_root"`
		BlockHash          string   `json:"block_hash"`
		PrevRandao         string   `json:"prev_randao"`
		FeeRecipient       string   `json:"fee_recipient"`
		GasLimit           string   `json:"gas_limit"`
		BuilderIndex       string   `json:"builder_index"`
		Value              string   `json:"value"`
		ExecutionPayment   string   `json:"execution_payment"`
		BlobKZGCommitments []string `json:"blob_kzg_commitments"`
	} `json:"message"`
	Signature string `json:"signature"`
}

// EventStream manages SSE connections to the beacon node event stream.
type EventStream struct {
	client                      *Client
	headDispatcher              *utils.Dispatcher[*HeadEvent]
	bidDispatcher               *utils.Dispatcher[*BidEvent]
	payloadDispatcher           *utils.Dispatcher[*PayloadAvailableEvent]
	payloadAttributesDispatcher *utils.Dispatcher[*PayloadAttributesEvent]
	attestationDispatcher       *utils.Dispatcher[*AttestationEvent]
	cancelFunc                  context.CancelFunc
	running                     bool
	mu                          sync.Mutex
	wg                          sync.WaitGroup

	// Per-slot cache of latest payload_attributes events.
	// Multiple events may arrive for the same slot (e.g. reorgs, updated attributes);
	// we always keep the latest one so the builder uses the most up-to-date data.
	payloadAttrCache   map[phase0.Slot]*PayloadAttributesEvent
	payloadAttrCacheMu sync.RWMutex
}

// NewEventStream creates a new event stream for the given client.
func NewEventStream(client *Client) *EventStream {
	return &EventStream{
		client:                      client,
		headDispatcher:              &utils.Dispatcher[*HeadEvent]{},
		bidDispatcher:               &utils.Dispatcher[*BidEvent]{},
		payloadDispatcher:           &utils.Dispatcher[*PayloadAvailableEvent]{},
		payloadAttributesDispatcher: &utils.Dispatcher[*PayloadAttributesEvent]{},
		attestationDispatcher:       &utils.Dispatcher[*AttestationEvent]{},
		payloadAttrCache:            make(map[phase0.Slot]*PayloadAttributesEvent, 4),
	}
}

// Start begins listening to beacon node events.
func (e *EventStream) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil
	}

	streamCtx, cancel := context.WithCancel(ctx)
	e.cancelFunc = cancel
	e.running = true
	e.mu.Unlock()

	// Start separate goroutines for each topic
	e.wg.Add(5)

	go e.runTopicLoop(streamCtx, "head", 5*time.Second)
	go e.runTopicLoop(streamCtx, "payload_attributes", 5*time.Second)
	go e.runTopicLoop(streamCtx, "execution_payload_bid", 30*time.Second)
	go e.runTopicLoop(streamCtx, "execution_payload_available", 30*time.Second)
	go e.runTopicLoop(streamCtx, "attestation", 5*time.Second)

	return nil
}

// Stop stops the event stream.
func (e *EventStream) Stop() {
	e.mu.Lock()
	if e.cancelFunc != nil {
		e.cancelFunc()
		e.cancelFunc = nil
	}

	e.running = false
	e.mu.Unlock()

	e.wg.Wait()
}

// SubscribeHead returns a subscription for head events.
func (e *EventStream) SubscribeHead() *utils.Subscription[*HeadEvent] {
	return e.headDispatcher.Subscribe(16, false)
}

// SubscribeBids returns a subscription for bid events.
func (e *EventStream) SubscribeBids() *utils.Subscription[*BidEvent] {
	return e.bidDispatcher.Subscribe(64, false)
}

// SubscribePayloadAvailable returns a subscription for execution_payload_available events.
func (e *EventStream) SubscribePayloadAvailable() *utils.Subscription[*PayloadAvailableEvent] {
	return e.payloadDispatcher.Subscribe(16, false)
}

// SubscribePayloadAttributes returns a subscription for payload attributes events.
func (e *EventStream) SubscribePayloadAttributes() *utils.Subscription[*PayloadAttributesEvent] {
	return e.payloadAttributesDispatcher.Subscribe(16, false)
}

// SubscribeAttestations returns a subscription for attestation events.
func (e *EventStream) SubscribeAttestations() *utils.Subscription[*AttestationEvent] {
	return e.attestationDispatcher.Subscribe(256, false)
}

// GetLatestPayloadAttributes returns the latest cached payload_attributes event
// for the given slot, or nil if none has been received.
func (e *EventStream) GetLatestPayloadAttributes(slot phase0.Slot) *PayloadAttributesEvent {
	e.payloadAttrCacheMu.RLock()
	defer e.payloadAttrCacheMu.RUnlock()

	return e.payloadAttrCache[slot]
}

// CleanupPayloadAttributesCache removes cached payload_attributes entries
// for slots older than beforeSlot.
func (e *EventStream) CleanupPayloadAttributesCache(beforeSlot phase0.Slot) {
	e.payloadAttrCacheMu.Lock()
	defer e.payloadAttrCacheMu.Unlock()

	for slot := range e.payloadAttrCache {
		if slot < beforeSlot {
			delete(e.payloadAttrCache, slot)
		}
	}
}

// runTopicLoop connects to the SSE endpoint for a specific topic and processes events.
func (e *EventStream) runTopicLoop(ctx context.Context, topic string, retryDelay time.Duration) {
	defer e.wg.Done()

	currentDelay := retryDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := e.connectAndStreamTopic(ctx, topic)
		if err != nil {
			// Check if this is a 400 error (topic not supported)
			if strings.Contains(err.Error(), "status 400") {
				e.client.log.WithField("topic", topic).Debug(
					"Topic not supported by beacon node, will retry later",
				)
				// Use longer delay for unsupported topics
				currentDelay = 60 * time.Second
			} else {
				e.client.log.WithError(err).WithField("topic", topic).Warn(
					"Event stream connection error, reconnecting...",
				)
				currentDelay = retryDelay
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(currentDelay):
				continue
			}
		}
	}
}

// connectAndStreamTopic establishes an SSE connection for a specific topic.
func (e *EventStream) connectAndStreamTopic(ctx context.Context, topic string) error {
	url := fmt.Sprintf("%s/eth/v1/events?topics=%s", e.client.baseURL, topic)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")

	httpClient := &http.Client{
		Timeout: 0, // No timeout for SSE
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to event stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("event stream returned status %d", resp.StatusCode)
	}

	e.client.log.WithField("topic", topic).Info("Connected to beacon node event stream")

	return e.processStream(ctx, resp.Body)
}

// processStream reads and processes SSE events from the response body.
func (e *EventStream) processStream(ctx context.Context, body io.Reader) error {
	reader := bufio.NewReader(body)

	var eventType string

	var eventData strings.Builder

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read from stream: %w", err)
		}

		line = strings.TrimSpace(line)

		// Empty line indicates end of event
		if line == "" {
			if eventType != "" && eventData.Len() > 0 {
				e.handleEvent(eventType, eventData.String())
			}

			eventType = ""
			eventData.Reset()

			continue
		}

		// Parse SSE format using CutPrefix for cleaner code
		if after, found := strings.CutPrefix(line, "event:"); found {
			eventType = strings.TrimSpace(after)
		} else if after, found := strings.CutPrefix(line, "data:"); found {
			eventData.WriteString(strings.TrimSpace(after))
		}
	}
}

// handleEvent processes a completed SSE event.
func (e *EventStream) handleEvent(eventType, data string) {
	switch eventType {
	case "head":
		var raw headEventJSON
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to parse head event JSON")
			return
		}

		event, err := parseHeadEvent(&raw)
		if err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to convert head event")
			return
		}

		e.headDispatcher.Fire(event)

	case "execution_payload_bid":
		var raw bidEventJSON
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to parse bid event JSON")
			return
		}

		event, err := parseBidEvent(&raw)
		if err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to convert bid event")
			return
		}

		event.ReceivedAt = time.Now()
		e.bidDispatcher.Fire(event)

	case "execution_payload_available":
		var raw payloadAvailableEventJSON
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to parse payload available event JSON")
			return
		}

		event, err := parsePayloadAvailableEvent(&raw)
		if err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to convert payload available event")
			return
		}

		event.ReceivedAt = time.Now()
		e.payloadDispatcher.Fire(event)

	case "payload_attributes":
		var raw payloadAttributesEventJSON
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to parse payload attributes event JSON")
			return
		}

		event, err := parsePayloadAttributesEvent(&raw)
		if err != nil {
			e.client.log.WithError(err).WithField("data", data).Warn("Failed to convert payload attributes event")
			return
		}

		e.client.log.WithFields(map[string]interface{}{
			"slot":        event.ProposalSlot,
			"parent_hash": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
		}).Debug("Payload attributes event received")

		// Cache the latest attributes per slot (overwrites any previous event for the same slot).
		e.payloadAttrCacheMu.Lock()
		e.payloadAttrCache[event.ProposalSlot] = event
		e.payloadAttrCacheMu.Unlock()

		e.payloadAttributesDispatcher.Fire(event)

	case "attestation":
		var raw attestationEventJSON
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			e.client.log.WithError(err).Debug("Failed to parse attestation event JSON")
			return
		}

		event, err := parseAttestationEvent(&raw)
		if err != nil {
			e.client.log.WithError(err).Debug("Failed to convert attestation event")
			return
		}

		event.ReceivedAt = time.Now()
		e.attestationDispatcher.Fire(event)

	default:
		e.client.log.WithField("event_type", eventType).Debug("Unknown event type")
	}
}

// parseHeadEvent converts a raw JSON head event to the typed HeadEvent.
func parseHeadEvent(raw *headEventJSON) (*HeadEvent, error) {
	slot, err := strconv.ParseUint(raw.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	block, err := parseRoot(raw.Block)
	if err != nil {
		return nil, fmt.Errorf("invalid block: %w", err)
	}

	state, err := parseRoot(raw.State)
	if err != nil {
		return nil, fmt.Errorf("invalid state: %w", err)
	}

	prevDuty, err := parseRoot(raw.PreviousDutyDependentRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid previous_duty_dependent_root: %w", err)
	}

	currDuty, err := parseRoot(raw.CurrentDutyDependentRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid current_duty_dependent_root: %w", err)
	}

	return &HeadEvent{
		Slot:                      phase0.Slot(slot),
		Block:                     block,
		State:                     state,
		EpochTransition:           raw.EpochTransition,
		ExecutionOptimistic:       raw.ExecutionOptimistic,
		PreviousDutyDependentRoot: prevDuty,
		CurrentDutyDependentRoot:  currDuty,
	}, nil
}

// parseBidEvent converts a raw JSON bid event to the typed BidEvent.
func parseBidEvent(raw *bidEventJSON) (*BidEvent, error) {
	msg := &raw.Message

	slot, err := strconv.ParseUint(msg.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	parentBlockHash, err := parseHash32(msg.ParentBlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_block_hash: %w", err)
	}

	parentBlockRoot, err := parseRoot(msg.ParentBlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_block_root: %w", err)
	}

	blockHash, err := parseHash32(msg.BlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid block_hash: %w", err)
	}

	builderIndex, err := strconv.ParseUint(msg.BuilderIndex, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid builder_index: %w", err)
	}

	value, err := strconv.ParseUint(msg.Value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid value: %w", err)
	}

	executionPayment, _ := strconv.ParseUint(msg.ExecutionPayment, 10, 64)
	gasLimit, _ := strconv.ParseUint(msg.GasLimit, 10, 64)

	var feeRecipient [20]byte
	frBytes := common.FromHex(msg.FeeRecipient)
	if len(frBytes) >= 20 {
		copy(feeRecipient[:], frBytes[:20])
	}

	// Parse blob KZG commitments from hex strings.
	blobCommitments := make([][]byte, 0, len(msg.BlobKZGCommitments))
	for _, commitmentHex := range msg.BlobKZGCommitments {
		b, err := hex.DecodeString(strings.TrimPrefix(commitmentHex, "0x"))
		if err != nil {
			return nil, fmt.Errorf("invalid blob_kzg_commitment: %w", err)
		}

		blobCommitments = append(blobCommitments, b)
	}

	signature, err := parseSignature(raw.Signature)
	if err != nil {
		return nil, fmt.Errorf("invalid signature: %w", err)
	}

	return &BidEvent{
		Slot:               phase0.Slot(slot),
		ParentBlockHash:    parentBlockHash,
		ParentBlockRoot:    parentBlockRoot,
		BlockHash:          blockHash,
		FeeRecipient:       feeRecipient,
		GasLimit:           gasLimit,
		BuilderIndex:       builderIndex,
		Value:              value,
		ExecutionPayment:   executionPayment,
		BlobKZGCommitments: blobCommitments,
		Signature:          signature,
	}, nil
}

// parseRoot parses a hex string (with 0x prefix) into a phase0.Root.
func parseRoot(s string) (phase0.Root, error) {
	var root phase0.Root

	s = strings.TrimPrefix(s, "0x")

	b, err := hex.DecodeString(s)
	if err != nil {
		return root, err
	}

	if len(b) != 32 {
		return root, fmt.Errorf("invalid root length: got %d, want 32", len(b))
	}

	copy(root[:], b)

	return root, nil
}

// parseHash32 parses a hex string (with 0x prefix) into a phase0.Hash32.
func parseHash32(s string) (phase0.Hash32, error) {
	var hash phase0.Hash32

	s = strings.TrimPrefix(s, "0x")

	b, err := hex.DecodeString(s)
	if err != nil {
		return hash, err
	}

	if len(b) != 32 {
		return hash, fmt.Errorf("invalid hash length: got %d, want 32", len(b))
	}

	copy(hash[:], b)

	return hash, nil
}

// parseSignature parses a hex string (with 0x prefix) into a phase0.BLSSignature.
func parseSignature(s string) (phase0.BLSSignature, error) {
	var sig phase0.BLSSignature

	s = strings.TrimPrefix(s, "0x")

	b, err := hex.DecodeString(s)
	if err != nil {
		return sig, err
	}

	if len(b) != 96 {
		return sig, fmt.Errorf("invalid signature length: got %d, want 96", len(b))
	}

	copy(sig[:], b)

	return sig, nil
}

// parsePayloadAvailableEvent converts a raw JSON execution_payload_available event to typed event.
func parsePayloadAvailableEvent(raw *payloadAvailableEventJSON) (*PayloadAvailableEvent, error) {
	slot, err := strconv.ParseUint(raw.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	blockRoot, err := parseRoot(raw.BlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid block_root: %w", err)
	}

	return &PayloadAvailableEvent{
		Slot:      phase0.Slot(slot),
		BlockRoot: blockRoot,
	}, nil
}

// parsePayloadAttributesEvent converts a raw JSON payload attributes event to typed event.
func parsePayloadAttributesEvent(raw *payloadAttributesEventJSON) (*PayloadAttributesEvent, error) {
	slot, err := strconv.ParseUint(raw.Data.ProposalSlot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid proposal_slot: %w", err)
	}

	proposerIndex, err := strconv.ParseUint(raw.Data.ProposerIndex, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid proposer_index: %w", err)
	}

	parentBlockRoot, err := parseRoot(raw.Data.ParentBlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_block_root: %w", err)
	}

	parentBlockNumber, _ := strconv.ParseUint(raw.Data.ParentBlockNumber, 10, 64)

	parentBlockHash, err := parseHash32(raw.Data.ParentBlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_block_hash: %w", err)
	}

	timestamp, err := strconv.ParseUint(raw.Data.PayloadAttributes.Timestamp, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}

	prevRandao, err := parseRoot(raw.Data.PayloadAttributes.PrevRandao)
	if err != nil {
		return nil, fmt.Errorf("invalid prev_randao: %w", err)
	}

	parentBeaconBlockRoot, err := parseRoot(raw.Data.PayloadAttributes.ParentBeaconBlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid parent_beacon_block_root: %w", err)
	}

	// Parse withdrawals
	withdrawals := make([]*capella.Withdrawal, len(raw.Data.PayloadAttributes.Withdrawals))
	for i, w := range raw.Data.PayloadAttributes.Withdrawals {
		index, _ := strconv.ParseUint(w.Index, 10, 64)
		validatorIndex, _ := strconv.ParseUint(w.ValidatorIndex, 10, 64)
		amount, _ := strconv.ParseUint(w.Amount, 10, 64)

		var address [20]byte
		addrBytes := common.FromHex(w.Address)
		if len(addrBytes) >= 20 {
			copy(address[:], addrBytes[:20])
		}

		withdrawals[i] = &capella.Withdrawal{
			Index:          capella.WithdrawalIndex(index),
			ValidatorIndex: phase0.ValidatorIndex(validatorIndex),
			Address:        address,
			Amount:         phase0.Gwei(amount),
		}
	}

	return &PayloadAttributesEvent{
		Version:               raw.Version,
		ProposalSlot:          phase0.Slot(slot),
		ProposerIndex:         phase0.ValidatorIndex(proposerIndex),
		ParentBlockRoot:       parentBlockRoot,
		ParentBlockNumber:     parentBlockNumber,
		ParentBlockHash:       parentBlockHash,
		Timestamp:             timestamp,
		PrevRandao:            prevRandao,
		SuggestedFeeRecipient: common.HexToAddress(raw.Data.PayloadAttributes.SuggestedFeeRecipient),
		Withdrawals:           withdrawals,
		ParentBeaconBlockRoot: parentBeaconBlockRoot,
	}, nil
}

// parseAttestationEvent converts a raw JSON attestation event to the typed AttestationEvent.
func parseAttestationEvent(raw *attestationEventJSON) (*AttestationEvent, error) {
	slot, err := strconv.ParseUint(raw.Data.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	index, err := strconv.ParseUint(raw.Data.Index, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid index: %w", err)
	}

	blockRoot, err := parseRoot(raw.Data.BeaconBlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid beacon_block_root: %w", err)
	}

	aggBits, err := hex.DecodeString(strings.TrimPrefix(raw.AggregationBits, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid aggregation_bits: %w", err)
	}

	event := &AttestationEvent{
		AggregationBits: aggBits,
		Slot:            phase0.Slot(slot),
		Index:           index,
		BeaconBlockRoot: blockRoot,
	}

	if raw.CommitteeBits != "" {
		cbBits, err := hex.DecodeString(strings.TrimPrefix(raw.CommitteeBits, "0x"))
		if err != nil {
			return nil, fmt.Errorf("invalid committee_bits: %w", err)
		}

		event.CommitteeBits = cbBits
	}

	return event, nil
}
