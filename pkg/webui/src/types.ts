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
  reveal?: RevealConfig;
  build?: BuildConfig;
  local_build?: LocalBuildConfig;
  txpool?: TxPoolConfig;
  deposit_amount: number;
  topup_threshold: number;
  topup_amount: number;
  payload_build_time?: number;
  extra_data?: string;
}

// Build-candidate policy: which parent candidates a slot builds payloads for
// (auto | always | never per candidate) and how the engine builds run.
export interface BuildConfig {
  candidate_parent_full?: string;
  candidate_parent_empty?: string;
  candidate_grandparent_full?: string;
  candidate_grandparent_empty?: string;
  parallel?: boolean;
  speculative_build_time_ms?: number;
  auto_weak_head_pct?: number;
  enforce_bid_gas_limit?: boolean;
}

// Local block building extension (testing_buildBlockV1): an additional payload
// per slot from a chosen transaction source, plus the selector deciding which
// payload (EL or local) feeds bids and reveals.
export interface LocalBuildConfig {
  enabled: boolean;
  payload_source: string; // el | local | local_or_el
  tx_source: string; // txpool | empty | el_mempool
  build_el_payload: boolean;
  allow_blobs_without_bundle: boolean;
  blob_encoding: string; // auto | network | canonical (startup-only)
}

// Owned transaction pool settings.
export interface TxPoolConfig {
  enabled: boolean;
  ordering: string; // fifo | tip | random
  max_txs_per_block: number;
  gas_fill_pct: number;
  max_pool_txs: number;
  max_txs_per_sender: number;
  tx_ttl_slots: number;
  forward_to_el: boolean;
}

// Probed availability of the EL's testing namespace + per-EL blob handling.
export interface LocalBuildAvailability {
  configured: boolean;
  available: boolean;
  reason?: string;
  checked_at: string;
  el_code?: string;
  enable_hint?: string;
  blob_bundle: boolean;
  blob_encoding: string;
}

export interface TxPoolStatus {
  available: boolean;
  enabled: boolean;
  reason?: string;
  ingress_path?: string;
}

export interface LocalBuildStatus {
  availability: LocalBuildAvailability;
  enabled: boolean;
  payload_source: string;
  tx_source: string;
  build_el_payload: boolean;
  txpool: TxPoolStatus;
  plan_checks?: {
    checked: number;
    match: number;
    mismatch: number;
    block_not_found: number;
    missed: number;
    orphaned: number;
  };
}

// Pool aggregates + lifetime counters (SSE txpool_stats and GET /txpool).
export interface TxPoolStats {
  enabled: boolean;
  pending: number;
  senders: number;
  blob_txs: number;
  blobs: number;
  bytes: number;
  gas_sum: number;
  value_wei: string;
  admitted: number;
  replaced: number;
  removed: number;
  cleared: number;
  evicted_included_by_us: number;
  evicted_included_by_other: number;
  evicted_nonce_too_low: number;
  evicted_ttl: number;
  evicted_strikes?: number;
  rejected: Record<string, number>;
  last_admitted_at?: string;
  version: number;
}

export interface TxPoolTx {
  hash: string;
  sender: string;
  nonce: number;
  type: number;
  to?: string;
  gas: number;
  max_fee_per_gas: string;
  max_priority_fee_per_gas: string;
  max_fee_per_blob_gas?: string;
  value_wei: string;
  blobs?: number;
  size: number;
  arrived: string;
  arrived_slot: number;
  seq: number;
  strikes?: number;
}

export interface TxPoolResponse {
  stats: TxPoolStats;
  txs: TxPoolTx[];
  total: number;
  offset: number;
  limit: number;
}

// Pool selection summary (dry-run preview and per-build results).
export interface TxSelectionSummary {
  selected: number;
  gas_sum: number;
  blobs?: number;
  skipped?: Record<string, number>;
  inclusion_list_txs?: number;
  pool_size: number;
  base_fee?: string;
  blob_base_fee?: string;
  gas_budget: number;
  hashes?: string[];
}

