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
}

export type InstanceStatus =
  | { state: 'loading' }
  | { state: 'online'; data: OverviewResponse; lastUpdated: number }
  | { state: 'error'; error: string; lastUpdated: number };
