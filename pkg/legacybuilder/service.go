package legacybuilder

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// Service is the legacy builder service that polls relays for upcoming proposals,
// builds payloads with builder payment transactions, and submits signed block bids.
type Service struct {
	cfg            *config.LegacyBuilderConfig
	relayClient    *RelayClient
	blockSubmitter *BlockSubmitter
	nonceManager   *NonceManager
	paymentBuilder *PaymentBuilder
	validatorCache *ValidatorCache
	engineClient   *engine.Client
	clClient       *beacon.Client
	chainSvc       chain.Service
	wallet         *wallet.Wallet
	rpcClient      *execution.Client
	blsSigner      *signer.BLSSigner
	builderPubkey  phase0.BLSPubKey
	feeRecipient   common.Address // builder wallet address

	// Event dispatching
	submissionDispatch *utils.Dispatcher[*BlockSubmissionEvent]

	// Statistics
	stats   LegacyBuilderStats
	statsMu sync.RWMutex

	// Build tracking
	buildStartedSlots map[phase0.Slot]bool
	buildMu           sync.Mutex

	// Runtime toggle
	enabled atomic.Bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewService creates a new legacy builder service.
func NewService(
	cfg *config.LegacyBuilderConfig,
	clClient *beacon.Client,
	chainSvc chain.Service,
	engineClient *engine.Client,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	rpcClient *execution.Client,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "legacy-builder")

	relayClient := NewRelayClient(cfg.RelayURLs, serviceLog)
	validatorCache := NewValidatorCache()
	nonceManager := NewNonceManager(w, serviceLog)
	paymentBuilder := NewPaymentBuilder(w, serviceLog)

	builderPubkey := blsSigner.PublicKey()

	// Get genesis info for signing
	genesis := chainSvc.GetGenesis()
	if genesis == nil {
		return nil, fmt.Errorf("genesis not available")
	}

	chainSpec := chainSvc.GetChainSpec()
	if chainSpec == nil {
		return nil, fmt.Errorf("chain spec not available")
	}

	blockSubmitter := NewBlockSubmitter(
		blsSigner,
		relayClient,
		genesis.GenesisValidatorsRoot,
		chainSpec.GenesisForkVersion,
		builderPubkey,
		serviceLog,
	)

	return &Service{
		cfg:                cfg,
		relayClient:        relayClient,
		blockSubmitter:     blockSubmitter,
		nonceManager:       nonceManager,
		paymentBuilder:     paymentBuilder,
		validatorCache:     validatorCache,
		engineClient:       engineClient,
		clClient:           clClient,
		chainSvc:           chainSvc,
		wallet:             w,
		rpcClient:          rpcClient,
		blsSigner:          blsSigner,
		builderPubkey:      builderPubkey,
		feeRecipient:       w.Address(),
		submissionDispatch: &utils.Dispatcher[*BlockSubmissionEvent]{},
		buildStartedSlots:  make(map[phase0.Slot]bool),
		log:                serviceLog,
	}, nil
}

// Start initializes and starts the legacy builder service.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Sync wallet nonce
	if err := s.wallet.Sync(s.ctx); err != nil {
		return fmt.Errorf("failed to sync wallet: %w", err)
	}

	if err := s.nonceManager.Sync(s.ctx); err != nil {
		return fmt.Errorf("failed to sync nonce: %w", err)
	}

	// Initial validator poll
	s.pollValidatorRegistrations()

	// Enable by default
	s.enabled.Store(true)

	// Start relay polling loop
	s.wg.Add(1)

	go s.relayPollingLoop()

	// Start main event loop
	s.wg.Add(1)

	go s.run()

	s.log.WithFields(logrus.Fields{
		"relays":        len(s.cfg.RelayURLs),
		"fee_recipient": s.feeRecipient.Hex(),
		"payment_mode":  s.cfg.PaymentMode,
	}).Info("Legacy builder service started")

	return nil
}

// Stop stops the legacy builder service.
func (s *Service) Stop() {
	s.log.Info("Stopping legacy builder service")

	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Legacy builder service stopped")
}

// SubscribeBlockSubmissions subscribes to block submission events.
func (s *Service) SubscribeBlockSubmissions(
	capacity int,
) *utils.Subscription[*BlockSubmissionEvent] {
	return s.submissionDispatch.Subscribe(capacity, false)
}

// GetStats returns the current statistics.
func (s *Service) GetStats() LegacyBuilderStats {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()

	stats := s.stats
	stats.ValidatorsTracked = uint64(s.validatorCache.Len())

	return stats
}

// GetConfig returns the current configuration.
func (s *Service) GetConfig() *config.LegacyBuilderConfig {
	return s.cfg
}

