// Shared REST background-refresh intervals. SSE is the live update channel —
// REST polling only guards against slow drift (e.g. registration counts), so
// it runs slow and aligned across hooks instead of each picking its own
// cadence.
//
// SLOW: near-static data (status cards, builder preferences). LIVE: data that
// genuinely changes slot-by-slot (proposer preferences page); only polled
// while its page is open.
export const REFRESH_INTERVAL_SLOW_MS = 60_000;
export const REFRESH_INTERVAL_LIVE_MS = 12_000;
