// Auth types
export interface AuthState {
  /**
   * Whether authentication is enabled at all. False when the backend is
   * running without --auth-provider-url (open API). In that case
   * isLoggedIn is true (the user is implicitly authorized), no token is
   * sent on requests, and login/logout controls are hidden.
   */
  authEnabled: boolean;
  isLoggedIn: boolean;
  user: string | null;
  token: string | null;
  expiresAt: number | null; // Local timestamp (ms)
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
  deposit_amount: number;
  topup_threshold: number;
  topup_amount: number;
  payload_build_time?: number;
  extra_data?: string;
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
  bid_subsidy: number;
  payload_build_delay?: number;
}

export interface ServiceStatus {
  epbs_available: boolean;
  epbs_enabled: boolean;
  epbs_registration_state: string; // "unknown" | "unregistered" | "waiting_gloas" | "pending" | "pending_finalization" | "registered" | "exiting" | "exited"
  builder_api_available: boolean;
  builder_api_enabled: boolean;
  lifecycle_available: boolean;
  lifecycle_enabled: boolean;
}

export interface ChainInfo {
  genesis_time: number;
  seconds_per_slot: number;
  slots_per_epoch: number;
}

export interface Stats {
  slots_built: number;
  blocks_included: number;
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
  block_value: string;
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

export interface PayloadAvailableEvent {
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
  error?: string;
  attempt?: number;
  max_attempts?: number;
  timestamp: number;
}

// A single payload reveal attempt (the reveal may be retried on failure).
export interface RevealAttempt {
  time: number;
  success: boolean;
  skipped: boolean;
  skipReason?: string;
  error?: string;
  attempt: number;
  maxAttempts: number;
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
  payloadBuildStartedAt?: number;
  payloadBuildFailed?: boolean;
  payloadBuildFailedAt?: number;
  payloadBuildError?: string;
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
  payloadAvailableAt?: number;
  payloadAvailableBlockHash?: string;
  payloadAvailableBuilder?: number;
  revealed?: boolean;
  revealSkipped?: boolean;
  revealFailed?: boolean;
  revealSentAt?: number;
  revealAttempts?: RevealAttempt[];
  headVotes?: HeadVoteDataPoint[];
  getHeaderReceivedAt?: number;
  getHeaderDeliveredAt?: number;
  getHeaderBlockHash?: string;
  getHeaderBlockValue?: string;
  submitBlindedReceivedAt?: number;
  submitBlindedDeliveredAt?: number;
  submitBlindedBlockHash?: string;
  // Gloas (post-Gloas) builder API interactions.
  getBidReceivedAt?: number;
  getBidDeliveredAt?: number;
  getBidBlockHash?: string;
  getBidBlockValue?: string;
  submitBlockReceivedAt?: number;
  submitBlockDeliveredAt?: number;
  submitBlockBlockHash?: string;
  // payload_attributes events targeting the NEXT slot (this.slot + 1). They
  // arrive before the slot they target (the CL re-emits one per head update),
  // so they are rendered on this (parent) slot's graph — one dot each.
  nextSlotAttributes?: PayloadAttributesInfo[];
}

export interface PayloadAttributesInfo {
  proposalSlot: number;
  proposerIndex: number;
  parentBlockHash: string;
  parentBlockRoot: string;
  parentBlockNumber: number;
  timestamp: number;
  feeRecipient: string;
  targetGasLimit: number;
  withdrawalsCount: number;
  receivedAt: number;
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
  id: number;
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
  block_value_subsidy_gwei: number;
}

// Bids won types
export interface BidWonEntry {
  slot: number;
  block_hash: string;
  num_transactions: number;
  num_blobs: number;
  value_eth: string;
  value_wei: string;
  timestamp: number;
}

export interface BidsWonResponse {
  bids_won: BidWonEntry[];
  total: number;
  offset: number;
  limit: number;
}

// Audit log types (populated only when --state-db is configured)
export interface AuditLogEntry {
  id: number;
  timestamp: number;
  actor: string;
  remote_addr: string;
  action: string;
  target: string;
  detail: string;
  result: string;
}

export interface AuditLogResponse {
  entries: AuditLogEntry[];
  total: number;
  offset: number;
  limit: number;
}

// Proposer preferences types
export interface ProposerPreference {
  slot: number;
  validator_index: number;
  client_name?: string;
  fee_recipient: string;
  target_gas_limit: number;
}

export interface ProposerPreferencesResponse {
  preferences: ProposerPreference[];
}

// Builder preferences types
export interface BuilderPreference {
  validator_pubkey: string;
  max_execution_payment: number; // Gwei
}

export interface BuilderPreferencesResponse {
  preferences: BuilderPreference[];
}

// ---------------------------------------------------------------------------
// Per-slot action plan types (wire shapes of pkg/action_plan; snake_case JSON)
// ---------------------------------------------------------------------------

// Per-category plan mode: "custom" force-activates the category for the slot
// with optional overrides, "disabled" suppresses it. An absent category
// inherits the global baseline.
export type ActionMode = 'custom' | 'disabled';

// Timing fields are SIGNED milliseconds relative to slot start.
export interface BidPlan {
  mode: ActionMode;
  bid_start_time?: number;
  bid_end_time?: number;
  bid_min_amount?: number; // gwei
  bid_increase?: number; // gwei
  bid_interval?: number; // ms, >= 0, 0 = single bid
  bid_subsidy?: number; // gwei
  bid_value_gwei?: number; // absolute bid base value
  ignore_missing_prefs?: boolean;
}

export interface BuilderAPIPlan {
  mode: ActionMode;
  value_subsidy_gwei?: number;
  total_value_override_gwei?: number;
  response_delay_ms?: number;
}

export interface RevealPlan {
  mode: ActionMode;
  reveal_time_ms?: number;
}

export interface SlotPlan {
  slot: number;
  bid?: BidPlan;
  builder_api?: BuilderAPIPlan;
  reveal?: RevealPlan;
  updated_at: string;
  updated_by: string;
}

// One mutation unit of the bulk plan update API. Category members are
// three-state: absent = unchanged, null = clear (back to inherit), object =
// replace. `set` applies fine-grained path updates (e.g. "bid.bid_min_amount")
// after the category members; a null value clears a single override.
export interface PlanUpdate {
  slots?: number[];
  from_slot?: number;
  to_slot?: number;
  delete?: boolean;
  bid?: BidPlan | null;
  builder_api?: BuilderAPIPlan | null;
  reveal?: RevealPlan | null;
  set?: Record<string, number | string | boolean | null>;
}

export interface ActionPlanResponse {
  plans: SlotPlan[];
  min_slot: number;
  max_slot: number;
}

// Authoritative normalized result of a committed plan mutation; a null plan
// means the slot's plan was deleted.
export interface UpdateActionPlanResponse {
  status: string;
  slots: number[];
  plans: (SlotPlan | null)[];
}

// Resolved (frozen) settings — pkg/action_plan/frozen.go wire shapes.
export interface ResolvedBuildSettings {
  build: boolean;
  forced?: boolean;
  skip_reason?: string; // "schedule" | "plan_disabled" | "no_consumer"
  plan_involved?: boolean;
  build_start_time_ms: number;
}

export interface ResolvedBidSettings {
  start_ms: number;
  end_ms: number;
  interval_ms: number;
  min_gwei: number;
  increase_gwei: number;
  subsidy_gwei: number;
  value_gwei?: number;
  ignore_missing_prefs?: boolean;
  forced?: boolean;
}

export interface ResolvedBuilderAPISettings {
  subsidy_gwei: number;
  total_value_gwei?: number;
  delay_ms?: number;
  forced?: boolean;
}

export interface ResolvedRevealSettings {
  suppressed?: boolean;
  reveal_time_ms: number;
  bypass_deadline?: boolean;
}

// FrozenPlan is the immutable per-slot execution snapshot; a nil bid /
// builder_api category means the category is suppressed for the slot.
export interface FrozenPlan {
  slot: number;
  plan?: SlotPlan;
  fork: string;
  frozen_at: string;
  build: ResolvedBuildSettings;
  bid?: ResolvedBidSettings;
  builder_api?: ResolvedBuilderAPISettings;
  reveal?: ResolvedRevealSettings;
}

// Per-slot result types (wire shapes of pkg/slot_results).
export type BuildStatus =
  | 'waiting_attributes'
  | 'no_attributes'
  | 'started'
  | 'ready'
  | 'failed'
  | 'skipped';

export type BidAttemptStatus =
  | 'suppressed'
  | 'constructed'
  | 'submitted'
  | 'served'
  | 'failed'
  | 'cancelled';

export type BlockSubmissionStatus = 'received' | 'accepted' | 'failed';

export type RevealAttemptStatus = 'suppressed' | 'published' | 'failed' | 'skipped';

export interface BuildOutcome {
  status: BuildStatus;
  skip_reason?: string;
  block_hash?: string;
  block_value_wei?: string;
  num_transactions?: number;
  num_blobs?: number;
  fee_recipient?: string;
  error?: string;
  at: string;
}

export interface SlotBidAttempt {
  status: BidAttemptStatus;
  transport: string;
  total_value_gwei: number;
  execution_payment_gwei?: number;
  competitor_high_gwei?: number;
  artifact_index?: number;
  error?: string;
  at: string;
}

export interface SlotBlockSubmission {
  dialect: string; // "legacy" | "epbs"
  status: BlockSubmissionStatus;
  error?: string;
  at: string;
}

export interface SlotRevealAttempt {
  status: RevealAttemptStatus;
  transport: string;
  skip_reason?: string; // "plan_disabled" | "late"
  error?: string;
  attempt: number;
  at: string;
}

export interface SlotInclusionResult {
  source: string; // "epbs" | "builder_api"
  block_hash: string;
  num_transactions: number;
  num_blobs: number;
  value_wei: string;
  value_eth: string;
  timestamp: string;
}

export interface SlotResult {
  slot: number;
  epoch: number;
  fork: string;
  applied_plan?: FrozenPlan;
  build?: BuildOutcome;
  bids?: SlotBidAttempt[];
  block_submissions?: SlotBlockSubmission[];
  reveal_attempts?: SlotRevealAttempt[];
  inclusion?: SlotInclusionResult;
  dropped_attempts?: Record<string, number>;
  updated_at: string;
}

export interface SlotResultsResponse {
  results: SlotResult[];
  min_slot: number;
  max_slot: number;
}

// Slot bid artifact listing (GET /api/buildoor/slot-results/{slot}/bids).
export interface BidArtifactMetaEntry {
  index: number;
  fork: string;
  transport?: string;
  total_value_gwei?: number;
  execution_payment_gwei?: number;
  at?: number; // unix milliseconds
}

export interface SlotBidArtifactsResponse {
  slot: number;
  bids: BidArtifactMetaEntry[];
}