// SetEnabled sets whether the legacy builder is actively building/submitting.
func (s *Service) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)

	if enabled {
		s.log.Info("Legacy builder service enabled")
	} else {
		s.log.Info("Legacy builder service disabled")
	}
}

// IsEnabled returns whether the legacy builder is actively building/submitting.
func (s *Service) IsEnabled() bool {
	return s.enabled.Load()
}

// UpdateConfig updates the service configuration at runtime.
func (s *Service) UpdateConfig(cfg *config.LegacyBuilderConfig) {
	s.cfg = cfg
	s.log.Info("Legacy builder configuration updated")
}

// run is the main event loop.
func (s *Service) run() {
	defer s.wg.Done()

	payloadAttrSub := s.clClient.Events().SubscribePayloadAttributes()
	defer payloadAttrSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-payloadAttrSub.Channel():
			s.handlePayloadAttributes(event)
		}
	}
}

// relayPollingLoop periodically polls relays for validator registrations.
func (s *Service) relayPollingLoop() {
	defer s.wg.Done()

	pollInterval := time.Duration(s.cfg.ValidatorPollSecs) * time.Second
	if pollInterval <= 0 {
		pollInterval = 60 * time.Second
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.pollValidatorRegistrations()
		}
	}
}

// pollValidatorRegistrations fetches validator registrations from all relays.
func (s *Service) pollValidatorRegistrations() {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	regs, err := s.relayClient.GetValidatorRegistrations(ctx)
	if err != nil {
		s.log.WithError(err).Warn("Failed to poll validator registrations")
		return
	}

	s.validatorCache.Update(regs)

	s.log.WithField("count", len(regs)).Debug("Updated validator registrations cache")
}

// handlePayloadAttributes processes a payload_attributes event.
func (s *Service) handlePayloadAttributes(event *beacon.PayloadAttributesEvent) {
	if !s.enabled.Load() {
		return
	}

	// Check if the proposer for this slot is registered on a relay
	reg := s.validatorCache.GetRegistration(event.ProposalSlot)
	if reg == nil {
		s.log.WithField("slot", event.ProposalSlot).Debug(
			"No relay registration for slot proposer, skipping legacy build",
		)
		return
	}

	// Check if already building for this slot
	s.buildMu.Lock()
	if s.buildStartedSlots[event.ProposalSlot] {
		s.buildMu.Unlock()
		return
	}

	s.buildStartedSlots[event.ProposalSlot] = true
	s.buildMu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":        event.ProposalSlot,
		"proposer":    reg.Entry.Message.Pubkey[:16],
		"parent_hash": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
	}).Info("Relay-registered proposer found, scheduling legacy build")

	s.scheduleBuildForSlot(event.ProposalSlot, event, reg)
}

// scheduleBuildForSlot schedules a payload build at the configured BuildStartTime.
func (s *Service) scheduleBuildForSlot(
	slot phase0.Slot,
	event *beacon.PayloadAttributesEvent,
	reg *ValidatorRegistration,
) {
	buildStartMs := s.cfg.BuildStartTime
	slotStart := s.chainSvc.SlotToTime(slot)
	buildTime := slotStart.Add(time.Duration(buildStartMs) * time.Millisecond)
	delay := time.Until(buildTime)

	if delay <= 0 {
		go s.executeBuildForSlot(slot, event, reg)
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":     slot,
		"delay_ms": delay.Milliseconds(),
	}).Debug("Scheduling legacy build for slot")

	time.AfterFunc(delay, func() {
		s.executeBuildForSlot(slot, event, reg)
	})
}

