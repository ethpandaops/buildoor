export interface OverviewHost {
  id: number;
  url: string;
  label: string;
}

export interface OverviewELClient {
  code?: string;
  name?: string;
  version?: string;
  commit?: string;
}

export interface OverviewServices {
  epbs_available: boolean;
  epbs_enabled: boolean;
  epbs_registration_state?: string;
  builder_api_available: boolean;
  builder_api_enabled: boolean;
  lifecycle_available: boolean;
  lifecycle_enabled: boolean;
}

export interface OverviewBalances {
  cl_balance_gwei?: number;
  pending_payments_gwei?: number;
  effective_balance_gwei?: number;
  wallet_address?: string;
  wallet_balance_wei?: string;
}

export interface OverviewStats {
  slots_built: number;
  blocks_included: number;
  bids_submitted: number;
  bids_won: number;
  builder_api_headers_requested: number;
  builder_api_blocks_published: number;
  builder_api_registered_validators: number;
}

// One managed builder key, as listed by the overview endpoint.
export interface OverviewBuilderKey {
  key_index: number;
  pubkey: string;
  status: string;
  builder_index: number;
  has_builder_index: boolean;
  balance_gwei?: number;
}

// The managed fleet. Absent on instances predating the managed key set, which
// is why builder_pubkey/builder_index below are still populated (primary key).
export interface OverviewBuilders {
  count: number;
  target: number;
  active: number;
  indexes: number[];
  keys?: OverviewBuilderKey[];
  total_balance_gwei: number;
  total_pending_payments_gwei: number;
  total_effective_gwei: number;
}

export interface OverviewResponse {
  version: string;
  running: boolean;
  builder_pubkey?: string;
  builder_index?: number;
  is_registered: boolean;
  current_slot: number;
  el_client?: OverviewELClient;
  services: OverviewServices;
  balances: OverviewBalances;
  stats: OverviewStats;
  builders?: OverviewBuilders;
}

export type InstanceStatus =
  | { state: 'loading' }
  | { state: 'online'; data: OverviewResponse; lastUpdated: number }
  | { state: 'error'; error: string; lastUpdated: number };
