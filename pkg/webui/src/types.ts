// Auth types
export interface AuthTokenResponse {
  token: string;
  user: string;
  expr: string;  // Unix timestamp as string
  now: string;   // Server's current time as string
}

export interface AuthState {
  isLoggedIn: boolean;
  user: string | null;
  token: string | null;
  expiresAt: number | null;  // Local timestamp
}

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
  payload_build_delay?: number;
}

export interface ServiceStatus {
  epbs_available: boolean;
  epbs_enabled: boolean;
  legacy_available: boolean;
  legacy_enabled: boolean;
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
  builder_api_headers_requested: number;
  builder_api_blocks_published: number;
  builder_api_registered_validators: number;
}

export interface BuilderInfo {
  builder_pubkey: string;
  builder_index: number;
  is_registered: boolean;
  cl_balance_gwei: number;
  pending_payments_gwei: number;
  effective_balance_gwei: number;
  lifecycle_enabled: boolean;
  wallet_address?: string;
  wallet_balance_wei?: string;
  pending_deposit_gwei?: number;
  deposit_epoch: number;
  withdrawable_epoch: number;
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

export interface HeadVoteDataPoint {
  time: number;
  pct: number;
  eth: number;
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
  headVotes?: HeadVoteDataPoint[];
  getHeaderReceivedAt?: number;
  getHeaderDeliveredAt?: number;
  getHeaderBlockHash?: string;
  getHeaderBlockValue?: string;
  submitBlindedReceivedAt?: number;
  submitBlindedDeliveredAt?: number;
  submitBlindedBlockHash?: string;
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

// Validator registration types
export interface ValidatorRegistration {
  pubkey: string;
  fee_recipient: string;
  gas_limit: number;
  timestamp: number;
}

export interface ValidatorsResponse {
  validators: ValidatorRegistration[];
}

// Builder API status types
export interface BuilderAPIStatus {
  enabled: boolean;
  port: number;
  validator_count: number;
  use_proposer_fee_recipient: boolean;
  block_value_subsidy_gwei: number;
}

// Bids won types
export interface BidWonEntry {
  slot: number;
  block_hash: string;
  num_transactions: number;
  num_blobs: number;
  value_eth: string;
  value_wei: number;
  timestamp: number;
}

export interface BidsWonResponse {
  bids_won: BidWonEntry[];
  total: number;
  offset: number;
  limit: number;
}
