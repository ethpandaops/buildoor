import type { ScheduleConfig } from './types';

export function formatGwei(gwei: number): string {
  if (gwei >= 1000000000) {
    return (gwei / 1000000000).toFixed(4) + ' ETH';
  } else if (gwei >= 1000000) {
    return (gwei / 1000000).toFixed(2) + 'M gwei';
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

export function getEventTypeClass(type: string): string {
  const classes: Record<string, string> = {
    'slot_start': 'event-slot-start',
    'payload_ready': 'event-payload',
    'bid_submitted': 'event-bid',
    'bid_failed': 'event-bid-failed',
    'head_received': 'event-head',
    'reveal': 'event-reveal',
    'bid_event': 'event-bid-seen',
    'payload_available': 'event-envelope'
  };
  return classes[type] || '';
}

export function calculateSlotTiming(
  chainInfo: { genesis_time: number; seconds_per_slot: number } | null,
  slot: number
): { slotStartTime: number; rangeStart: number; rangeEnd: number; totalRange: number } {
  const slotDuration = chainInfo?.seconds_per_slot || 12000;
  const genesisTime = chainInfo?.genesis_time || 0;
  const slotStartTime = genesisTime + (slot * slotDuration);
  const rangeStart = -slotDuration * 0.25;
  const rangeEnd = slotDuration;
  const totalRange = rangeEnd - rangeStart;

  return { slotStartTime, rangeStart, rangeEnd, totalRange };
}

export function calculatePosition(
  timeMs: number,
  rangeStart: number,
  totalRange: number
): number {
  return ((timeMs - rangeStart) / totalRange) * 100;
}
