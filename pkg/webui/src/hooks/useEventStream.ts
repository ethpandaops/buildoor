import { useState, useEffect, useCallback, useRef, useSyncExternalStore } from 'react';
import type { Config, ChainInfo, Stats, SlotState, LogEvent, OurBid, ExternalBid, BuilderInfo, HeadVoteDataPoint, ServiceStatus, RevealAttempt, VoteCoverage } from '../types';

// ---------------------------------------------------------------------------
// Module-level SSE fan-out: lets other hooks/components subscribe to raw
// stream events through the page's EXISTING EventSource connection instead of
// opening a second one. Every event handled by useEventStream is also
// dispatched to the callbacks registered for its type.
// ---------------------------------------------------------------------------

type StreamEventCallback = (data: unknown) => void;

const streamEventListeners = new Map<string, Set<StreamEventCallback>>();

/**
 * Subscribe to raw SSE events of one type on the shared connection.
 * Returns an unsubscribe function.
 */
export function onStreamEvent(type: string, cb: StreamEventCallback): () => void {
  let set = streamEventListeners.get(type);
  if (!set) {
    set = new Set();
    streamEventListeners.set(type, set);
  }
  set.add(cb);
  return () => {
    const listeners = streamEventListeners.get(type);
    if (!listeners) return;
    listeners.delete(cb);
    if (listeners.size === 0) {
      streamEventListeners.delete(type);
    }
  };
}

function dispatchStreamEvent(type: string, data: unknown) {
  const listeners = streamEventListeners.get(type);
  if (!listeners) return;
  // Copy so callbacks may unsubscribe (or subscribe) during dispatch.
  for (const cb of [...listeners]) {
    try {
      cb(data);
    } catch (err) {
      console.error(`Stream event callback failed for '${type}':`, err);
    }
  }
}

// Connection generation: incremented on every successful (re)connect of the
// SSE stream, so views can refetch REST state after a reconnect (events that
// occurred during the gap were never delivered).
let connectionGeneration = 0;
const generationListeners = new Set<() => void>();

export function getConnectionGeneration(): number {
  return connectionGeneration;
}

export function subscribeConnectionGeneration(listener: () => void): () => void {
  generationListeners.add(listener);
  return () => {
    generationListeners.delete(listener);
  };
}

function bumpConnectionGeneration() {
  connectionGeneration++;
  generationListeners.forEach((listener) => listener());
}

interface UseEventStreamResult {
  connected: boolean;
  config: Config | null;
  chainInfo: ChainInfo | null;
  stats: Stats | null;
  builderInfo: BuilderInfo | null;
  serviceStatus: ServiceStatus | null;
  voteCoverage: VoteCoverage | null;
  currentSlot: number;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  slotServiceStatuses: Record<number, ServiceStatus>;
  events: LogEvent[];
  clearEvents: () => void;
  connectionGeneration: number;
}

