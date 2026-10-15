import type {
  BuilderInfo,
  BuilderKeysAggregate,
  Config,
  LocalBuildStatus,
  ServiceStatus,
  Stats,
  TxPoolStats,
} from '../types';
import type { Tone } from '../components/ui/Tag';
import type { FlowId } from '../stores/settingsDrawerStore';
import { formatNumber, formatSetting, readSetting, type SettingContext } from './values';

// Everything a flow card or drawer header derives its state from.
export interface FlowContext extends SettingContext {
  stats: Stats | null;
  builderInfo: BuilderInfo | null;
  builderKeysAggregate: BuilderKeysAggregate | null;
  txPoolStats: TxPoolStats | null;
  localBuild: LocalBuildStatus | null;
}

export interface FlowStatus {
  label: string;
  tone: Tone;
  // One-line headline metric under the status tag.
  metric: string;
}

// A read-only summary row. `keys` are the settings it shows (their source
// tags mark the row); `value` renders it, defaulting to the formatted
// values of the keys joined by " · ".
export interface SummaryRow {
  label: string;
  keys: string[];
  value?: (ctx: FlowContext) => string;
}

export interface FlowSection {
  title?: string;
  keys: string[];
}

export interface FlowTab {
  id: string;
  label: string;
  sections: FlowSection[];
  // Non-setting content rendered above the fields (see SettingsDrawer).
  panel?: 'el-status';
}

// The on/off control of a flow: the setting it writes and why it may be
// unavailable (the module cannot run at all).
export interface FlowToggle {
  key: string;
  unavailable?: (ctx: FlowContext) => string | null;
}

export interface FlowDef {
  id: FlowId;
  title: string;
  // What the flow does, shown at the top of its drawer.
  description: string;
  toggle?: FlowToggle;
  tabs: FlowTab[];
  summary: SummaryRow[];
  status: (ctx: FlowContext) => FlowStatus;
}

const fmt = (key: string, ctx: FlowContext) => formatSetting(key, readSetting(key, ctx));
const raw = (key: string, ctx: FlowContext) => readSetting(key, ctx);
const count = (n: number | undefined) => formatNumber(n ?? 0);

function gweiToEth(gwei: number): string {
  return `${formatNumber(Number((gwei / 1e9).toFixed(4)))} ETH`;
}

function weiToEth(wei: string | undefined): string {
  if (!wei) return '—';
  const eth = Number(BigInt(wei) / 10n ** 12n) / 1e6;
  return `${formatNumber(Number(eth.toFixed(2)))} ETH`;
}