// executeBuildForSlot builds a payload with a builder payment tx and submits to relays.
func (s *Service) executeBuildForSlot(
	slot phase0.Slot,
	attrs *beacon.PayloadAttributesEvent,
	reg *ValidatorRegistration,
) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	// Use the latest payload attributes for this slot.
	// The parent hash may have changed since the build was scheduled (e.g. new head received),
	// so always fetch the most recent attributes from the beacon cache.
	latestAttrs := s.clClient.Events().GetLatestPayloadAttributes(slot)
	if latestAttrs != nil {
		attrs = latestAttrs
	}

	// Parse proposer's fee recipient from registration
	proposerFeeRecipient := common.HexToAddress(reg.Entry.Message.FeeRecipient)

	// Calculate payment amount
	paymentAmount, err := s.calculatePaymentAmount(ctx, attrs)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error(
			"Failed to calculate payment amount",
		)
		return
	}

	// Sync nonce from chain before building. Payment txs are included via BuilderTxs,
	// so they only land on-chain if our block is proposed. We must not speculatively
	// increment the nonce — always use the confirmed on-chain nonce.
	if err := s.nonceManager.Sync(ctx); err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to sync nonce before build")
	}

	nonce := s.nonceManager.GetBaseNonce()

	// Get chain parameters for the payment tx
	chainID, err := s.rpcClient.GetChainID(ctx)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to get chain ID")
		return
	}

	gasTipCap, err := s.rpcClient.SuggestGasTipCap(ctx)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to get gas tip cap")
		return
	}

	header, err := s.rpcClient.HeaderByNumber(ctx, nil)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to get latest header")
		return
	}

	gasFeeCap := new(big.Int).Mul(header.BaseFee, big.NewInt(2))
	gasFeeCap.Add(gasFeeCap, gasTipCap)

	gasLimit := s.cfg.PaymentGasLimit
	if gasLimit == 0 {
		gasLimit = 21000
	}

	// Create the payment transaction
	paymentTx, err := s.paymentBuilder.CreatePaymentTx(
		proposerFeeRecipient,
		paymentAmount,
		nonce,
		chainID,
		gasLimit,
		gasFeeCap,
		gasTipCap,
	)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to create payment tx")
		return
	}

	// Build payload with BuilderTxs
	executionPayload, blobsBundle, _, execRequests, consensusVersion, err := s.buildPayloadWithBuilderTxs(
		ctx, attrs, []*types.Transaction{paymentTx},
	)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error(
			"Failed to build payload with payment",
		)
		return
	}

	// Parse payload fields for BidTrace
	payloadFields, err := engine.ParsePayloadFields(executionPayload)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to parse payload fields")
		return
	}

	blockHash := common.HexToHash(payloadFields.BlockHash)
	parentHash := common.HexToHash(payloadFields.ParentHash)

	payloadGasLimit, _ := strconv.ParseUint(
		strings.TrimPrefix(payloadFields.GasLimit, "0x"), 16, 64,
	)
	payloadGasUsed, _ := strconv.ParseUint(
		strings.TrimPrefix(payloadFields.GasUsed, "0x"), 16, 64,
	)

	// Parse proposer pubkey
	proposerPubkeyBytes := common.FromHex(reg.Entry.Message.Pubkey)

	var proposerPubkey [48]byte

	copy(proposerPubkey[:], proposerPubkeyBytes)

	// Create BidTrace
	trace := &BidTrace{
		Slot:                 uint64(slot),
		ParentHash:           [32]byte(parentHash),
		BlockHash:            [32]byte(blockHash),
		BuilderPubkey:        [48]byte(s.builderPubkey),
		ProposerPubkey:       proposerPubkey,
		ProposerFeeRecipient: [20]byte(proposerFeeRecipient),
		GasLimit:             payloadGasLimit,
		GasUsed:              payloadGasUsed,
		Value:                paymentAmount,
	}

	// Submit to relays
	results, err := s.blockSubmitter.Submit(ctx, trace, executionPayload, blobsBundle, execRequests, consensusVersion)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to submit block")

		s.incrementStat(func(stats *LegacyBuilderStats) {
			stats.SubmissionFailures++
		})

		return
	}

	// Process results
	for _, result := range results {
		submissionEvent := &BlockSubmissionEvent{
			Slot:           slot,
			BlockHash:      blockHash.Hex(),
			Value:          paymentAmount.String(),
			ProposerPubkey: reg.Entry.Message.Pubkey,
			RelayURL:       result.RelayURL,
			Success:        result.Success,
			Error:          result.Error,
			Timestamp:      time.Now(),
		}

		s.submissionDispatch.Fire(submissionEvent)

		if result.Success {
			s.incrementStat(func(stats *LegacyBuilderStats) {
				stats.BlocksSubmitted++
			})

			s.log.WithFields(logrus.Fields{
				"slot":  slot,
				"relay": result.RelayURL,
				"value": paymentAmount.String(),
			}).Info("Block submitted to relay successfully")
		} else {
			s.incrementStat(func(stats *LegacyBuilderStats) {
				stats.SubmissionFailures++
			})

			s.log.WithFields(logrus.Fields{
				"slot":  slot,
				"relay": result.RelayURL,
				"error": result.Error,
			}).Warn("Block submission to relay failed")
		}
	}

	// Cleanup old tracking
	if slot > 64 {
		cleanupSlot := slot - 64
		s.validatorCache.Cleanup(cleanupSlot)
		s.clClient.Events().CleanupPayloadAttributesCache(cleanupSlot)

		s.buildMu.Lock()
		for oldSlot := range s.buildStartedSlots {
			if oldSlot < cleanupSlot {
				delete(s.buildStartedSlots, oldSlot)
			}
		}
		s.buildMu.Unlock()
	}
}

