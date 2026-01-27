import { useState, useEffect, useCallback, useRef } from 'react';
import type { Config, ChainInfo, Stats, SlotState, LogEvent, OurBid, ExternalBid, BuilderInfo } from '../types';

interface UseEventStreamResult {
  connected: boolean;
  config: Config | null;
  chainInfo: ChainInfo | null;
  stats: Stats | null;
  builderInfo: BuilderInfo | null;
  currentSlot: number;
  slotStates: Record<number, SlotState>;
  slotConfigs: Record<number, Config>;
  events: LogEvent[];
  clearEvents: () => void;
}

export function useEventStream(): UseEventStreamResult {
  const [connected, setConnected] = useState(false);
  const [config, setConfig] = useState<Config | null>(null);
  const [chainInfo, setChainInfo] = useState<ChainInfo | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [builderInfo, setBuilderInfo] = useState<BuilderInfo | null>(null);
  const [currentSlot, setCurrentSlot] = useState(0);
  const [slotStates, setSlotStates] = useState<Record<number, SlotState>>({});
  const [slotConfigs, setSlotConfigs] = useState<Record<number, Config>>({});
  const [events, setEvents] = useState<LogEvent[]>([]);

  // Use refs to access current values in event handlers without causing reconnection
  const configRef = useRef<Config | null>(null);
  const chainInfoRef = useRef<ChainInfo | null>(null);
  const currentSlotRef = useRef(0);
  const eventSourceRef = useRef<EventSource | null>(null);

  // Keep refs in sync with state
  useEffect(() => { configRef.current = config; }, [config]);
  useEffect(() => { chainInfoRef.current = chainInfo; }, [chainInfo]);
  useEffect(() => { currentSlotRef.current = currentSlot; }, [currentSlot]);

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
          setCurrentSlot(status.current_slot);
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

        case 'slot_start': {
          const data = event.data as { slot: number; slot_start_time: number };
          setCurrentSlot(data.slot);
          // Store config snapshot for this slot
          setSlotConfigs(prev => {
            const currentConfig = configRef.current;
            if (currentConfig) {
              return { ...prev, [data.slot]: JSON.parse(JSON.stringify(currentConfig)) };
            }
            return prev;
          });
          updateSlotState(data.slot, { slotStartTime: data.slot_start_time });

          // Calculate actual slot from time
          const info = chainInfoRef.current;
          if (info) {
            const now = Date.now();
            const elapsed = now - info.genesis_time;
            const actualSlot = Math.floor(elapsed / info.seconds_per_slot);
            addEvent('slot_start', `Slot ${actualSlot} started`, event.timestamp);
          } else {
            addEvent('slot_start', `Slot ${data.slot - 1} started`, event.timestamp);
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
    currentSlot,
    slotStates,
    slotConfigs,
    events,
    clearEvents
  };
}