export const FLOWS: Record<FlowId, FlowDef> = {
  engine: {
    id: 'engine',
    title: 'Engine builder',
    description:
      'Builds the slot payloads through the engine API (forkchoiceUpdated → getPayload): when to start, ' +
      'which parent candidates to build on, which slots to build, and how long slot history is kept.',
    tabs: [
      { id: 'timing', label: 'Timing', sections: [{ keys: ['epbs.build_start_time', 'payload_build_time', 'extra_data'] }] },
      {
        id: 'candidates',
        label: 'Candidates',
        sections: [
          {
            title: 'Parent candidates',
            keys: [
              'build.candidate_parent_full',
              'build.candidate_parent_empty',
              'build.candidate_grandparent_full',
              'build.candidate_grandparent_empty',
            ],
          },
          {
            title: 'Engine builds',
            keys: ['build.parallel', 'build.speculative_build_time_ms', 'build.enforce_bid_gas_limit'],
          },
        ],
      },
      {
        id: 'schedule',
        label: 'Schedule',
        sections: [{ keys: ['schedule.mode', 'schedule.every_nth', 'schedule.next_n', 'schedule.start_slot'] }],
      },
      {
        id: 'history',
        label: 'History',
        sections: [
          {
            keys: ['slot_result_retention_epochs', 'slot_artifact_retention_epochs', 'slot_artifact_capture_enabled'],
          },
        ],
      },
    ],
    summary: [
      {
        label: 'Schedule',
        keys: ['schedule.mode', 'schedule.every_nth', 'schedule.next_n'],
        value: (ctx) => {
          const mode = raw('schedule.mode', ctx);
          if (mode === 'every_nth') return `every ${fmt('schedule.every_nth', ctx)}th slot`;
          if (mode === 'next_n') return `next ${fmt('schedule.next_n', ctx)} slots`;
          return fmt('schedule.mode', ctx);
        },
      },
      { label: 'Build start', keys: ['epbs.build_start_time'] },
      { label: 'Build time', keys: ['payload_build_time'] },
      {
        label: 'Candidates',
        keys: ['build.candidate_parent_full', 'build.candidate_parent_empty'],
        value: (ctx) =>
          `full ${fmt('build.candidate_parent_full', ctx)} · empty ${fmt('build.candidate_parent_empty', ctx)}`,
      },
      { label: 'Reorg candidates', keys: ['build.candidate_grandparent_full', 'build.candidate_grandparent_empty'] },
      {
        label: 'Engine builds',
        keys: ['build.parallel', 'build.speculative_build_time_ms'],
        value: (ctx) =>
          `${raw('build.parallel', ctx) ? 'parallel' : 'sequential'} · ${fmt('build.speculative_build_time_ms', ctx)}`,
      },
      {
        label: 'Bid gas limit',
        keys: ['build.enforce_bid_gas_limit'],
        value: (ctx) => (raw('build.enforce_bid_gas_limit', ctx) ? 'enforced' : 'off'),
      },
      {
        label: 'History',
        keys: ['slot_result_retention_epochs', 'slot_artifact_retention_epochs', 'slot_artifact_capture_enabled'],
        value: (ctx) =>
          `${count(Number(raw('slot_result_retention_epochs', ctx) ?? 0))} / ${count(
            Number(raw('slot_artifact_retention_epochs', ctx) ?? 0),
          )} epochs${raw('slot_artifact_capture_enabled', ctx) === false ? ' · no capture' : ''}`,
      },
    ],
    status: (ctx) => {
      const mode = raw('schedule.mode', ctx);
      const label = mode === 'every_nth' ? 'Every Nth slot' : mode === 'next_n' ? 'Next N slots' : 'Building';
      return {
        label,
        tone: 'ok',
        metric: `${count(ctx.stats?.slots_built)} built · ${count(ctx.stats?.blocks_included)} included`,
      };
    },
  },

  testing: {
    id: 'testing',
    title: 'Testing builder',
    description:
      "Builds an additional payload per slot through the EL's testing_buildBlockV1 from a chosen transaction " +
      'source (the buildoor mempool, the EL mempool, or both) and decides which payload feeds the bids.',
    toggle: {
      key: 'local_build.enabled',
      unavailable: (ctx) => {
        if (!ctx.serviceStatus) return null;
        if (ctx.serviceStatus.local_build_available === false) {
          return ctx.serviceStatus.local_build_reason || 'The EL does not expose testing_buildBlockV1';
        }
        return null;
      },
    },
    tabs: [
      {
        id: 'block',
        label: 'Block',
        sections: [
          {
            keys: [
              'local_build.payload_source',
              'local_build.tx_source',
              'local_build.build_el_payload',
              'local_build.allow_blobs_without_bundle',
              'local_build.start_delay_ms',
              'local_build.max_attempts',
            ],
          },
        ],
      },
      { id: 'el', label: 'Execution client', sections: [], panel: 'el-status' },
    ],
    summary: [
      {
        label: 'EL testing API',
        keys: [],
        value: (ctx) => {
          const availability = ctx.localBuild?.availability;
          if (!availability) return '—';
          if (!availability.configured) return 'no EL RPC';
          const el = availability.el_code ? `${availability.el_code} · ` : '';
          return `${el}${availability.available ? 'available' : 'unavailable'}`;
        },
      },
      { label: 'Bid with', keys: ['local_build.payload_source'] },
      { label: 'Transactions', keys: ['local_build.tx_source'] },
      { label: 'Keep engine build', keys: ['local_build.build_el_payload'] },
      {
        label: 'Blob encoding',
        keys: [],
        value: (ctx) => ctx.localBuild?.availability.blob_encoding || ctx.config?.local_build?.blob_encoding || '—',
      },
      { label: 'Blobs without bundle', keys: ['local_build.allow_blobs_without_bundle'] },
      {
        label: 'Start · attempts',
        keys: ['local_build.start_delay_ms', 'local_build.max_attempts'],
        value: (ctx) => `${fmt('local_build.start_delay_ms', ctx)} · ${fmt('local_build.max_attempts', ctx)}`,
      },
      {
        label: 'Plan checks',
        keys: [],
        value: (ctx) => {
          const checks = ctx.localBuild?.plan_checks;
          if (!checks) return '—';
          const failed = checks.mismatch + checks.block_not_found + checks.missed + checks.orphaned;
          return `${count(checks.checked)} · ${count(failed)} failed`;
        },
      },
    ],
    status: (ctx) => {
      const availability = ctx.localBuild?.availability;
      const elText = availability?.el_code ? `${availability.el_code} · ` : '';
      if (ctx.serviceStatus?.local_build_available === false) {
        return { label: 'Unavailable', tone: 'warn', metric: ctx.serviceStatus.local_build_reason || 'EL testing API unavailable' };
      }
      if (!ctx.serviceStatus?.local_build_enabled) {
        return { label: 'Off', tone: 'neutral', metric: `${elText}testing API available` };
      }
      const source = raw('local_build.payload_source', ctx);
      const label =
        source === 'local'
          ? 'Bidding custom'
          : source === 'local_or_el'
            ? 'Custom first'
            : source === 'higher_value'
              ? 'Higher value'
              : 'Comparing';
      return { label, tone: 'ok', metric: `${elText}${fmt('local_build.tx_source', ctx)}` };
    },
  },

  keys: {
    id: 'keys',
    title: 'Keys & candidates',
    description:
      'Which build candidate each bid uses and which builder keys sign the bids of the p2p bidder and the ' +
      'Builder API. A key is spent once one of its bids reaches the network.',
    tabs: [
      {
        id: 'candidates',
        label: 'Bid candidate',
        sections: [{ keys: ['epbs.bid_candidate', 'epbs.bid_candidate_switch', 'build.auto_weak_head_pct'] }],
      },
      {
        id: 'keys',
        label: 'Keys',
        sections: [
          {
            keys: ['epbs.key_strategy', 'epbs.bid_keys_per_slot', 'epbs.bid_keys_per_step', 'builder_api.key_strategy'],
          },
        ],
      },
    ],
    summary: [
      { label: 'Bid candidate', keys: ['epbs.bid_candidate'] },
      { label: 'Candidate switch', keys: ['epbs.bid_candidate_switch'] },
      { label: 'Key strategy', keys: ['epbs.key_strategy'] },
      { label: 'Keys per slot', keys: ['epbs.bid_keys_per_slot'] },
      { label: 'Keys per step', keys: ['epbs.bid_keys_per_step'] },
      { label: 'Builder API keys', keys: ['builder_api.key_strategy'] },
      { label: 'Weak head below', keys: ['build.auto_weak_head_pct'] },
      {
        label: 'Active keys',
        keys: [],
        value: (ctx) =>
          ctx.builderKeysAggregate
            ? `${count(ctx.builderKeysAggregate.active)} of ${count(ctx.builderKeysAggregate.target)}`
            : '—',
      },
    ],
    status: (ctx) => ({
      label: fmt('epbs.key_strategy', ctx),
      tone: 'info',
      metric: ctx.builderKeysAggregate
        ? `${count(ctx.builderKeysAggregate.active)} of ${count(ctx.builderKeysAggregate.target)} keys active`
        : 'which key and parent each bid uses',
    }),
  },

  lifecycle: {
    id: 'lifecycle',
    title: 'Lifecycle',
    description:
      'Keeps the managed builder keys registered and funded: deposits keys up to the target, exits surplus ' +
      'keys and tops up keys whose balance runs low, one transaction per pass from the funding wallet.',
    toggle: {
      key: 'lifecycle_enabled',
      unavailable: (ctx) =>
        ctx.serviceStatus && !ctx.serviceStatus.lifecycle_available
          ? 'Lifecycle management needs --lifecycle, --el-rpc and --wallet-privkey'
          : null,
    },
    tabs: [
      {
        id: 'fleet',
        label: 'Fleet',
        sections: [
          {
            keys: [
              'builder_keys.target_count',
              'builder_keys.auto_deposit',
              'builder_keys.auto_exit',
              'builder_keys.max_index',
            ],
          },
        ],
      },
      { id: 'amounts', label: 'Amounts', sections: [{ keys: ['deposit_amount', 'topup_threshold', 'deposit_max_fee', 'early_deposit_tip'] }] },
    ],
    summary: [
      { label: 'Target keys', keys: ['builder_keys.target_count'] },
      { label: 'Auto deposit', keys: ['builder_keys.auto_deposit'] },
      { label: 'Auto exit', keys: ['builder_keys.auto_exit'] },
      { label: 'Max index', keys: ['builder_keys.max_index'] },
      { label: 'Deposit amount', keys: ['deposit_amount'] },
      { label: 'Top up below', keys: ['topup_threshold'] },
      { label: 'Max deposit fee', keys: ['deposit_max_fee'] },
      { label: 'Wallet', keys: [], value: (ctx) => weiToEth(ctx.builderInfo?.wallet_balance_wei) },
    ],
    status: (ctx) => {
      const keys = ctx.builderKeysAggregate
        ? `${count(ctx.builderKeysAggregate.active)} / ${count(ctx.builderKeysAggregate.target)} keys active`
        : '';
      if (ctx.serviceStatus && !ctx.serviceStatus.lifecycle_available) {
        return { label: 'Not available', tone: 'neutral', metric: 'needs --lifecycle and a wallet' };
      }
      if (!ctx.serviceStatus?.lifecycle_enabled) return { label: 'Paused', tone: 'neutral', metric: keys };
      return { label: 'Active', tone: 'ok', metric: keys };
    },
  },

  epbs: {
    id: 'epbs',
    title: 'ePBS bidder',
    description:
      'Bids for slots over p2p gossip (Gloas): a bid window relative to the slot start, escalating bids every ' +
      'interval, and the value each bid offers on top of the block value.',
    toggle: {
      key: 'epbs_enabled',
      unavailable: (ctx) =>
        ctx.serviceStatus && !ctx.serviceStatus.epbs_available ? 'The Gloas fork is not scheduled' : null,
    },
    tabs: [
      {
        id: 'window',
        label: 'Window',
        sections: [
          {
            keys: ['epbs.bid_start_time', 'epbs.bid_end_time', 'epbs.bid_interval', 'epbs.head_vote_threshold_pct'],
          },
        ],
      },
      {
        id: 'amounts',
        label: 'Amounts',
        sections: [
          { keys: ['epbs.bid_min_amount', 'epbs.bid_increase', 'epbs.bid_subsidy', 'epbs.bid_value_override'] },
        ],
      },
    ],
    summary: [
      {
        label: 'Window',
        keys: ['epbs.bid_start_time', 'epbs.bid_end_time'],
        value: (ctx) => `${fmt('epbs.bid_start_time', ctx)} → ${fmt('epbs.bid_end_time', ctx)}`,
      },
      { label: 'Interval', keys: ['epbs.bid_interval'] },
      { label: 'Minimum', keys: ['epbs.bid_min_amount'] },
      { label: 'Increase', keys: ['epbs.bid_increase'] },
      { label: 'Subsidy', keys: ['epbs.bid_subsidy'] },
      { label: 'Absolute value', keys: ['epbs.bid_value_override'] },
      { label: 'Vote marker', keys: ['epbs.head_vote_threshold_pct'] },
      {
        label: 'Registration',
        keys: [],
        value: (ctx) => (ctx.serviceStatus?.epbs_registration_state || '—').replace(/_/g, ' '),
      },
    ],
    status: (ctx) => {
      const metric = `${count(ctx.stats?.bids_submitted)} bids · ${count(ctx.stats?.bids_won)} won`;
      const status = ctx.serviceStatus;
      if (status && !status.epbs_available) return { label: 'Not scheduled', tone: 'neutral', metric: 'Gloas not scheduled' };
      if (!status?.epbs_enabled) return { label: 'Paused', tone: 'neutral', metric };
      switch (status.epbs_registration_state) {
        case 'waiting_gloas':
          return { label: 'Waiting for Gloas', tone: 'warn', metric };
        case 'pending':
        case 'pending_finalization':
        case 'unregistered':
          return { label: 'Registering', tone: 'warn', metric };
        case 'exiting':
        case 'exited':
          return { label: 'Exited', tone: 'danger', metric };
        default:
          return { label: 'Bidding', tone: 'ok', metric };
      }
    },
  },

  'builder-api': {
    id: 'builder-api',
    title: 'Builder API',
    description:
      'Serves bids to proposers that call buildoor directly (getHeader before Gloas, getExecutionPayloadBid ' +
      'after) and publishes the blocks they sign.',
    toggle: {
      key: 'builder_api_enabled',
      unavailable: (ctx) =>
        ctx.serviceStatus && !ctx.serviceStatus.builder_api_available ? 'The Builder API needs --api-port' : null,
    },
    tabs: [
      {
        id: 'value',
        label: 'Value',
        sections: [
          {
            keys: [
              'builder_api.block_value_subsidy_gwei',
              'builder_api.value_override_gwei',
              'builder_api.execution_payment_gwei',
              'builder_api.execution_payment_percent',
              'builder_api.ignore_preference_limit',
            ],
          },
        ],
      },
      {
        id: 'serving',
        label: 'Serving',
        sections: [
          { keys: ['builder_api.serve_candidates', 'builder_api.on_demand_build', 'builder_api.require_request_auth'] },
        ],
      },
    ],
    summary: [
      {
        label: 'Builder URL',
        keys: [],
        value: (ctx) => ctx.config?.builder_api?.builder_url?.replace(/^https?:\/\//, '') || '—',
      },
      { label: 'Subsidy', keys: ['builder_api.block_value_subsidy_gwei'] },
      { label: 'Absolute value', keys: ['builder_api.value_override_gwei'] },
      {
        label: 'Execution payment',
        keys: ['builder_api.execution_payment_gwei', 'builder_api.execution_payment_percent'],
      },
      {
        label: 'Preference cap',
        keys: ['builder_api.ignore_preference_limit'],
        value: (ctx) => (raw('builder_api.ignore_preference_limit', ctx) ? 'ignored' : 'respected'),
      },
      { label: 'Serve candidates', keys: ['builder_api.serve_candidates'] },
      { label: 'On-demand build', keys: ['builder_api.on_demand_build'] },
      { label: 'Request auth', keys: ['builder_api.require_request_auth'] },
    ],
    status: (ctx) => {
      const metric = `${count(ctx.stats?.builder_api_registered_validators)} validators · ${count(
        ctx.stats?.builder_api_blocks_published,
      )} blocks`;
      const status = ctx.serviceStatus;
      if (status && !status.builder_api_available) return { label: 'Not available', tone: 'neutral', metric: 'needs --api-port' };
      if (!status?.builder_api_enabled) return { label: 'Paused', tone: 'neutral', metric };
      return { label: 'Serving', tone: 'ok', metric };
    },
  },

  reveal: {
    id: 'reveal',
    title: 'Payload reveal',
    description:
      'Publishes the payload envelope of every won slot (both flows) once its gate opens: at a time, at the ' +
      'head-vote payment quorum, or a combination of both.',
    toggle: { key: 'reveal.enabled' },
    tabs: [
      { id: 'gate', label: 'Gate', sections: [{ keys: ['reveal.gate_mode', 'reveal.time_ms', 'reveal.vote_threshold_pct'] }] },
      {
        id: 'publishing',
        label: 'Publishing',
        sections: [
          {
            keys: [
              'reveal.broadcast_validation',
              'reveal.max_attempts',
              'reveal.retry_interval_ms',
              'reveal.rebind_on_reorg',
            ],
          },
        ],
      },
    ],
    summary: [
      { label: 'Gate', keys: ['reveal.gate_mode'] },
      { label: 'Reveal time', keys: ['reveal.time_ms'] },
      { label: 'Vote threshold', keys: ['reveal.vote_threshold_pct'] },
      { label: 'Validation', keys: ['reveal.broadcast_validation'] },
      { label: 'Attempts', keys: ['reveal.max_attempts'] },
      { label: 'Retry interval', keys: ['reveal.retry_interval_ms'] },
      { label: 'Rebind on reorg', keys: ['reveal.rebind_on_reorg'] },
      {
        label: 'Outcomes',
        keys: [],
        value: (ctx) =>
          ctx.stats
            ? `${count(ctx.stats.reveals_success)} sent · ${count(ctx.stats.reveals_skipped)} withheld`
            : '—',
      },
    ],
    status: (ctx) => {
      const metric = ctx.stats
        ? `${count(ctx.stats.reveals_success)} revealed · ${count(ctx.stats.reveals_failed)} failed`
        : '';
      if (raw('reveal.enabled', ctx) === false) return { label: 'Withholding', tone: 'warn', metric };
      return { label: 'Armed', tone: 'ok', metric };
    },
  },

  mempool: {
    id: 'mempool',
    title: 'Mempool /rpc',
    description:
      'The private transaction pool behind /rpc: what it admits, how it fills custom blocks, and who may call ' +
      'the endpoint at which rate.',
    toggle: {
      key: 'txpool.enabled',
      unavailable: (ctx) =>
        ctx.serviceStatus && ctx.serviceStatus.txpool_available === false ? 'The mempool needs --el-rpc' : null,
    },
    tabs: [
      {
        id: 'pool',
        label: 'Pool',
        sections: [
          {
            title: 'Block filling',
            keys: ['txpool.ordering', 'txpool.max_txs_per_block', 'txpool.gas_fill_pct', 'txpool.base_fee_ceiling_gwei'],
          },
          {
            title: 'Pool limits',
            keys: [
              'txpool.max_pool_txs',
              'txpool.max_txs_per_sender',
              'txpool.tx_ttl_slots',
              'txpool.max_strikes',
              'txpool.forward_to_el',
            ],
          },
        ],
      },
      {
        id: 'access',
        label: 'Access',
        sections: [
          {
            title: 'Callers',
            keys: [
              'txpool.auth',
              'txpool.rpc_anon_rate',
              'txpool.rpc_anon_scope',
              'txpool.rpc_key_rate',
              'txpool.rpc_proxy_hops',
            ],
          },
          { title: 'Read upstreams', keys: ['txpool.rpc_upstreams'] },
        ],
      },
    ],
    summary: [
      { label: 'Ordering', keys: ['txpool.ordering'] },
      { label: 'Per block', keys: ['txpool.max_txs_per_block', 'txpool.gas_fill_pct'] },
      { label: 'Pool limits', keys: ['txpool.max_pool_txs', 'txpool.max_txs_per_sender'] },
      { label: 'Expiry', keys: ['txpool.tx_ttl_slots', 'txpool.max_strikes'] },
      { label: 'Base fee ceiling', keys: ['txpool.base_fee_ceiling_gwei'] },
      { label: 'Forward to EL', keys: ['txpool.forward_to_el'] },
      {
        label: 'Access',
        keys: ['txpool.auth', 'txpool.rpc_anon_rate'],
        value: (ctx) => `${fmt('txpool.auth', ctx)} · ${fmt('txpool.rpc_anon_rate', ctx)}`,
      },
      { label: 'Upstreams', keys: ['txpool.rpc_upstreams'] },
    ],
    status: (ctx) => {
      const pending = `${count(ctx.txPoolStats?.pending)} txs queued`;
      if (ctx.serviceStatus && ctx.serviceStatus.txpool_available === false) {
        return { label: 'Unavailable', tone: 'neutral', metric: 'needs --el-rpc' };
      }
      if (!ctx.serviceStatus?.txpool_enabled) return { label: 'Off', tone: 'neutral', metric: pending };
      return { label: 'Accepting', tone: 'ok', metric: pending };
    },
  },
};

// Card order of the dashboard: build side first, then bidding and serving.
export const FLOW_ORDER: FlowId[] = [
  'engine',
  'testing',
  'keys',
  'lifecycle',
  'epbs',
  'builder-api',
  'reveal',
  'mempool',
];

export { gweiToEth };