export interface TxPoolPreviewResponse {
  selection: TxSelectionSummary;
  parent_hash: string;
  gas_limit: number;
  max_blobs: number;
  ordering: string;
}

// How a local payload's transaction list was assembled.
export interface LocalBuildInfo {
  tx_source: string;
  selection?: TxSelectionSummary;
  explicit_txs?: number;
  expected_hashes?: string[];
  inclusion_list_txs?: number;
  inclusion_list_dropped?: boolean;
  dropped_by_el?: number;
  submitted_txs: number;
  attempts?: number;
  dropped?: { hash: string; reason: string }[];
  built_at?: string;
}

// SSE local_build event (slot-scoped): one build target's local outcome.
export interface LocalBuildStreamEvent {
  slot: number;
  candidate?: string;
  status: string; // ready | failed | skipped
  skip_reason?: string;
  error?: string;
  tx_source?: string;
  payload_source?: string;
  selected: boolean;
  fallback: boolean;
  local?: StreamPayloadSummary;
  el?: StreamPayloadSummary;
  info?: LocalBuildInfo;
  built_at?: number; // unix ms, when testing_buildBlockV1 returned (ready only)
  el_ready_at?: number; // unix ms, when the engine payload of the target was ready
  at: number;
}

export interface StreamPayloadSummary {
  block_hash: string;
  block_value_wei: string;
  num_transactions: number;
  num_blobs: number;
  gas_used: number;
  gas_limit: number;
}

// Payload reveal config (own section, shared by the p2p bidder and Builder
// API flows).
export interface RevealConfig {
  enabled: boolean;
  gate_mode: string; // time | vote | vote_or_time | vote_and_time
  time_ms: number;
  vote_threshold_pct: number;
  broadcast_validation: string; // gossip | consensus | consensus_and_equivocation
  max_attempts: number;
  retry_interval_ms: number;
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
  bid_min_amount: number;
  bid_increase: number;
  bid_interval: number;
  bid_subsidy: number;
  payload_build_delay?: number;
  bid_candidate?: string;
  key_strategy?: string;
  bid_keys_per_slot?: number;
  bid_keys_per_step?: number;
}

export interface ServiceStatus {
  epbs_available: boolean;
  epbs_enabled: boolean;
  epbs_registration_state: string; // "unknown" | "unregistered" | "waiting_gloas" | "pending" | "pending_finalization" | "registered" | "exiting" | "exited"
  builder_api_available: boolean;
  builder_api_enabled: boolean;
  lifecycle_available: boolean;
  lifecycle_enabled: boolean;
  local_build_available?: boolean;
  local_build_enabled?: boolean;
  local_build_reason?: string;
  txpool_available?: boolean;
  txpool_enabled?: boolean;
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
  // Fleet summary of the managed builder key set. Equal to the fields above on
  // a single-key deployment.
  key_count: number;
  key_target: number;
  keys_active: number;
  total_balance_gwei: number;
  total_pending_payments_gwei: number;
  total_effective_gwei: number;
}

export type BuilderKeyStatus =
  | 'unused'
  | 'depositing'
  | 'pending'
  | 'active'
  | 'exiting'
  | 'exited'
  | 'withdrawn';

export interface BuilderKeyState {
  key_index: number;
  pubkey: string;
  status: BuilderKeyStatus;
  builder_index: number;
  has_builder_index: boolean;
  balance_gwei: number;
  pending_payments_gwei: number;
  balance_adjustment_gwei: number;
  effective_balance_gwei: number;
  deposit_epoch: number;
  withdrawable_epoch: number;
  use_count: number;
  last_deposit_at?: number;
  last_exit_at?: number;
  last_topup_epoch?: number;
  bids_submitted: number;
  bids_won: number;
}

export interface BuilderKeysAggregate {
  target: number;
  managed: number;
  unused: number;
  depositing: number;
  pending: number;
  active: number;
  exiting: number;
  exited: number;
  withdrawn: number;
  total_balance_gwei: number;
  total_pending_payments_gwei: number;
  total_effective_gwei: number;
}