// calculatePaymentAmount determines the payment to the proposer based on mode.
func (s *Service) calculatePaymentAmount(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
) (*big.Int, error) {
	switch s.cfg.PaymentMode {
	case "fixed":
		amount, ok := new(big.Int).SetString(s.cfg.FixedPayment, 10)
		if !ok {
			return nil, fmt.Errorf("invalid fixed payment amount: %s", s.cfg.FixedPayment)
		}

		return amount, nil

	case "percentage":
		// Build without payment tx to estimate block value
		estimatedValue, err := s.estimateBlockValue(ctx, attrs)
		if err != nil {
			return nil, fmt.Errorf("failed to estimate block value: %w", err)
		}

		// Calculate payment as percentage (basis points: 10000 = 100%)
		payment := new(big.Int).Mul(
			estimatedValue,
			big.NewInt(int64(s.cfg.PaymentPercentage)),
		)
		payment.Div(payment, big.NewInt(10000))

		s.log.WithFields(logrus.Fields{
			"slot":            attrs.ProposalSlot,
			"estimated_value": estimatedValue.String(),
			"percentage_bps":  s.cfg.PaymentPercentage,
			"payment":         payment.String(),
		}).Debug("Calculated percentage payment")

		return payment, nil

	default:
		return nil, fmt.Errorf("unknown payment mode: %s", s.cfg.PaymentMode)
	}
}

// estimateBlockValue builds a payload without payment to estimate block value.
func (s *Service) estimateBlockValue(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
) (*big.Int, error) {
	finalityInfo, err := s.clClient.GetFinalityInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	headBlockHash := common.BytesToHash(attrs.ParentBlockHash[:])
	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])
	parentBeaconRoot := common.BytesToHash(attrs.ParentBeaconBlockRoot[:])

	withdrawals := convertCLWithdrawals(attrs)

	payloadID, err := s.engineClient.RequestPayloadBuild(
		ctx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             attrs.Timestamp,
			PrevRandao:            common.BytesToHash(attrs.PrevRandao[:]),
			SuggestedFeeRecipient: s.feeRecipient,
			Withdrawals:           withdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request estimation payload: %w", err)
	}

	_, blockValue, _, err := s.engineClient.GetPayloadRaw(ctx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get estimation payload: %w", err)
	}

	if blockValue == nil {
		return big.NewInt(0), nil
	}

	return blockValue, nil
}

// buildPayloadWithBuilderTxs builds a payload with builder-injected transactions.
// Returns the execution payload, blobs bundle, block value, execution requests,
// consensus version string, and an error.
func (s *Service) buildPayloadWithBuilderTxs(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	builderTxs []*types.Transaction,
) (json.RawMessage, json.RawMessage, *big.Int, [][]byte, string, error) {
	finalityInfo, err := s.clClient.GetFinalityInfo(ctx)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("failed to get finality info: %w", err)
	}

	headBlockHash := common.BytesToHash(attrs.ParentBlockHash[:])
	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])
	parentBeaconRoot := common.BytesToHash(attrs.ParentBeaconBlockRoot[:])

	withdrawals := convertCLWithdrawals(attrs)

	payloadID, err := s.engineClient.RequestPayloadBuild(
		ctx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             attrs.Timestamp,
			PrevRandao:            common.BytesToHash(attrs.PrevRandao[:]),
			SuggestedFeeRecipient: s.feeRecipient,
			Withdrawals:           withdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
			BuilderTxs:            builderTxs,
		},
	)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("failed to request payload build: %w", err)
	}

	executionPayload, blobsBundle, blockValue, execRequests, consensusVersion, err := s.engineClient.GetPayloadRawFull(
		ctx, payloadID,
	)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("failed to get payload: %w", err)
	}

	// Modify extra data to brand the block and produce a unique block hash
	executionPayload, _, err = engine.ModifyPayloadExtraData(
		executionPayload, []byte("buildoor/"), parentBeaconRoot, execRequests,
	)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("failed to modify payload extra data: %w", err)
	}

	return executionPayload, blobsBundle, blockValue, execRequests, consensusVersion, nil
}

// convertCLWithdrawals converts CL withdrawals to engine API format.
func convertCLWithdrawals(attrs *beacon.PayloadAttributesEvent) []*types.Withdrawal {
	if attrs.Withdrawals == nil {
		return make([]*types.Withdrawal, 0)
	}

	result := make([]*types.Withdrawal, len(attrs.Withdrawals))

	for i, w := range attrs.Withdrawals {
		result[i] = &types.Withdrawal{
			Index:     uint64(w.Index),
			Validator: uint64(w.ValidatorIndex),
			Address:   common.Address(w.Address),
			Amount:    uint64(w.Amount),
		}
	}

	return result
}

func (s *Service) incrementStat(fn func(*LegacyBuilderStats)) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	fn(&s.stats)
}