export function useEventStream(): UseEventStreamResult {
  const [connected, setConnected] = useState(false);
  const [config, setConfig] = useState<Config | null>(null);
  const [chainInfo, setChainInfo] = useState<ChainInfo | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [builderInfo, setBuilderInfo] = useState<BuilderInfo | null>(null);
  const [serviceStatus, setServiceStatus] = useState<ServiceStatus | null>(null);
  const [voteCoverage, setVoteCoverage] = useState<VoteCoverage | null>(null);
  const [currentSlot, setCurrentSlot] = useState(0);
  const [slotStates, setSlotStates] = useState<Record<number, SlotState>>({});
  const [slotConfigs, setSlotConfigs] = useState<Record<number, Config>>({});
  const [slotServiceStatuses, setSlotServiceStatuses] = useState<Record<number, ServiceStatus>>({});
  const [events, setEvents] = useState<LogEvent[]>([]);
  const eventIdRef = useRef(0);
  // Highest server-side sequence number processed so far. On connect the
  // server bursts a replay of the last slots' events (each carrying the seq
  // it was originally broadcast with); after a reconnect, anything at or
  // below this watermark is a duplicate we already processed, while replayed
  // events above it fill the gap the connection loss created.
  const lastSeqRef = useRef(0);

  // Use refs to access current values in event handlers without causing reconnection
  const configRef = useRef<Config | null>(null);
  const serviceStatusRef = useRef<ServiceStatus | null>(null);
  const chainInfoRef = useRef<ChainInfo | null>(null);
  const currentSlotRef = useRef(0);
  const eventSourceRef = useRef<EventSource | null>(null);

  // Keep refs in sync with state
  useEffect(() => { configRef.current = config; }, [config]);
  useEffect(() => { serviceStatusRef.current = serviceStatus; }, [serviceStatus]);
  useEffect(() => { chainInfoRef.current = chainInfo; }, [chainInfo]);
  useEffect(() => { currentSlotRef.current = currentSlot; }, [currentSlot]);

  // Calculate actual current slot from chain time
  useEffect(() => {
    if (!chainInfo) return;

    const calculateCurrentSlot = () => {
      const now = Date.now();
      const elapsed = now - chainInfo.genesis_time;
      const slot = Math.floor(elapsed / chainInfo.seconds_per_slot);
      if (slot !== currentSlotRef.current) {
        setCurrentSlot(slot);
      }
    };

    // Calculate immediately
    calculateCurrentSlot();

    // Update every 100ms
    const interval = setInterval(calculateCurrentSlot, 100);

    return () => clearInterval(interval);
  }, [chainInfo]);

  const clearEvents = useCallback(() => {
    setEvents([]);
  }, []);

  // Single stable effect for EventSource connection
  useEffect(() => {
    const addEvent = (type: string, message: string, timestamp: number) => {
      setEvents(prev => {
        const newEvents = [{ id: eventIdRef.current++, type, message, timestamp }, ...prev];
        // Hard cap to bound memory; must be >= the EventLog max scrollback
        // (10000) so the configured scrollback can actually be reached.
        return newEvents.slice(0, 10000);
      });
    };

    const updateSlotState = (slot: number, updates: Partial<SlotState>) => {
      setSlotStates(prev => ({
        ...prev,
        [slot]: { ...prev[slot], slot, ...updates }
      }));
    };

    const handleEvent = (event: { type: string; timestamp: number; seq?: number; data: unknown }) => {
      // Drop already-processed events from a reconnect replay. Events
      // without a seq (per-client initial-state snapshots) always pass.
      if (event.seq) {
        if (event.seq <= lastSeqRef.current) return;
        lastSeqRef.current = event.seq;
      }

      switch (event.type) {
        case 'config':
          setConfig(event.data as Config);
          break;

        case 'status': {
          const status = event.data as { current_slot: number };
          // Only use server's slot if we don't have chain info yet to calculate ourselves
          if (!chainInfoRef.current) {
            setCurrentSlot(status.current_slot);
          }
          break;
        }

        case 'chain_info':
          setChainInfo(event.data as ChainInfo);
          break;

        case 'stats':
          setStats(event.data as Stats);
          break;

        case 'builder_info':
          setBuilderInfo(event.data as BuilderInfo);
          break;

        case 'service_status':
          setServiceStatus(event.data as ServiceStatus);
          break;

        case 'vote_coverage':
          setVoteCoverage(event.data as VoteCoverage);
          break;

        case 'slot_start': {
          const data = event.data as { slot: number; slot_start_time: number };
          // Don't update currentSlot here - it's the "next" slot being prepared
          // Store config and service status snapshots for this slot
          setSlotConfigs(prev => {
            const currentConfig = configRef.current;
            if (currentConfig) {
              return { ...prev, [data.slot]: JSON.parse(JSON.stringify(currentConfig)) };
            }
            return prev;
          });
          setSlotServiceStatuses(prev => {
            const currentSS = serviceStatusRef.current;
            if (currentSS) {
              return { ...prev, [data.slot]: { ...currentSS } };
            }
            return prev;
          });
          updateSlotState(data.slot, { slotStartTime: data.slot_start_time, scheduled: true });

          // Calculate actual slot from time for the log
          const info = chainInfoRef.current;
          if (info) {
            const now = Date.now();
            const elapsed = now - info.genesis_time;
            const actualSlot = Math.floor(elapsed / info.seconds_per_slot);
            addEvent('slot_start', `Preparing for slot ${data.slot} (current: ${actualSlot})`, event.timestamp);
          } else {
            addEvent('slot_start', `Preparing for slot ${data.slot}`, event.timestamp);
          }
          break;
        }

        case 'payload_attributes': {
          const data = event.data as {
            proposal_slot: number;
            proposer_index: number;
            parent_block_hash: string;
            parent_block_root: string;
            parent_block_number: number;
            timestamp: number;
            prev_randao?: string;
            fee_recipient: string;
            parent_beacon_block_root?: string;
            target_gas_limit: number;
            withdrawals_count: number;
            inclusion_list_count?: number;
            received_at: number;
          };
          addEvent('payload_attributes', `Payload attributes received for slot ${data.proposal_slot}`, event.timestamp);
          // The attributes target proposal_slot but arrive before it, so render
          // them on the parent slot's graph (proposal_slot - 1). The CL emits one
          // per head update, so append each rather than overwriting — every one
          // is rendered as its own dot.
          {
            const parentSlot = data.proposal_slot - 1;
            const info = {
              proposalSlot: data.proposal_slot,
              proposerIndex: data.proposer_index,
              parentBlockHash: data.parent_block_hash,
              parentBlockRoot: data.parent_block_root,
              parentBlockNumber: data.parent_block_number,
              timestamp: data.timestamp,
              prevRandao: data.prev_randao,
              feeRecipient: data.fee_recipient,
              parentBeaconBlockRoot: data.parent_beacon_block_root,
              targetGasLimit: data.target_gas_limit,
              withdrawalsCount: data.withdrawals_count,
              inclusionListCount: data.inclusion_list_count,
              receivedAt: data.received_at
            };
            setSlotStates(prev => {
              const state = prev[parentSlot] || { slot: parentSlot };
              const list = state.nextSlotAttributes ? [...state.nextSlotAttributes, info] : [info];
              // Defensive cap so a misbehaving CL can't grow this unbounded.
              if (list.length > 64) list.splice(0, list.length - 64);
              return { ...prev, [parentSlot]: { ...state, slot: parentSlot, nextSlotAttributes: list } };
            });
          }
          break;
        }

        case 'payload_build_started': {
          const data = event.data as { slot: number; started_at: number };
          addEvent('payload_build_started', `Payload build started for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, { payloadBuildStartedAt: data.started_at });
          break;
        }

        case 'payload_build_failed': {
          const data = event.data as { slot: number; error: string; failed_at: number };
          addEvent('payload_build_failed', `Payload build failed for slot ${data.slot}: ${data.error}`, event.timestamp);
          updateSlotState(data.slot, {
            payloadBuildFailed: true,
            payloadBuildFailedAt: data.failed_at,
            payloadBuildError: data.error
          });
          break;
        }

        case 'payload_ready': {
          // block_value is the EL's MEV value as a wei decimal string; convert to
          // gwei so it matches the gwei-based formatGwei display used elsewhere.
          const data = event.data as {
            slot: number; block_hash: string; block_value: string; ready_at: number;
            block_number?: number; fee_recipient?: string; gas_limit?: number; gas_used?: number;
            base_fee_per_gas?: string; extra_data?: string; blob_gas_used?: number; excess_blob_gas?: number;
            num_transactions?: number; num_withdrawals?: number; num_blobs?: number; num_exec_requests?: number;
          };
          addEvent('payload_ready', `Payload ready for slot ${data.slot} (hash: ${data.block_hash})`, event.timestamp);
          updateSlotState(data.slot, {
            payloadReady: true,
            payloadCreatedAt: data.ready_at,
            payloadBlockHash: data.block_hash,
            payloadBlockValue: data.block_value ? Number(data.block_value) / 1e9 : 0,
            payloadDetail: {
              blockNumber: data.block_number,
              feeRecipient: data.fee_recipient,
              gasLimit: data.gas_limit,
              gasUsed: data.gas_used,
              baseFeePerGas: data.base_fee_per_gas,
              extraData: data.extra_data,
              blobGasUsed: data.blob_gas_used,
              excessBlobGas: data.excess_blob_gas,
              numTransactions: data.num_transactions,
              numWithdrawals: data.num_withdrawals,
              numBlobs: data.num_blobs,
              numExecRequests: data.num_exec_requests
            }
          });
          break;
        }

        case 'bid_submitted': {
          const data = event.data as {
            slot: number; block_hash: string; value: number; bid_count: number; success: boolean;
            error?: string; warning?: string; execution_payment?: number; fee_recipient?: string;
            gas_limit?: number; builder_index?: number; parent_block_hash?: string;
            parent_block_root?: string; num_blob_commitments?: number;
          };
          const bidSuccess = data.success !== false;
          // A warning without an error on an unsuccessful event is a skip
          // (e.g. no proposer preferences cached for the slot), not a failed
          // submission attempt.
          const bidSkipped = !bidSuccess && !data.error && !!data.warning;
          let bidMsg = bidSuccess
            ? `Bid #${data.bid_count} submitted for slot ${data.slot} (value: ${data.value} gwei)`
            : bidSkipped
              ? `Bid skipped for slot ${data.slot}: ${data.warning}`
              : `Bid #${data.bid_count} FAILED for slot ${data.slot}: ${data.error || 'unknown error'}`;
          if (bidSuccess && data.warning) {
            bidMsg += ` [${data.warning}]`;
          }
          addEvent(
            bidSuccess && !data.warning ? 'bid_submitted' : bidSuccess || bidSkipped ? 'lifecycle_warning' : 'bid_failed',
            bidMsg,
            event.timestamp
          );

          // Skips are not bid attempts — keep them out of the slot's bid list.
          if (bidSkipped) break;

          setSlotStates(prev => {
            const state = prev[data.slot] || { slot: data.slot };
            const ourBids: OurBid[] = state.ourBids ? [...state.ourBids] : [];
            ourBids.push({
              time: event.timestamp,
              value: data.value,
              blockHash: data.block_hash,
              count: data.bid_count,
              success: bidSuccess,
              error: data.error,
              executionPayment: data.execution_payment,
              feeRecipient: data.fee_recipient,
              gasLimit: data.gas_limit,
              builderIndex: data.builder_index,
              parentBlockHash: data.parent_block_hash,
              parentBlockRoot: data.parent_block_root,
              numBlobCommitments: data.num_blob_commitments
            });
            return {
              ...prev,
              [data.slot]: { ...state, ourBids, bidSubmittedAt: event.timestamp }
            };
          });
          break;
        }

        case 'head_received': {
          const data = event.data as { slot: number; block_root: string; received_at: number };
          let headMsg = `Block received for slot ${data.slot}`;
          if (data.block_root) headMsg += ` (root: ${data.block_root})`;
          addEvent('head_received', headMsg, event.timestamp);
          updateSlotState(data.slot, { blockReceivedAt: data.received_at, blockRoot: data.block_root, bidsClosed: true });
          break;
        }

        case 'block_detail': {
          const data = event.data as import('../types').BlockDetail;
          updateSlotState(data.slot, { blockDetail: data });
          break;
        }

        case 'reveal_started': {
          const data = event.data as { slot: number; attempt: number; started_at: number };
          updateSlotState(data.slot, {
            revealInFlight: { attempt: data.attempt, startedAt: data.started_at }
          });
          break;
        }

        case 'reveal': {
          const data = event.data as {
            slot: number; success: boolean; skipped: boolean; skip_reason?: string; error?: string;
            attempt?: number; max_attempts?: number; started_at?: number; timestamp?: number;
            transport?: string; envelope?: import('../types').EnvelopeDetail;
          };
          const failed = !data.success && !data.skipped;
          const attempt = data.attempt || 0;
          const maxAttempts = data.max_attempts || 0;
          const revealMsg = data.skipped
            ? `Reveal skipped${data.skip_reason ? ` (${data.skip_reason})` : ''}`
            : data.success
              ? 'Reveal successful'
              : `Reveal failed${attempt ? ` (attempt ${attempt}/${maxAttempts})` : ''}${data.error ? `: ${data.error}` : ''}`;
          addEvent(failed ? 'reveal_failed' : 'reveal', `${revealMsg} for slot ${data.slot}`, event.timestamp);
          setSlotStates(prev => {
            const st = prev[data.slot] || { slot: data.slot };
            const revealAttempts: RevealAttempt[] = st.revealAttempts ? [...st.revealAttempts] : [];
            revealAttempts.push({
              time: data.timestamp ?? event.timestamp,
              startedAt: data.started_at || undefined,
              transport: data.transport,
              envelope: data.envelope,
              success: data.success,
              skipped: data.skipped,
              skipReason: data.skip_reason,
              error: data.error,
              attempt,
              maxAttempts
            });
            return {
              ...prev,
              [data.slot]: {
                ...st,
                slot: data.slot,
                revealAttempts,
                revealed: data.success,
                revealSkipped: data.skipped,
                revealFailed: failed,
                revealSentAt: event.timestamp,
                revealInFlight: undefined // the attempt completed
              }
            };
          });
          break;
        }

        case 'bid_event': {
          const data = event.data as {
            slot: number; builder_index: number; value: number; block_hash: string; is_ours: boolean;
            received_at: number; execution_payment?: number; fee_recipient?: string; gas_limit?: number;
            parent_block_hash?: string; parent_block_root?: string; num_blob_commitments?: number;
          };
          if (data.is_ours) {
            addEvent('bid_event', `Our bid seen on network for slot ${data.slot}`, event.timestamp);
          } else {
            setSlotStates(prev => {
              const state = prev[data.slot] || { slot: data.slot };
              const externalBids: ExternalBid[] = state.externalBids ? [...state.externalBids] : [];
              externalBids.push({
                time: data.received_at,
                value: data.value,
                builder: data.builder_index,
                blockHash: data.block_hash,
                executionPayment: data.execution_payment,
                feeRecipient: data.fee_recipient,
                gasLimit: data.gas_limit,
                parentBlockHash: data.parent_block_hash,
                parentBlockRoot: data.parent_block_root,
                numBlobCommitments: data.num_blob_commitments
              });
              return { ...prev, [data.slot]: { ...state, externalBids } };
            });
          }
          break;
        }

        case 'payload_available': {
          const data = event.data as {
            slot: number; block_hash: string; builder_index: number; received_at: number;
          } & import('../types').EnvelopeDetail;
          addEvent('payload_available', `Payload available for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            payloadAvailableDetail: data,
            payloadAvailableAt: data.received_at,
            payloadAvailableBlockHash: data.block_hash,
            payloadAvailableBuilder: data.builder_index
          });
          break;
        }

        case 'head_votes': {
          const data = event.data as {
            slot: number;
            block_root?: string;
            participation_pct: number;
            participation_eth: number;
            total_slot_eth: number;
            vote_count?: number;
            threshold_pct?: number;
            threshold_met?: boolean;
            timestamp: number;
          };
          const point: HeadVoteDataPoint = {
            time: data.timestamp,
            pct: data.participation_pct,
            eth: data.participation_eth,
            voteCount: data.vote_count,
            thresholdMet: data.threshold_met
          };
          setSlotStates(prev => {
            const state = prev[data.slot] || { slot: data.slot };
            const headVotes: HeadVoteDataPoint[] = state.headVotes
              ? [...state.headVotes, point]
              : [{ time: data.timestamp, pct: 0, eth: 0 }, point];
            const headVoteThresholdPct = data.threshold_pct ?? state.headVoteThresholdPct;
            const headVoteThresholdMetAt = state.headVoteThresholdMetAt
              ?? (data.threshold_met ? data.timestamp : undefined);
            return {
              ...prev,
              [data.slot]: { ...state, headVotes, headVoteThresholdPct, headVoteThresholdMetAt }
            };
          });
          break;
        }

        case 'builder_api_get_header_received': {
          const data = event.data as { slot: number; parent_hash: string; pubkey: string; received_at: number };
          addEvent('builder_api', `getHeader request received for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, { getHeaderReceivedAt: data.received_at });
          break;
        }

        case 'builder_api_get_header_delivered': {
          const data = event.data as { slot: number; block_hash: string; block_value: string; delivered_at: number };
          addEvent('builder_api', `Header delivered for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            getHeaderDeliveredAt: data.delivered_at,
            getHeaderBlockHash: data.block_hash,
            getHeaderBlockValue: data.block_value
          });
          break;
        }

        case 'builder_api_submit_blinded_received': {
          const data = event.data as { slot: number; block_hash: string; received_at: number };
          addEvent('builder_api', `submitBlindedBlock request received for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            submitBlindedReceivedAt: data.received_at,
            submitBlindedBlockHash: data.block_hash
          });
          break;
        }

        case 'builder_api_submit_blinded_delivered': {
          const data = event.data as { slot: number; block_hash: string; delivered_at: number };
          addEvent('builder_api', `Block published for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, { submitBlindedDeliveredAt: data.delivered_at });
          break;
        }

        case 'builder_api_get_bid_received': {
          const data = event.data as { slot: number; parent_hash: string; pubkey: string; received_at: number };
          addEvent('builder_api', `getExecutionPayloadBid request received for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, { getBidReceivedAt: data.received_at });
          break;
        }

        case 'builder_api_get_bid_delivered': {
          const data = event.data as { slot: number; block_hash: string; block_value: string; delivered_at: number };
          addEvent('builder_api', `Execution payload bid delivered for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            getBidDeliveredAt: data.delivered_at,
            getBidBlockHash: data.block_hash,
            getBidBlockValue: data.block_value
          });
          break;
        }

        case 'builder_api_submit_block_received': {
          const data = event.data as { slot: number; block_hash: string; received_at: number };
          addEvent('builder_api', `submitSignedBeaconBlock request received for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            submitBlockReceivedAt: data.received_at,
            submitBlockBlockHash: data.block_hash
          });
          break;
        }

        case 'builder_api_submit_block_delivered': {
          const data = event.data as { slot: number; block_hash: string; delivered_at: number };
          addEvent('builder_api', `Envelope published for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, { submitBlockDeliveredAt: data.delivered_at });
          break;
        }

        case 'bid_won': {
          // Event handled by BidsWonView component directly
          // No need to store in main state, just log it
          const data = event.data as { slot: number; block_hash: string; num_transactions: number; value_eth: string };
          addEvent('bid_won', `Bid won for slot ${data.slot} (${data.num_transactions} txs, ${parseFloat(data.value_eth).toFixed(6)} ETH)`, event.timestamp);
          break;
        }

        case 'bid_included': {
          const data = event.data as { slot: number; block_hash: string; bid_value: number };
          addEvent('lifecycle_success', `Block won for slot ${data.slot}!`, event.timestamp);
          updateSlotState(data.slot, { bidWon: true });
          break;
        }

        case 'lifecycle': {
          const data = event.data as { action: string; message: string; status: string };
          const eventType = data.status === 'error' ? 'lifecycle_error'
            : data.status === 'warning' ? 'lifecycle_warning'
            : data.status === 'success' ? 'lifecycle_success'
            : 'lifecycle';
          addEvent(eventType, data.message, event.timestamp);
          break;
        }
      }

      // Fan out every event to module-level subscribers (shared connection).
      dispatchStreamEvent(event.type, event.data);
    };

    const connect = () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }

      const eventSource = new EventSource('/api/events');
      eventSourceRef.current = eventSource;

      eventSource.onopen = () => {
        setConnected(true);
        bumpConnectionGeneration();
      };

      eventSource.onerror = () => {
        setConnected(false);
        // Close explicitly to prevent the browser's built-in auto-reconnect,
        // which would create a duplicate connection alongside our manual retry.
        eventSource.close();
        eventSourceRef.current = null;
        setTimeout(connect, 3000);
      };

      eventSource.onmessage = (e) => {
        try {
          const event = JSON.parse(e.data);
          handleEvent(event);
        } catch (err) {
          console.error('Failed to parse event:', err);
        }
      };
    };

    connect();

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
        eventSourceRef.current = null;
      }
    };
  }, []); // Empty dependency array - connection is stable

  const generation = useSyncExternalStore(subscribeConnectionGeneration, getConnectionGeneration);

  return {
    connected,
    config,
    chainInfo,
    stats,
    builderInfo,
    serviceStatus,
    voteCoverage,
    currentSlot,
    slotStates,
    slotConfigs,
    slotServiceStatuses,
    events,
    clearEvents,
    connectionGeneration: generation
  };
}