export interface BuilderKeysSettings {
  target_count: number;
  max_index: number;
  auto_deposit: boolean;
  auto_exit: boolean;
}

export interface BuilderKeysResponse {
  keys: BuilderKeyState[];
  aggregate: BuilderKeysAggregate;
  settings: BuilderKeysSettings;
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
  builder_index?: number;
  key_index?: number;
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
// startedAt/time bracket the attempt (construction + submit call); startedAt
// is unset on skips where nothing was attempted.
export interface RevealAttempt {
  time: number;
  startedAt?: number;
  transport?: string;
  success: boolean;
  skipped: boolean;
  skipReason?: string;
  error?: string;
  attempt: number;
  maxAttempts: number;
  envelope?: EnvelopeDetail;
}

// Raw single-attestation subnet coverage vs. next-block attesters. `low` marks
// the vote graph unreliable (beacon node likely without subscribe-all-subnets).
export interface VoteCoverage {
  slots: number;
  attesters: number;
  seen_pct: number;
  low: boolean;
}

// Per-name head-vote arrival heatmap of one slot (REST:
// GET /api/buildoor/head-votes/{slot}); counts are vote arrivals per
// fixed-width time bucket from the slot start.
export interface HeadVoteDetail {
  slot: number;
  root: string;
  slot_start_ms: number;
  bucket_ms: number;
  bucket_count: number;
  total_members: number;
  rows: HeadVoteDetailRow[];
}

export interface HeadVoteDetailRow {
  name: string;
  members: number;
  seen: number;
  in_block_unseen: number;
  counts: number[];
}

export interface HeadVoteDataPoint {
  time: number;
  pct: number;
  eth: number;
  voteCount?: number;
  thresholdMet?: boolean;
}

// Execution payload envelope summary (payload_available / reveal events;
// list fields aggregated to counts).
export interface EnvelopeDetail {
  block_hash?: string;
  builder_index?: number;
  block_number?: number;
  gas_limit?: number;
  gas_used?: number;
  base_fee_per_gas?: string;
  extra_data?: string;
  blob_gas_used?: number;
  num_transactions?: number;
  num_withdrawals?: number;
  num_exec_requests?: number;
}

// Display summary of an imported beacon block (block_detail event; list
// fields aggregated to counts).
export interface BlockDetail {
  slot: number;
  proposer_index: number;
  parent_root: string;
  state_root: string;
  graffiti: string; // 0x-hex
  num_attestations: number;
  num_proposer_slashings?: number;
  num_attester_slashings?: number;
  num_deposits?: number;
  num_voluntary_exits?: number;
  num_bls_changes?: number;
  sync_participation: number;
  num_blob_commitments?: number;
  num_payload_attestations?: number;
  bid?: {
    builder_index: number;
    value_gwei: number;
    execution_payment_gwei: number;
    block_hash: string;
    parent_block_hash: string;
    gas_limit: number;
    fee_recipient: string;
    num_blob_kzgs?: number;
  };
  payload?: {
    block_number: number;
    block_hash: string;
    gas_limit: number;
    gas_used: number;
    num_transactions: number;
    num_withdrawals: number;
  };
}

// Full built-payload properties from the payload_ready event (transactions,
// withdrawals, blobs, execution requests aggregated to counts).
export interface PayloadDetail {
  blockNumber?: number;
  feeRecipient?: string;
  gasLimit?: number;
  gasUsed?: number;
  baseFeePerGas?: string; // wei
  extraData?: string; // 0x-hex
  blobGasUsed?: number;
  excessBlobGas?: number;
  numTransactions?: number;
  numWithdrawals?: number;
  numBlobs?: number;
  numExecRequests?: number;
}

// Build-parent candidate keys: which beacon block / execution payload a
// candidate payload extends (see the backend chain.CandidateKey).
export type CandidateKey =
  | 'parent_full'
  | 'parent_empty'
  | 'grandparent_full'
  | 'grandparent_empty'
  | '';

// CandidateBuild tracks one candidate payload build of a slot (a slot may
// build several payloads on different parents for reorg preparedness).
export interface CandidateBuild {
  candidate: CandidateKey;
  startedAt?: number;
  readyAt?: number;
  failedAt?: number;
  failed?: boolean;
  error?: string;
  blockHash?: string;
  blockValueGwei?: number;
  // Parent tuple this candidate payload extends.
  parentBlockHash?: string;
  parentBlockNumber?: number;
  parentBlockRoot?: string;
  parentSlot?: number;
  detail?: PayloadDetail;
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
  // Candidate of the primary payload (the most canonical ready build).
  payloadCandidate?: CandidateKey;
  // Every candidate build of the slot (present when candidates ran).
  candidateBuilds?: CandidateBuild[];
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
  // Set while a reveal attempt's submit call is in flight (cleared by the
  // completion event); drives the live-growing call span.
  revealInFlight?: { attempt: number; startedAt: number };
  // Full built-payload properties (list fields aggregated to counts).
  payloadDetail?: PayloadDetail;
  // Imported block summary (from the block_detail event).
  blockDetail?: BlockDetail;
  // Envelope summary attached to the payload_available event.
  payloadAvailableDetail?: EnvelopeDetail;
  headVotes?: HeadVoteDataPoint[];
  headVoteThresholdPct?: number;
  headVoteThresholdMetAt?: number;
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
  // Local build extension outcome for the slot's primary target.
  localBuild?: LocalBuildStreamEvent;
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
  prevRandao?: string;
  feeRecipient: string;
  parentBeaconBlockRoot?: string;
  targetGasLimit: number;
  withdrawalsCount: number;
  inclusionListCount?: number;
  receivedAt: number;
}

export interface OurBid {
  time: number;
  value: number;
  blockHash?: string;
  count: number;
  success: boolean;
  error?: string;
  // Full bid message properties (blob commitments aggregated to a count).
  executionPayment?: number;
  feeRecipient?: string;
  gasLimit?: number;
  builderIndex?: number;
  // The managed builder key that signed the bid: with several keys bidding one
  // slot, the builder index alone does not say which of ours it was.
  keyIndex?: number;
  parentBlockHash?: string;
  parentBlockRoot?: string;
  numBlobCommitments?: number;
}

export interface ExternalBid {
  time: number;
  value: number;
  builder: number;
  blockHash?: string;
  // Full bid message properties (blob commitments aggregated to a count).
  executionPayment?: number;
  feeRecipient?: string;
  gasLimit?: number;
  parentBlockHash?: string;
  parentBlockRoot?: string;
  numBlobCommitments?: number;
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
  bid_candidate?: string; // auto | all | candidate key
}

export interface BuilderAPIPlan {
  mode: ActionMode;
  value_subsidy_gwei?: number;
  total_value_override_gwei?: number;
  execution_payment_gwei?: number; // unbacked execution_payment portion, wins over percent
  execution_payment_percent?: number; // 0-100, portion of the served total value
  ignore_preference_limit?: boolean; // serve beyond the proposer's max_execution_payment
  response_delay_ms?: number;
  serve_candidates?: string; // all | canonical_only | key list
}

export interface RevealPlan {
  mode: ActionMode;
  reveal_time_ms?: number;
  gate_mode?: string; // time | vote | vote_or_time | vote_and_time
  vote_threshold_pct?: number;
  broadcast_validation?: string; // gossip | consensus | consensus_and_equivocation
}

// The build category has no custom/disabled mode: it only tweaks how the
// slot's payload is built when a build happens.
export interface BuildPlan {
  reorg_parent_payload?: boolean;
  // Per-slot candidate policy overrides: candidate key -> auto/always/never.
  candidates?: Record<string, string>;
  // Local build extension overrides (absent = inherit the global settings).
  local?: LocalBuildPlan;
}

// Per-slot local build (testing_buildBlockV1) overrides; every field optional.
export interface LocalBuildPlan {
  enabled?: boolean;
  payload_source?: string; // el | local | local_or_el
  tx_source?: string; // txpool | empty | el_mempool | explicit
  transactions?: string[]; // 0x-hex raw transactions (tx_source explicit)
  queued?: string[]; // ordered pool tx hashes, exact or the build fails (tx_source queued)
  build_el_payload?: boolean;
  max_txs?: number;
  gas_fill_pct?: number;
  ordering?: string; // fifo | tip | random
}

// The transforms category has no mode: each field is a jq expression applied
// to the object's JSON (empty = no transform).
export interface TransformPlan {
  payload?: string;
  bid?: string;
  envelope?: string;
}

export interface SlotPlan {
  slot: number;
  bid?: BidPlan;
  builder_api?: BuilderAPIPlan;
  reveal?: RevealPlan;
  build?: BuildPlan;
  transforms?: TransformPlan;
  // Opts the slot out of every recurring rule (runs on the global baseline).
  ignore_rules?: boolean;
  // Set on plans synthesized from a recurring rule; never on stored plans.
  rule_id?: string;
  updated_at: string;
  updated_by: string;
}

// A recurring plan template: its categories apply to every slot whose index
// within the epoch matches, unless the slot carries an explicit plan.
export interface SlotRule {
  id: string;
  enabled: boolean;
  description?: string;
  slots_in_epoch: number[];
  from_epoch?: number;
  to_epoch?: number;
  bid?: BidPlan;
  builder_api?: BuilderAPIPlan;
  reveal?: RevealPlan;
  build?: BuildPlan;
  transforms?: TransformPlan;
  updated_at?: string;
  updated_by?: string;
}

export interface ActionPlanRulesResponse {
  rules: SlotRule[];
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
  build?: BuildPlan | null;
  transforms?: TransformPlan | null;
  // Two-state: absent = unchanged, true/false = set the rule opt-out.
  ignore_rules?: boolean;
  set?: Record<string, number | string | boolean | object | null>;
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
  reorg_parent_payload?: boolean;
  candidate_modes?: Record<string, string>;
  local?: ResolvedLocalBuildSettings;
}

export interface ResolvedLocalBuildSettings {
  enabled: boolean;
  payload_source: string;
  tx_source: string;
  transactions?: string[];
  queued?: string[];
  build_el_payload: boolean;
  max_txs?: number;
  gas_fill_pct: number;
  ordering: string;
  allow_blobs_without_bundle?: boolean;
  forced?: boolean;
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
  bid_candidate?: string;
  forced?: boolean;
}

export interface ResolvedBuilderAPISettings {
  subsidy_gwei: number;
  total_value_gwei?: number;
  execution_payment_gwei?: number;
  execution_payment_percent?: number;
  ignore_preference_limit?: boolean;
  delay_ms?: number;
  serve_candidates?: string;
  forced?: boolean;
}

export interface ResolvedRevealSettings {
  suppressed?: boolean;
  reveal_time_ms: number;
  gate_mode: string;
  vote_threshold_pct: number;
  broadcast_validation: string;
  max_attempts: number;
  retry_interval_ms: number;
  bypass_deadline?: boolean;
}

// FrozenPlan is the immutable per-slot execution snapshot; a nil bid /
// builder_api category means the category is suppressed for the slot.
export interface ResolvedTransforms {
  payload?: string;
  bid?: string;
  envelope?: string;
}

export interface FrozenPlan {
  slot: number;
  plan?: SlotPlan;
  fork: string;
  frozen_at: string;
  build: ResolvedBuildSettings;
  bid?: ResolvedBidSettings;
  builder_api?: ResolvedBuilderAPISettings;
  reveal?: ResolvedRevealSettings;
  transforms?: ResolvedTransforms;
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
  candidate?: string; // build-parent candidate key ("" / absent = unclassified)
  artifact_idx?: number; // payload artifact index of this build
  block_hash?: string;
  block_value_wei?: string;
  num_transactions?: number;
  num_blobs?: number;
  fee_recipient?: string;
  // Full built-payload properties (list fields aggregated to counts).
  block_number?: number;
  parent_hash?: string;
  state_root?: string;
  receipts_root?: string;
  prev_randao?: string;
  timestamp?: number;
  gas_limit?: number;
  gas_used?: number;
  base_fee_per_gas?: string;
  extra_data?: string;
  blob_gas_used?: number;
  excess_blob_gas?: number;
  num_withdrawals?: number;
  num_execution_requests?: number;
  attributes?: AttributesSnapshot;
  // Which build produced the payload feeding the consumers (el | local).
  source?: string;
  // The local payload was wanted but the engine payload was used.
  fallback?: boolean;
  local_build?: LocalBuildOutcome;
  error?: string;
  at: string;
}

// The local build's outcome for one build target.
export interface LocalBuildOutcome {
  status: string; // ready | failed | skipped
  skip_reason?: string;
  error?: string;
  tx_source?: string;
  payload_source?: string;
  selected: boolean;
  block_hash?: string;
  block_value_wei?: string;
  num_transactions?: number;
  num_blobs?: number;
  gas_used?: number;
  gas_limit?: number;
  artifact_idx?: number;
  info?: LocalBuildInfo;
  at: string;
}

// The payload_attributes snapshot a build ran on (list fields aggregated).
export interface AttributesSnapshot {
  proposer_index: number;
  parent_block_root: string;
  parent_block_hash: string;
  parent_block_number: number;
  timestamp: number;
  prev_randao: string;
  suggested_fee_recipient: string;
  parent_beacon_block_root?: string;
  target_gas_limit?: number;
  num_withdrawals: number;
  num_inclusion_list_txs?: number;
}

export interface SlotBidAttempt {
  status: BidAttemptStatus;
  transport: string;
  total_value_gwei: number;
  execution_payment_gwei?: number;
  competitor_high_gwei?: number;
  artifact_index?: number;
  // Full bid message properties (blob commitments aggregated to a count).
  block_hash?: string;
  parent_block_hash?: string;
  parent_block_root?: string;
  prev_randao?: string;
  fee_recipient?: string;
  gas_limit?: number;
  builder_index?: number;
  key_index?: number;
  num_blob_commitments?: number;
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
  skip_reason?: string; // "plan_disabled" | "disabled" | "late" | "vote_gate_timeout"
  error?: string;
  attempt: number;
  at: string;
  started_at?: string;
}

export type PayloadCanonicalStatus = 'pending' | 'canonical' | 'missed' | 'orphaned';

export interface SlotInclusionResult {
  source: string; // "epbs" | "builder_api"
  block_hash: string;
  num_transactions: number;
  num_blobs: number;
  value_wei: string;
  value_eth: string;
  timestamp: string;
  // Canonical verdict for the won payload, derived from the canonical chain's
  // next block (revised on reorgs while inside the tracking window).
  payload_status?: PayloadCanonicalStatus;
  payload_check_slot?: number | string;
}

export type TxPlanStatus = 'pending' | 'match' | 'mismatch' | 'block_not_found' | 'not_included' | 'missed' | 'orphaned';

// Post-inclusion verdict of a locally built payload: does the canonical
// block hold exactly the submitted transactions, in order?
export interface TxPlanResult {
  tx_source: string;
  expected_count: number;
  expected_hashes: string[];
  truncated?: boolean;
  status: TxPlanStatus;
  included_count?: number;
  first_mismatch?: number;
  detail?: string;
  verified_at?: string;
}

export interface SlotResult {
  slot: number;
  epoch: number;
  fork: string;
  applied_plan?: FrozenPlan;
  build?: BuildOutcome;
  // Every candidate build of the slot (present when more than one ran);
  // build stays the primary outcome.
  builds?: BuildOutcome[];
  bids?: SlotBidAttempt[];
  block_submissions?: SlotBlockSubmission[];
  reveal_attempts?: SlotRevealAttempt[];
  inclusion?: SlotInclusionResult;
  tx_plan?: TxPlanResult;
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
