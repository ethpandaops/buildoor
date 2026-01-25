// Event types from the SSE stream
export interface StreamEvent {
  type: string;
  timestamp: number;
  data: unknown;
}

export interface Config {
  schedule: ScheduleConfig;
  epbs: EPBSConfig;
}

export interface ScheduleConfig {
  mode: string;
  every_nth: number;
  next_n: number;
  start_slot: number;
}

export interface EPBSConfig {
  build_start_time: number;
  bid_start_time: number;
  bid_end_time: number;
  reveal_time: number;
  bid_min_amount: number;
  bid_increase: number;
  bid_interval: number;
}

export interface ChainInfo {
  genesis_time: number;
  seconds_per_slot: number;
}

export interface Stats {
  slots_built: number;
  bids_submitted: number;
  bids_won: number;
  total_paid: number;
  reveals_success: number;
  reveals_failed: number;
  reveals_skipped: number;
}

export interface SlotStartEvent {
  slot: number;
  slot_start_time: number;
}

export interface PayloadReadyEvent {
  slot: number;
  block_hash: string;
  parent_block_hash: string;
  block_value: number;
  ready_at: number;
}

export interface BidSubmittedEvent {
  slot: number;
  block_hash: string;
  value: number;
  bid_count: number;
  timestamp: number;
  success: boolean;
  error?: string;
}

export interface HeadReceivedEvent {
  slot: number;
  block_root: string;
  received_at: number;
}

export interface BidEvent {
  slot: number;
  builder_index: number;
  value: number;
  block_hash: string;
  is_ours: boolean;
  received_at: number;
}

export interface PayloadEnvelopeEvent {
  slot: number;
  block_root: string;
  block_hash: string;
  builder_index: number;
  received_at: number;
}

export interface RevealEvent {
  slot: number;
  success: boolean;
  skipped: boolean;
  timestamp: number;
}

// UI State types
export interface SlotState {
  slot: number;
  scheduled?: boolean;
  slotStartTime?: number;
  payloadReady?: boolean;
  payloadCreatedAt?: number;
  payloadBlockHash?: string;
  payloadBlockValue?: number;
  blockReceivedAt?: number;
  blockRoot?: string;
  bidsClosed?: boolean;
  bidWon?: boolean;
  ourBids?: OurBid[];
  externalBids?: ExternalBid[];
  payloadEnvelopeAt?: number;
  payloadEnvelopeBlockHash?: string;
  payloadEnvelopeBuilder?: number;
  revealed?: boolean;
  revealSkipped?: boolean;
  revealFailed?: boolean;
  revealSentAt?: number;
}

export interface OurBid {
  time: number;
  value: number;
  blockHash?: string;
  count: number;
  success: boolean;
  error?: string;
}

export interface ExternalBid {
  time: number;
  value: number;
  builder: number;
  blockHash?: string;
}

export interface LogEvent {
  type: string;
  message: string;
  timestamp: number;
}
