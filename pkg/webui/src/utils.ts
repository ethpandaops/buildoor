import type { ChainInfo, ScheduleConfig } from './types';

// formatEventTime renders a timestamp (ms epoch) as HH:mm:ss.mmm in 24h format
// using the local timezone.
export function formatEventTime(timestamp: number): string {
  const d = new Date(timestamp);
  const pad = (n: number, len = 2) => String(n).padStart(len, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

export function formatGwei(gwei: number): string {
  const sign = gwei < 0 ? '-' : '';
  const abs = Math.abs(gwei);
  if (abs >= 1000000000) {
    return sign + (abs / 1000000000).toFixed(4) + ' ETH';
  } else if (abs >= 1000000) {
    return sign + (abs / 1000000).toFixed(2) + 'M gwei';
  }
  return gwei + ' gwei';
}

export function isSlotScheduled(slot: number, schedule: ScheduleConfig | undefined): boolean {
  if (!schedule) return true;
  const mode = schedule.mode || 'all';
  const startSlot = schedule.start_slot || 0;

  if (slot < startSlot) return false;

  switch (mode) {
    case 'all':
      return true;
    case 'every_nth': {
      const nth = schedule.every_nth || 1;
      return ((slot - startSlot) % nth) === 0;
    }
    case 'next_n': {
      const nextN = schedule.next_n || 0;
      return (slot - startSlot) < nextN;
    }
    default:
      return true;
  }
}

// getEventTypeClass maps a log event type to a colour class. Colours mirror the
// matching slot-graph element so the log and graph read the same; types without
// a graph element get no colour. Errors are always rendered in a wider red block
// (event-error), regardless of whether they have a graph element.
export function getEventTypeClass(type: string): string {
  const classes: Record<string, string> = {
    // Errors → wider red block.
    'build_failed': 'event-error',
    'testing_build_failed': 'event-error',
    'bid_failed': 'event-error',
    'reveal_failed': 'event-error',
    'lifecycle_error': 'event-error',

    // Warnings → wider yellow block.
    'lifecycle_warning': 'event-warning',

    // Colours matching graph elements.
    'head_received': 'event-color-block-received',        // block-received dot
    'payload_attributes': 'event-color-payload-attributes', // payload-attributes dot
    'build_target': 'event-color-build',                   // build lines
    'engine_build': 'event-color-build',                   // engine payload dot
    'testing_build': 'event-color-build',                  // testing run dot
    'payload_selected': 'event-color-build',               // selection ring
    'bid_submitted': 'event-color-bid',                    // bid-submitted dot
    'bid_event': 'event-color-external-bid',               // external-bid dot
    'payload_available': 'event-color-payload-available',  // payload-available dot
    'reveal': 'event-color-reveal',                        // reveal-sent dot
    'bid_won': 'event-color-bid-won',                      // bid-won crown
    'builder_api': 'event-color-builder-api'               // builder-api dot

    // No graph element (no colour): slot_start, lifecycle, lifecycle_success
    // — fall through to ''.
  };
  return classes[type] || '';
}

export interface EventCategory {
  key: string;
  label: string;
  color: string;
}

// Maps a log event type to a filter category. Categories group types by their
// graph colour so the log filter can enable/disable them as a unit.
const EVENT_TYPE_CATEGORY: Record<string, string> = {
  head_received: 'block',
  payload_attributes: 'attributes',
  build_target: 'build',
  engine_build: 'build',
  testing_build: 'build',
  payload_selected: 'build',
  testing_build_failed: 'error',
  build_failed: 'error',
  bid_submitted: 'bid',
  bid_event: 'external_bid',
  payload_available: 'available',
  reveal: 'reveal',
  bid_won: 'bid_won',
  builder_api: 'builder_api',
  bid_failed: 'error',
  reveal_failed: 'error',
  lifecycle_error: 'error',
  lifecycle_warning: 'warning'
};

export function getEventCategory(type: string): string {
  return EVENT_TYPE_CATEGORY[type] || 'other';
}

// EVENT_CATEGORIES drives the log filter UI (swatch colour + label per category).
export const EVENT_CATEGORIES: EventCategory[] = [
  { key: 'block', label: 'Block Received', color: '#fd7e14' },
  { key: 'attributes', label: 'Payload Attributes', color: '#e83e8c' },
  { key: 'build', label: 'Build / Payload', color: '#17a2b8' },
  { key: 'bid', label: 'Bid Submitted', color: '#28a745' },
  { key: 'external_bid', label: 'External Bid', color: '#6c757d' },
  { key: 'available', label: 'Payload Available', color: '#20c997' },
  { key: 'reveal', label: 'Reveal', color: '#6f42c1' },
  { key: 'bid_won', label: 'Bid Won', color: '#ffc107' },
  { key: 'builder_api', label: 'Builder API', color: '#0d6efd' },
  { key: 'error', label: 'Errors', color: '#dc3545' },
  { key: 'warning', label: 'Warnings', color: '#ffc107' },
  { key: 'other', label: 'Other', color: '#6c757d' }
];

type SlotClock = Pick<ChainInfo, 'genesis_time' | 'seconds_per_slot' | 'heze_fork_slot' | 'slot_duration_heze'>;

function hezeForkSlot(chainInfo: SlotClock | null): number | null {
  if (chainInfo?.heze_fork_slot == null || !chainInfo.slot_duration_heze) return null;
  return chainInfo.heze_fork_slot;
}

// slotDurationAt returns the duration (ms) of the given slot.
export function slotDurationAt(chainInfo: SlotClock | null, slot: number): number {
  const forkSlot = hezeForkSlot(chainInfo);
  if (forkSlot !== null && slot >= forkSlot) return chainInfo!.slot_duration_heze!;
  return chainInfo?.seconds_per_slot || 12000;
}

// slotStartTime returns the start (unix ms) of the given slot.
export function slotStartTime(chainInfo: SlotClock | null, slot: number): number {
  const genesisTime = chainInfo?.genesis_time || 0;
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const forkSlot = hezeForkSlot(chainInfo);
  if (forkSlot !== null && slot > forkSlot) {
    return genesisTime + forkSlot * slotDuration + (slot - forkSlot) * chainInfo!.slot_duration_heze!;
  }
  return genesisTime + slot * slotDuration;
}

// slotAtTime returns the slot in progress at the given unix ms time.
export function slotAtTime(chainInfo: SlotClock | null, timeMs: number): number {
  const elapsed = timeMs - (chainInfo?.genesis_time || 0);
  if (elapsed < 0) return 0;
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const forkSlot = hezeForkSlot(chainInfo);
  if (forkSlot !== null && elapsed >= forkSlot * slotDuration) {
    return forkSlot + Math.floor((elapsed - forkSlot * slotDuration) / chainInfo!.slot_duration_heze!);
  }
  return Math.floor(elapsed / slotDuration);
}

export function calculateSlotTiming(
  chainInfo: SlotClock | null,
  slot: number
): { slotStartTime: number; rangeStart: number; rangeEnd: number; totalRange: number } {
  const slotDuration = slotDurationAt(chainInfo, slot);
  const startTime = slotStartTime(chainInfo, slot);
  // A third of the slot before its start keeps the payload builds visible:
  // they start ahead of the slot (default -28% of the slot time).
  const rangeStart = -slotDuration / 3;
  const rangeEnd = slotDuration;
  const totalRange = rangeEnd - rangeStart;

  return { slotStartTime: startTime, rangeStart, rangeEnd, totalRange };
}

export function calculatePosition(
  timeMs: number,
  rangeStart: number,
  totalRange: number
): number {
  return ((timeMs - rangeStart) / totalRange) * 100;
}

// decodeExtraData renders a payload's 0x-hex extra data as printable ASCII
// when possible (builders commonly stamp readable tags), else keeps the hex.
export const decodeExtraData = (hex: string): string => {
  const raw = hex.startsWith('0x') ? hex.slice(2) : hex;
  if (raw.length === 0) return '(empty)';

  let text = '';
  for (let i = 0; i + 1 < raw.length || i < raw.length; i += 2) {
    const code = parseInt(raw.slice(i, i + 2), 16);
    if (Number.isNaN(code) || code < 0x20 || code > 0x7e) return hex;
    text += String.fromCharCode(code);
  }
  return text;
};
