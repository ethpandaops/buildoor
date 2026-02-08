import { useState, useEffect, useCallback, useRef } from 'react';
import type { Config, ChainInfo, Stats, SlotState, LogEvent, OurBid, ExternalBid, BuilderInfo, HeadVoteDataPoint, ServiceStatus } from '../types';

interface UseEventStreamResult {
  connected: boolean;
  config: Config | null;
  chainInfo: ChainInfo | null;
  stats: Stats | null;
  builderInfo: BuilderInfo | null;
  serviceStatus: ServiceStatus | null;
  currentSlot: number;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  slotServiceStatuses: Record<number, ServiceStatus>;
  events: LogEvent[];
  clearEvents: () => void;
}

export function useEventStream(): UseEventStreamResult {
  const [connected, setConnected] = useState(false);
  const [config, setConfig] = useState<Config | null>(null);
  const [chainInfo, setChainInfo] = useState<ChainInfo | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [builderInfo, setBuilderInfo] = useState<BuilderInfo | null>(null);
  const [serviceStatus, setServiceStatus] = useState<ServiceStatus | null>(null);
  const [currentSlot, setCurrentSlot] = useState(0);
  const [slotStates, setSlotStates] = useState<Record<number, SlotState>>({});
  const [slotConfigs, setSlotConfigs] = useState<Record<number, Config>>({});
  const [slotServiceStatuses, setSlotServiceStatuses] = useState<Record<number, ServiceStatus>>({});
  const [events, setEvents] = useState<LogEvent[]>([]);

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
        const newEvents = [{ type, message, timestamp }, ...prev];
        return newEvents.slice(0, 100);
      });
    };

    const updateSlotState = (slot: number, updates: Partial<SlotState>) => {
      setSlotStates(prev => ({
        ...prev,
        [slot]: { ...prev[slot], slot, ...updates }
      }));
    };

    const handleEvent = (event: { type: string; timestamp: number; data: unknown }) => {
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

        case 'payload_ready': {
          const data = event.data as { slot: number; block_hash: string; block_value: number; ready_at: number };
          addEvent('payload_ready', `Payload ready for slot ${data.slot} (hash: ${data.block_hash.substring(0, 10)}...)`, event.timestamp);
          updateSlotState(data.slot, {
            payloadReady: true,
            payloadCreatedAt: data.ready_at,
            payloadBlockHash: data.block_hash,
            payloadBlockValue: data.block_value
          });
          break;
        }

        case 'bid_submitted': {
          const data = event.data as { slot: number; block_hash: string; value: number; bid_count: number; success: boolean; error?: string };
          const bidSuccess = data.success !== false;
          const bidMsg = bidSuccess
            ? `Bid #${data.bid_count} submitted for slot ${data.slot} (value: ${data.value} gwei)`
            : `Bid #${data.bid_count} FAILED for slot ${data.slot}: ${data.error || 'unknown error'}`;
          addEvent(bidSuccess ? 'bid_submitted' : 'bid_failed', bidMsg, event.timestamp);

          setSlotStates(prev => {
            const state = prev[data.slot] || { slot: data.slot };
            const ourBids: OurBid[] = state.ourBids ? [...state.ourBids] : [];
            ourBids.push({
              time: event.timestamp,
              value: data.value,
              blockHash: data.block_hash,
              count: data.bid_count,
              success: bidSuccess,
              error: data.error
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
          if (data.block_root) headMsg += ` (root: ${data.block_root.substring(0, 10)}...)`;
          addEvent('head_received', headMsg, event.timestamp);
          updateSlotState(data.slot, { blockReceivedAt: data.received_at, blockRoot: data.block_root, bidsClosed: true });
          break;
        }

        case 'reveal': {
          const data = event.data as { slot: number; success: boolean; skipped: boolean };
          const revealMsg = data.skipped ? 'Reveal skipped' : (data.success ? 'Reveal successful' : 'Reveal failed');
          addEvent('reveal', `${revealMsg} for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            revealed: data.success,
            revealSkipped: data.skipped,
            revealFailed: !data.success && !data.skipped,
            revealSentAt: event.timestamp
          });
          break;
        }

        case 'bid_event': {
          const data = event.data as { slot: number; builder_index: number; value: number; block_hash: string; is_ours: boolean; received_at: number };
          if (data.is_ours) {
            addEvent('bid_event', `Our bid seen on network for slot ${data.slot}`, event.timestamp);
            updateSlotState(data.slot, { bidWon: true });
          } else {
            setSlotStates(prev => {
              const state = prev[data.slot] || { slot: data.slot };
              const externalBids: ExternalBid[] = state.externalBids ? [...state.externalBids] : [];
              externalBids.push({
                time: data.received_at,
                value: data.value,
                builder: data.builder_index,
                blockHash: data.block_hash
              });
              return { ...prev, [data.slot]: { ...state, externalBids } };
            });
          }
          break;
        }

        case 'payload_envelope': {
          const data = event.data as { slot: number; block_hash: string; builder_index: number; received_at: number };
          addEvent('payload_envelope', `Payload envelope received for slot ${data.slot}`, event.timestamp);
          updateSlotState(data.slot, {
            payloadEnvelopeAt: data.received_at,
            payloadEnvelopeBlockHash: data.block_hash,
            payloadEnvelopeBuilder: data.builder_index
          });
          break;
        }

        case 'head_votes': {
          const data = event.data as {
            slot: number;
            participation_pct: number;
            participation_eth: number;
            total_slot_eth: number;
            timestamp: number;
          };
          const point: HeadVoteDataPoint = {
            time: data.timestamp,
            pct: data.participation_pct,
            eth: data.participation_eth
          };
          setSlotStates(prev => {
            const state = prev[data.slot] || { slot: data.slot };
            const headVotes: HeadVoteDataPoint[] = state.headVotes
              ? [...state.headVotes, point]
              : [{ time: data.timestamp, pct: 0, eth: 0 }, point];
            return { ...prev, [data.slot]: { ...state, headVotes } };
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

        case 'bid_won': {
          // Event handled by BidsWonView component directly
          // No need to store in main state, just log it
          const data = event.data as { slot: number; block_hash: string; num_transactions: number; value_eth: string };
          addEvent('bid_won', `Bid won for slot ${data.slot} (${data.num_transactions} txs, ${parseFloat(data.value_eth).toFixed(6)} ETH)`, event.timestamp);
          break;
        }
      }
    };

    const connect = () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }

      const eventSource = new EventSource('/api/events');
      eventSourceRef.current = eventSource;

      eventSource.onopen = () => {
        setConnected(true);
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

  return {
    connected,
    config,
    chainInfo,
    stats,
    builderInfo,
    serviceStatus,
    currentSlot,
    slotStates,
    slotConfigs,
    slotServiceStatuses,
    events,
    clearEvents
  };
}
