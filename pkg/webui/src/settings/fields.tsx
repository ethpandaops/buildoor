import React from 'react';
import type { SettingField, SettingOption, SettingValue } from './types';

// The settings field registry: one entry per mutable backend setting
// (pkg/config/settings_keys.go + settings_fields.go). The settings drawer and
// the read-only summaries render from it. Every key is written as a quoted
// object key followed by a colon: pkg/webui/settings_coverage_test.go greps
// the frontend sources for `'<key>':` to prove each setting has an editor.
//
// Paths: the SSE `config` event is the JSON of config.Config, whose json tags
// match the setting keys, so most entries need no `path`. The three module
// enable flags are read from the SSE service_status object instead (the
// `service_status.` prefix), because the effective state includes the
// module's availability.

// ---------------------------------------------------------------------------
// Draft helpers
// ---------------------------------------------------------------------------

const num = (draft: Record<string, SettingValue>, key: string): number => {
  const value = draft[key];
  if (typeof value === 'number') return value;
  if (typeof value === 'string') return Number(value) || 0;
  return value ? 1 : 0;
};

const str = (draft: Record<string, SettingValue>, key: string): string => String(draft[key] ?? '');

const optionList = (options: Array<{ label: string; hint: React.ReactNode }>) => (
  <dl>
    {options.map((o) => (
      <React.Fragment key={o.label}>
        <dt>{o.label}</dt>
        <dd>{o.hint}</dd>
      </React.Fragment>
    ))}
  </dl>
);

// ---------------------------------------------------------------------------
// Upstreams (txpool.rpc_upstreams: comma-separated <target>@<priority>)
// ---------------------------------------------------------------------------

export interface UpstreamRow {
  target: string;
  priority: string;
}

// parseUpstreams mirrors config.ParseRPCUpstreams: the priority is the part
// after the last '@' unless that part holds a '/' (an '@' inside a URL).
export function parseUpstreams(spec: string | undefined): UpstreamRow[] {
  return (spec ?? '')
    .split(',')
    .map((part) => part.trim())
    .filter((part) => part !== '')
    .map((part) => {
      const at = part.lastIndexOf('@');
      if (at > 0 && !part.slice(at).includes('/')) {
        return { target: part.slice(0, at).trim(), priority: part.slice(at + 1).trim() };
      }
      return { target: part, priority: '0' };
    });
}

export function serializeUpstreams(rows: UpstreamRow[]): string {
  return rows
    .filter((row) => row.target.trim() !== '')
    .map((row) => `${row.target.trim()}@${row.priority.trim() === '' ? '0' : row.priority.trim()}`)
    .join(',');
}

// validateUpstreams applies the rules of config.ParseRPCUpstreams.
export function validateUpstreams(spec: string): string | null {
  const rows = parseUpstreams(spec);
  if (rows.length === 0) {
    return 'At least one upstream is required ("local" for the EL behind --el-rpc).';
  }
  const seen = new Set<string>();
  for (const row of rows) {
    if (!/^\d+$/.test(row.priority)) {
      return `Invalid priority "${row.priority}" for upstream ${row.target}.`;
    }
    if (row.target !== 'local') {
      let valid = false;
      try {
        const url = new URL(row.target);
        valid = url.host !== '' && ['http:', 'https:', 'ws:', 'wss:'].includes(url.protocol);
      } catch {
        valid = false;
      }
      if (!valid) return `Upstream "${row.target}" must be "local" or an http(s)/ws(s) URL.`;
    }
    if (seen.has(row.target)) return `Upstream "${row.target}" is listed twice.`;
    seen.add(row.target);
  }
  return null;
}

// ---------------------------------------------------------------------------
// Shared option sets
// ---------------------------------------------------------------------------

const CANDIDATE_MODE_OPTIONS: SettingOption[] = [
  { value: 'auto', label: 'Auto (chain signals)', short: 'auto' },
  { value: 'always', label: 'Always', short: 'always' },
  { value: 'never', label: 'Never', short: 'never' },
];

const KEY_STRATEGY_OPTIONS: SettingOption[] = [
  { value: 'round_robin', label: 'Round robin', short: 'round robin' },
  { value: 'single', label: 'Single (lowest-index key first)', short: 'single' },
  { value: 'random', label: 'Random', short: 'random' },
  { value: 'least_used', label: 'Least used', short: 'least used' },
];

const PAYLOAD_SOURCES = [
  { value: 'el', label: 'Engine block', short: 'engine', hint: 'Bids and reveals use the engine block. The custom block is built next to it for comparison only.' },
  { value: 'local', label: 'Custom block', short: 'custom', hint: 'Bids and reveals use the custom block. When it fails or is skipped, the slot has no payload (no engine fallback).' },
  { value: 'local_or_el', label: 'Custom block, else engine block', short: 'custom, else engine', hint: 'Bids and reveals use the custom block when it was built, otherwise the engine block.' },
  { value: 'higher_value', label: 'Higher-value block', short: 'higher value', hint: 'Both blocks are built; bids and reveals use the one with the higher block value (the engine block on a tie, the other one when a build fails).' },
];

const TX_SOURCES = [
  { value: 'txpool', label: 'buildoor mempool', short: 'buildoor mempool', hint: "Transactions from buildoor's own mempool (sent to /rpc, see the Mempool tab). The EL's mempool is not used." },
  { value: 'txpool_el_fill', label: 'buildoor mempool, then EL mempool', short: 'buildoor + EL mempool', hint: "buildoor's mempool first; the remaining gas is filled with the EL mempool's pending transactions (highest tip first, checked against the parent state like ours; blob transactions get their blobs from the EL's blob pool through engine_getBlobs; geth does not list its blob transactions, so they stay out there). If the EL refuses the list, the build retries once without the EL part." },
  { value: 'el_mempool', label: 'EL mempool', short: 'EL mempool', hint: 'The EL fills the block from its own mempool, like the engine build does (useful to compare the two build paths).' },
  { value: 'empty', label: 'None (empty block)', short: 'empty', hint: 'No transactions at all.' },
];

const ORDERINGS = [
  { value: 'fifo', label: 'First in, first out', short: 'fifo', hint: 'Arrival order.' },
  { value: 'tip', label: 'Highest tip first', short: 'tip', hint: 'Highest effective tip first.' },
  { value: 'random', label: 'Random', short: 'random', hint: 'Random order (nonces stay sequential per sender).' },
  { value: 'round_robin', label: 'Round robin per sender', short: 'round robin', hint: 'One transaction per sender in turn.' },
];

const AUTH_MODES = [
  { value: 'open', label: 'Open (anonymous callers allowed)', short: 'open', hint: 'Anonymous callers are allowed, rate-limited by the anonymous rate.' },
  { value: 'api_key', label: 'API key required', short: 'API key', hint: 'An API key, the static --txpool-auth-token or a WebUI login is required.' },
];

const ANON_SCOPES = [
  { value: 'per_ip', label: 'Per client IP', short: 'per IP', hint: 'Each client IP gets its own limit and stats row.' },
  { value: 'shared', label: 'Shared by all anonymous callers', short: 'shared', hint: 'One limit and one stats row for all anonymous callers together.' },
];

const toOptions = (list: Array<{ value: string; label: string; short?: string }>): SettingOption[] =>
  list.map(({ value, label, short }) => ({ value, label, short }));

// ---------------------------------------------------------------------------
// Build candidates
// ---------------------------------------------------------------------------

const candidateHelp = (key: string, hint: string, auto: string, defaultMode: string) => (
  <>
    {hint} Setting <code>build.{key}</code>, default <strong>{defaultMode}</strong>; per-slot
    action plans can override it.
    <dl>
      <dt>auto</dt>
      <dd>Follows chain signals: {auto}</dd>
      <dt>always</dt>
      <dd>Builds every scheduled slot.</dd>
      <dt>never</dt>
      <dd>Never builds this candidate.</dd>
    </dl>
  </>
);

const candidateField = (
  key: string,
  label: string,
  short: string,
  hint: string,
  auto: string,
  defaultMode: string,
): SettingField => ({
  label,
  short,
  kind: 'select',
  options: CANDIDATE_MODE_OPTIONS,
  hint,
  help: candidateHelp(key, hint, auto, defaultMode),
});

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

export const SETTING_FIELDS: Record<string, SettingField> = {
  // ----- Module enable flags (POST /api/services/toggle) -----
  'epbs_enabled': {
    label: 'ePBS p2p bidding',
    short: 'P2P bidding',
    kind: 'bool',
    path: 'service_status.epbs_enabled',
    writer: 'service-toggle',
    boolLabels: { on: 'active', off: 'inactive' },
    hint: 'Needs the Gloas fork scheduled on the beacon node.',
    help: (
      <>
        Global switch for p2p bidding via gossip (<code>epbs_enabled</code>). Bidding also needs the
        Gloas fork and at least one active builder key. Per-slot action plans override it either
        way: a <em>custom</em> bid plan bids even while this is off, a <em>disabled</em> one
        suppresses a slot while it is on.
      </>
    ),
  },
  'builder_api_enabled': {
    label: 'Builder API',
    kind: 'bool',
    path: 'service_status.builder_api_enabled',
    writer: 'service-toggle',
    boolLabels: { on: 'active', off: 'inactive' },
    hint: 'Needs --api-port.',
    help: (
      <>
        Global switch for the Builder API (<code>builder_api_enabled</code>): validator
        registrations, builder preferences and block submissions. Serving bids is decided per slot:
        a <em>custom</em> builder_api plan serves a slot even while this is off, a{' '}
        <em>disabled</em> one suppresses it while it is on. Only available when started with{' '}
        <code>--api-port</code>.
      </>
    ),
  },
  'lifecycle_enabled': {
    label: 'Lifecycle management',
    short: 'Lifecycle',
    kind: 'bool',
    path: 'service_status.lifecycle_enabled',
    writer: 'service-toggle',
    boolLabels: { on: 'active', off: 'paused' },
    hint: 'Needs --el-rpc and --wallet-privkey.',
    help: (
      <>
        Lifecycle management (<code>lifecycle_enabled</code>): keeps the managed key set at the
        target count by depositing, topping up and exiting builder keys from the funding wallet, one
        transaction per pass. Paused = no automatic lifecycle transactions. Only available when
        started with <code>--el-rpc</code> and <code>--wallet-privkey</code>.
      </>
    ),
  },

  // ----- Engine builder: build config -----
  'epbs.build_start_time': {
    label: 'Build start',
    kind: 'number',
    unit: 'ms',
    hint: 'Relative to the proposal slot start; negative = before the slot starts.',
    help: (
      <>
        When the slot&apos;s payload build starts, in ms relative to the proposal slot&apos;s start
        (<code>epbs.build_start_time</code>); negative = before the slot starts. Applies to p2p bids
        and the Builder API alike. Startup default: -3400 ms at 12 s slots, scaled to the slot time.
        If no payload attributes arrived by then, the previous slot&apos;s are re-used
        (missing-block fallback).
      </>
    ),
  },
  'payload_build_time': {
    label: 'Payload build delay',
    short: 'Build delay',
    kind: 'number',
    unit: 'ms',
    min: 0,
    hint: 'Time the EL gets to fill the block before buildoor fetches it.',
    help: (
      <>
        Time in ms the EL gets to fill the block between the build trigger (forkchoiceUpdated) and
        fetching it (getPayload) (<code>payload_build_time</code>). Too short returns a near-empty
        payload. Startup default: 2600 ms at 12 s slots, scaled to the slot time.
      </>
    ),
  },
  'extra_data': {
    label: 'Extra data',
    kind: 'text',
    placeholder: 'buildoor/',
    zeroLabel: "EL's own",
    hint: "Prefix injected into the block's extra-data field (max 32 bytes, padded with the EL's original extra data).",
    validate: (value) =>
      new TextEncoder().encode(String(value)).length > 32 ? 'Extra data is at most 32 bytes.' : null,
    help: (
      <>
        Prefix written into every built block&apos;s extra-data field (<code>extra_data</code>),
        followed by <code>/</code> and the EL&apos;s own extra data, truncated to 32 bytes. Marks
        blocks built by this instance. Empty = keep the EL&apos;s extra data unchanged.
      </>
    ),
  },

  // ----- Engine builder: schedule -----
  'schedule.mode': {
    label: 'Schedule mode',
    short: 'Schedule',
    kind: 'select',
    options: [
      { value: 'all', label: 'All slots', short: 'all' },
      { value: 'every_nth', label: 'Every Nth slot', short: 'every Nth' },
      { value: 'next_n', label: 'Next N slots', short: 'next N' },
    ],
    hint: 'Which slots the builder builds for.',
    help: (
      <>
        Which slots the builder builds for (<code>schedule.mode</code>). Slots before the start slot
        are always skipped; a per-slot action plan that activates bidding or Builder API serving
        builds regardless.
        <dl>
          <dt>all</dt>
          <dd>Build every slot.</dd>
          <dt>every_nth</dt>
          <dd>Build only every Nth slot, counted from the start slot (or slot 0).</dd>
          <dt>next_n</dt>
          <dd>Build the next N slots, then stop. The count restarts whenever any setting is saved while this mode is active.</dd>
        </dl>
      </>
    ),
  },
  'schedule.every_nth': {
    label: 'Build every Nth slot',
    short: 'Every Nth',
    kind: 'number',
    min: 1,
    hint: '1 = every slot.',
    inactiveWhen: (draft) =>
      str(draft, 'schedule.mode') === 'every_nth' ? null : 'Only used in the every Nth slot schedule mode.',
    help: (
      <>
        Slot step for <em>every_nth</em> mode (<code>schedule.every_nth</code>): builds slots where
        (slot - start slot) is a multiple of N. 1 = every slot. Ignored in other modes.
      </>
    ),
  },
  'schedule.next_n': {
    label: 'Slots to build',
    short: 'Next N',
    kind: 'number',
    min: 0,
    hint: 'Build budget; 0 = build nothing.',
    inactiveWhen: (draft) =>
      str(draft, 'schedule.mode') === 'next_n' ? null : 'Only used in the next N slots schedule mode.',
    help: (
      <>
        Build budget for <em>next_n</em> mode (<code>schedule.next_n</code>): the number of slots to
        build before stopping. 0 = build nothing. Plan-forced builds do not consume it. Ignored in
        other modes.
      </>
    ),
  },
  'schedule.start_slot': {
    label: 'Start slot',
    kind: 'number',
    min: 0,
    zeroLabel: 'no lower bound',
    hint: 'Earlier slots are skipped in every mode.',
    help: (
      <>
        First slot the schedule may build for (<code>schedule.start_slot</code>); earlier slots are
        skipped in every mode. Also the origin for <em>every_nth</em> counting. 0 = no lower bound.
      </>
    ),
  },

  // ----- Engine builder: build candidates -----
  'build.candidate_parent_full': candidateField(
    'candidate_parent_full',
    'Parent (full payload)',
    'Parent (full)',
    'The normal build: head block and its revealed payload.',
    'builds unless the head block’s payload is known to be withheld.',
    'always',
  ),
  'build.candidate_parent_empty': candidateField(
    'candidate_parent_empty',
    'Parent (empty payload)',
    'Parent (empty)',
    'Head block, but on the payload it built upon — the Gloas payload-miss case.',
    'builds until the head block’s payload is confirmed revealed.',
    'auto',
  ),
  'build.candidate_grandparent_full': candidateField(
    'candidate_grandparent_full',
    'Grandparent (reorg)',
    'Grandparent (reorg)',
    'Head block’s parent: the proposer reorgs the head block out.',
    'builds while the head block’s vote participation is below the weak head threshold.',
    'auto',
  ),
  'build.candidate_grandparent_empty': candidateField(
    'candidate_grandparent_empty',
    'Grandparent (empty payload)',
    'Grandparent (empty)',
    'Reorg combined with a withheld grandparent payload (rare).',
    'builds while the head is weak and the grandparent’s payload is not confirmed revealed.',
    'never',
  ),
  'build.auto_weak_head_pct': {
    label: 'Weak head threshold',
    short: 'Weak head below',
    kind: 'number',
    unit: '%',
    min: 0,
    max: 100,
    zeroLabel: 'off',
    hint: 'Head-vote participation below which the head counts as contested, arming the auto-mode reorg candidates. 0 disables the signal.',
    help: (
      <>
        Head-vote participation in percent below which the head block counts as contested
        (<code>build.auto_weak_head_pct</code>), arming the grandparent (reorg) candidates set to
        auto. 0 = off: auto reorg candidates never build.
      </>
    ),
  },
  'build.parallel': {
    label: 'Build candidates in parallel',
    short: 'Engine builds',
    kind: 'bool',
    boolLabels: { on: 'parallel', off: 'sequential' },
    hint: 'Builds every selected candidate concurrently, each with its own payload ID. Off serializes them (canonical first).',
    help: (
      <>
        How the slot&apos;s candidate builds run against the EL (<code>build.parallel</code>).
        <dl>
          <dt>parallel</dt>
          <dd>All selected candidates build concurrently, each with its own payload ID and the full payload build delay.</dd>
          <dt>sequential</dt>
          <dd>One after another, canonical (parent full) first so it keeps its scheduled start; speculative ones follow.</dd>
        </dl>
      </>
    ),
  },
  'build.speculative_build_time_ms': {
    label: 'Speculative build time',
    short: 'Speculative time',
    kind: 'number',
    unit: 'ms',
    min: 0,
    zeroLabel: 'same as build delay',
    hint: 'Shortens the speculative candidate builds so they fit alongside the canonical one. 0 uses the payload build delay.',
    inactiveWhen: (draft) =>
      draft['build.parallel'] === true ? 'Only used when candidates build sequentially.' : null,
    help: (
      <>
        EL build time in ms for the speculative candidates (everything but parent full) when builds
        run sequentially (<code>build.speculative_build_time_ms</code>). Ignored for parallel
        builds. 0 = same as the payload build delay.
      </>
    ),
  },
  'build.enforce_bid_gas_limit': {
    label: 'Enforce bid gas limit',
    kind: 'bool',
    hint: "Rewrites the built payload's gas limit to the exact value the bid gossip rules require when the EL ignored the proposer's target.",
    help: (
      <>
        Gloas only (<code>build.enforce_bid_gas_limit</code>): when the EL ignored the
        proposer&apos;s target gas limit, rewrites the built payload&apos;s gas limit to the exact
        value the bid gossip rules require (EL parent gas limit stepped toward the target) and
        recomputes the block hash. Skipped when the payload&apos;s gas used would not fit.
      </>
    ),
  },

  // ----- Slot history -----
  'slot_result_retention_epochs': {
    label: 'Result retention',
    kind: 'number',
    unit: 'epochs',
    min: 1,
    hint: 'Epochs of action plans and slot results kept before pruning.',
    help: (
      <>
        Epochs of per-slot history kept before pruning (<code>slot_result_retention_epochs</code>):
        slot results (build, bids, reveals, inclusion, Bids Won) and past action plans, in memory
        and in the state-db. Pruned on epoch transitions; future plans are never pruned. Must be at
        least 1.
      </>
    ),
  },
  'slot_artifact_retention_epochs': {
    label: 'Artifact retention',
    kind: 'number',
    unit: 'epochs',
    min: 1,
    hint: 'Epochs of raw SSZ payloads, signed bids and envelopes kept in the state-db. Raw payloads dominate disk usage.',
    help: (
      <>
        Epochs of raw SSZ artifacts (built payloads, signed bids, envelopes) kept in the state-db
        (<code>slot_artifact_retention_epochs</code>). Raw payloads dominate disk usage.
        Without <code>--state-db</code> only the newest 64 slots are kept in memory regardless. Must
        be at least 1.
      </>
    ),
  },
  'slot_artifact_capture_enabled': {
    label: 'Capture raw SSZ artifacts',
    short: 'Artifact capture',
    kind: 'bool',
    hint: 'Slot results are recorded either way; this only controls the raw objects.',
    help: (
      <>
        Whether raw SSZ artifacts are captured per slot
        (<code>slot_artifact_capture_enabled</code>). Off = no artifact downloads for new slots; slot
        result summaries are recorded either way.
      </>
    ),
  },

  // ----- ePBS bidder: bid config -----
  'epbs.bid_start_time': {
    label: 'First bid',
    short: 'Bid start',
    kind: 'number',
    unit: 'ms',
    hint: 'Opening of the bid window, relative to the slot start.',
    help: (
      <>
        Opening of the bid window in ms relative to the proposal slot&apos;s start
        (<code>epbs.bid_start_time</code>). Negative = before the slot starts. Startup default:
        -400 ms at 12 s slots, scaled to the slot time. Per-slot action plans can override the
        window.
      </>
    ),
  },
  'epbs.bid_end_time': {
    label: 'Last bid',
    short: 'Bid end',
    kind: 'number',
    unit: 'ms',
    hint: 'Close of the bid window (exclusive), relative to the slot start.',
    validate: (value, draft) =>
      Number(value) <= num(draft, 'epbs.bid_start_time')
        ? 'The bid window must close after it opens (last bid after first bid).'
        : null,
    help: (
      <>
        Close of the bid window in ms relative to the proposal slot&apos;s start
        (<code>epbs.bid_end_time</code>, exclusive). Bidding also stops as soon as the slot&apos;s
        block is seen. Startup default: -100 ms at 12 s slots, scaled to the slot time.
      </>
    ),
  },
  'epbs.bid_min_amount': {
    label: 'Minimum bid',
    short: 'Bid min',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    hint: 'Floor for the bid base: max(block value, minimum bid) + subsidy.',
    inactiveWhen: (draft) =>
      num(draft, 'epbs.bid_value_override') > 0 ? 'Ignored while a bid value override is set.' : null,
    help: (
      <>
        Floor for the bid base in gwei (<code>epbs.bid_min_amount</code>): the first bid is
        max(block value, bid min) + subsidy. Ignored while a bid value override is set.
      </>
    ),
  },
  'epbs.bid_increase': {
    label: 'Bid increase',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    hint: 'Added per re-bid of the same payload.',
    help: (
      <>
        Gwei added per re-bid of the same payload (<code>epbs.bid_increase</code>): the n-th bid is
        the base + (n-1) &times; increase. Also applies on top of a bid value override.
      </>
    ),
  },
  'epbs.bid_interval': {
    label: 'Bid interval',
    kind: 'number',
    unit: 'ms',
    min: 0,
    zeroLabel: 'single bid',
    hint: 'Minimum time between two bids on the same payload; 0 = bid each payload once.',
    help: (
      <>
        Minimum ms between two bids on the same payload inside the bid window
        (<code>epbs.bid_interval</code>). 0 = bid each payload only once. Each re-bid needs a key
        that has not bid this slot yet (see the key selection settings).
      </>
    ),
  },
  'epbs.bid_subsidy': {
    label: 'Bid subsidy',
    kind: 'number',
    unit: 'gwei',
    zeroLabel: 'none',
    hint: "Flat gwei added to every bid so it clears the proposer's local-EL threshold; negative = the margin kept from the block value. 0 = bid the real block value.",
    inactiveWhen: (draft) =>
      num(draft, 'epbs.bid_value_override') > 0 ? 'Ignored while a bid value override is set.' : null,
    help: (
      <>
        Flat gwei added to every bid (<code>epbs.bid_subsidy</code>) so it clears the
        proposer&apos;s local-EL threshold; otherwise the beacon node self-builds when its local
        block is worth more. 0 = bid the real block value. A negative value is the builder&apos;s
        margin: the payload pays its block value to buildoor&apos;s wallet and the bid pays the
        proposer the block value minus the margin, never below the minimum bid. Ignored while a
        bid value override is set.
      </>
    ),
  },
  'epbs.bid_value_override': {
    label: 'Bid value override',
    short: 'Value override',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    zeroLabel: 'off',
    hint: 'Absolute bid base replacing max(block value, minimum bid) + subsidy; allows underbidding. 0 = off.',
    help: (
      <>
        Absolute bid base in gwei (<code>epbs.bid_value_override</code>) replacing max(block value,
        bid min) + subsidy, so it can underbid the block value. Bid increase still applies per
        re-bid. 0 = off. A per-slot plan value wins over it.
      </>
    ),
  },
  'epbs.head_vote_threshold_pct': {
    label: 'Vote marker threshold',
    short: 'Vote marker',
    kind: 'number',
    unit: '%',
    min: 0,
    max: 100,
    zeroLabel: 'off',
    hint: 'Head-vote participation marked as reached in the slot graph. Display only.',
    help: (
      <>
        Head-vote participation in percent marked as reached in the slot graph
        (<code>epbs.head_vote_threshold_pct</code>). Default 60 = the Gloas builder payment quorum.
        Display only: the reveal vote gate uses its own threshold
        (<code>reveal.vote_threshold_pct</code>). 0 = off.
      </>
    ),
  },

  // ----- ePBS bidder: key selection -----
  'epbs.bid_candidate': {
    label: 'Bid candidate',
    kind: 'select',
    options: [
      { value: 'auto', label: 'Auto (match the chain view)', short: 'auto' },
      { value: 'all', label: 'All built candidates', short: 'all' },
      { value: 'parent_full', label: 'Parent (full payload)', short: 'parent (full)' },
      { value: 'parent_empty', label: 'Parent (empty payload)', short: 'parent (empty)' },
      { value: 'grandparent_full', label: 'Grandparent (reorg)', short: 'grandparent (reorg)' },
      { value: 'grandparent_empty', label: 'Grandparent (empty payload)', short: 'grandparent (empty)' },
    ],
    hint: 'Which built payload the bids commit to. All bids every candidate the slot built, spending keys per candidate.',
    help: (
      <>
        Which built candidate payload the slot&apos;s p2p bids commit to
        (<code>epbs.bid_candidate</code>). A specific candidate only bids when that candidate was
        actually built (see the build candidate settings). Per-slot action plans can override it.
        <dl>
          <dt>auto</dt>
          <dd>The payload matching the current head and its payload status (empty-parent build when the head&apos;s payload was withheld), else the slot&apos;s primary payload.</dd>
          <dt>all</dt>
          <dd>Bids every built candidate, spending keys per candidate: deliberate multi-parent gossip, since only one of those parents can end up canonical.</dd>
          <dt>parent (full)</dt>
          <dd>Head block and its payload.</dd>
          <dt>parent (empty payload)</dt>
          <dd>Head block on its execution parent (payload-miss case).</dd>
          <dt>grandparent (reorg)</dt>
          <dd>Head block&apos;s parent, reorging the head out.</dd>
          <dt>grandparent (empty payload)</dt>
          <dd>Reorg plus a withheld grandparent payload.</dd>
        </dl>
      </>
    ),
  },
  'epbs.bid_candidate_switch': {
    label: 'Allow switching candidate mid-slot',
    short: 'Candidate switch',
    kind: 'bool',
    boolLabels: { on: 'mid-slot allowed', off: 'sticky per slot' },
    hint: 'Lets the auto selection follow chain view changes during the slot.',
    inactiveWhen: (draft) =>
      str(draft, 'epbs.bid_candidate') === 'auto' ? null : 'Only affects the auto bid candidate selection.',
    help: (
      <>
        Only affects <em>auto</em> bid candidate selection (<code>epbs.bid_candidate_switch</code>).
        <dl>
          <dt>sticky per slot (off)</dt>
          <dd>Keeps bidding the first candidate chosen for the slot.</dd>
          <dt>mid-slot allowed (on)</dt>
          <dd>Re-selects on every bid and follows chain view changes; a switched bid rarely propagates past the gossip first-seen rule.</dd>
        </dl>
      </>
    ),
  },
  'epbs.key_strategy': {
    label: 'Key strategy',
    kind: 'select',
    options: KEY_STRATEGY_OPTIONS,
    hint: 'Order the active keys are offered in. Balance is a preference, not a filter.',
    help: (
      <>
        Order the active builder keys are offered in for each bid (<code>epbs.key_strategy</code>);
        keys that already bid this slot are skipped. Keys that cannot cover the bid sort last but
        are still used when nothing else is left. Also used by Builder API bids unless the Builder
        API sets its own strategy. Per-slot action plans can override it.
        <dl>
          <dt>round robin</dt>
          <dd>Rotates the starting key by slot, spreading bids and payments evenly.</dd>
          <dt>single</dt>
          <dd>Lowest-index active key that has not bid this slot yet. Combine with keys per slot = 1 for single-key behaviour.</dd>
          <dt>random</dt>
          <dd>Shuffles the keys per slot (deterministic per slot).</dd>
          <dt>least used</dt>
          <dd>Prefers the keys with the fewest bids so far.</dd>
        </dl>
      </>
    ),
  },
  'epbs.bid_keys_per_slot': {
    label: 'Keys per slot',
    short: 'Keys / slot',
    kind: 'number',
    min: 0,
    zeroLabel: 'whole fleet',
    hint: 'Caps how many distinct keys one slot may spend. 0 means every active key is available.',
    help: (
      <>
        Maximum distinct builder keys that may bid one slot (<code>epbs.bid_keys_per_slot</code>).
        Since a key bids at most once per slot, this also caps the slot&apos;s bid count. 0 = no cap
        beyond the active fleet; 1 = one gossiped bid per slot. Per-slot action plans can override
        it.
      </>
    ),
  },
  'epbs.bid_keys_per_step': {
    label: 'Keys per step',
    short: 'Keys / step',
    kind: 'number',
    min: 0,
    zeroLabel: 'all remaining',
    hint: 'Keys spent on a payload per bid interval, each bidding one increase above the last. 0 spends every remaining key at once.',
    help: (
      <>
        Keys that bid a payload per bid interval step, each one bid increase above the previous
        (<code>epbs.bid_keys_per_step</code>). 1 = walk the fleet up the price ladder one key per
        interval; 0 = spend every remaining key at once when the window opens. Per-slot action plans
        can override it.
      </>
    ),
  },

  // ----- Payload reveal -----
  'reveal.enabled': {
    label: 'Payload reveals',
    short: 'Reveals',
    kind: 'bool',
    boolLabels: { on: 'active', off: 'disabled' },
    hint: 'Off withholds every won payload, except slots whose plan forces the reveal.',
    help: (
      <>
        Payload reveals serve both the p2p bidder and the Builder API (<code>reveal.enabled</code>).
        Disabled withholds every won payload, except slots whose action plan sets the reveal to
        custom (those force their reveal).
      </>
    ),
  },
  'reveal.gate_mode': {
    label: 'Reveal gate',
    short: 'Gate',
    kind: 'select',
    options: [
      { value: 'time', label: 'Timed', short: 'timed' },
      { value: 'vote', label: 'Vote threshold', short: 'vote' },
      { value: 'vote_or_time', label: 'Vote threshold or time (whichever first)', short: 'vote / time' },
      { value: 'vote_and_time', label: 'Vote threshold and time (both)', short: 'vote + time' },
    ],
    hint: 'When to reveal a won payload: at a fixed time, at the vote threshold, whichever comes first, or once both are open.',
    help: (
      <>
        <code>reveal.gate_mode</code>: when a won payload&apos;s envelope is published.
        <dl>
          <dt>time</dt>
          <dd>at the reveal time</dd>
          <dt>vote</dt>
          <dd>as soon as head-vote participation on the committing block reaches the vote threshold</dd>
          <dt>vote_or_time</dt>
          <dd>whichever of the two opens first (default)</dd>
          <dt>vote_and_time</dt>
          <dd>only once both are open</dd>
        </dl>
        A vote gate still unsatisfied at slot end withholds the reveal (<code>vote_gate_timeout</code>).
        Per-slot action plans can override it.
      </>
    ),
  },
  'reveal.time_ms': {
    label: 'Reveal time',
    kind: 'number',
    unit: 'ms',
    hint: 'Time gate, in ms after the slot start.',
    inactiveWhen: (draft) =>
      str(draft, 'reveal.gate_mode') === 'vote' ? 'Unused in the vote threshold gate mode.' : null,
    help: (
      <>
        <code>reveal.time_ms</code>: the time gate, in ms after slot start. Unused in{' '}
        <code>vote</code> mode. Auto default (CLI value 0): 5000 ms at 12 s slots, scaled to the
        slot time (2500 ms at 6 s); the Gloas payload deadline is 50% of the slot. Per-slot action
        plans can override it.
      </>
    ),
  },
  'reveal.vote_threshold_pct': {
    label: 'Payment quorum threshold',
    short: 'Vote threshold',
    kind: 'number',
    unit: '%',
    min: 0,
    max: 100,
    hint: 'Head-vote participation on the committing block that opens the vote gate. Unsatisfied gates withhold at slot end.',
    inactiveWhen: (draft) =>
      str(draft, 'reveal.gate_mode') === 'time' ? 'Unused in the timed gate mode.' : null,
    help: (
      <>
        <code>reveal.vote_threshold_pct</code>: head-vote participation (% of the slot&apos;s
        attesting balance voting for the committing block, counted from single attestations) that
        opens the vote gate. Default 60 = the Gloas builder payment quorum. Unused in{' '}
        <code>time</code> mode; low subnet coverage undercounts votes and delays the gate.
      </>
    ),
  },
  'reveal.broadcast_validation': {
    label: 'Broadcast validation',
    kind: 'select',
    options: [
      { value: 'gossip', label: 'Gossip (lightweight checks)', short: 'gossip' },
      { value: 'consensus', label: 'Consensus (full checks)', short: 'consensus' },
      { value: 'consensus_and_equivocation', label: 'Consensus and equivocation', short: 'consensus + equivocation' },
    ],
    hint: 'Validation the beacon node applies before broadcasting the envelope.',
    help: (
      <>
        <code>reveal.broadcast_validation</code>: the validation level the beacon node applies to
        the envelope before broadcasting it.
        <dl>
          <dt>gossip</dt>
          <dd>gossip-level checks only</dd>
          <dt>consensus</dt>
          <dd>full consensus validation</dd>
          <dt>consensus + equivocation</dt>
          <dd>consensus validation plus an equivocating-block check, the builder&apos;s protection
            against unbundling (default)</dd>
        </dl>
      </>
    ),
  },
  'reveal.max_attempts': {
    label: 'Maximum publish attempts',
    short: 'Max attempts',
    kind: 'number',
    min: 1,
    hint: 'Total publish attempts per reveal.',
    help: (
      <>
        <code>reveal.max_attempts</code>: total publish attempts per reveal (at least 1) before the
        reveal gives up.
      </>
    ),
  },
  'reveal.retry_interval_ms': {
    label: 'Retry interval',
    kind: 'number',
    unit: 'ms',
    min: 0,
    hint: 'Wait between failed publish attempts.',
    help: (
      <>
        <code>reveal.retry_interval_ms</code>: wait in ms between failed publish attempts.
      </>
    ),
  },
  'reveal.rebind_on_reorg': {
    label: 'Rebind on reorg',
    kind: 'bool',
    hint: 'Rebuilds and re-signs the envelope when our payload is re-included under a different block root.',
    help: (
      <>
        <code>reveal.rebind_on_reorg</code>: when the block a reveal was scheduled for is reorged
        out and our payload is re-included under a different block root, rebuild and re-sign the
        envelope for the new root (payload unchanged). Off = only the original block is revealed
        for.
      </>
    ),
  },

  // ----- Builder API -----
  'builder_api.block_value_subsidy_gwei': {
    label: 'Block value subsidy',
    short: 'Subsidy',
    kind: 'number',
    unit: 'gwei',
    zeroLabel: 'none',
    hint: 'Added to the block value of every served bid; negative = the margin kept from it (Gloas only).',
    inactiveWhen: (draft) =>
      num(draft, 'builder_api.value_override_gwei') > 0 ? 'Ignored while a value override is set.' : null,
    help: (
      <>
        <code>builder_api.block_value_subsidy_gwei</code> (gwei), added on top of the block value of
        every served bid (pre-Gloas getHeader and Gloas getExecutionPayloadBid). 0 = bid the plain
        block value. A negative value is the builder&apos;s margin on Gloas bids: the payload pays its
        block value to buildoor&apos;s wallet and the bid pays the proposer the rest. Before Gloas the
        payload pays the proposer directly, so a negative value serves the plain block value.
        Per-slot action plans can override it.
      </>
    ),
  },
  'builder_api.value_override_gwei': {
    label: 'Value override',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    zeroLabel: 'off',
    hint: 'Absolute total value served instead of block value + subsidy. 0 = off.',
    help: (
      <>
        <code>builder_api.value_override_gwei</code> (gwei): when non-zero, the served bid&apos;s
        total value is exactly this amount, replacing block value + subsidy (may exceed or undercut
        the real block value). 0 = off. Per-slot action plans can override it.
      </>
    ),
  },
  'builder_api.execution_payment_gwei': {
    label: 'Execution payment',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    zeroLabel: 'off',
    hint: "Portion of a served Gloas bid claimed as execution_payment: an unbacked claim, capped by the proposer's max_execution_payment. Wins over the percentage. 0 = off.",
    help: execPaymentHelp(),
  },
  'builder_api.execution_payment_percent': {
    label: 'Execution payment share',
    short: 'Execution payment',
    kind: 'number',
    unit: '%',
    min: 0,
    max: 100,
    zeroLabel: 'off',
    hint: 'Same claim as a percentage of the total value; used only while the absolute amount is 0.',
    inactiveWhen: (draft) =>
      num(draft, 'builder_api.execution_payment_gwei') > 0
        ? 'Ignored while an absolute execution payment is set.'
        : null,
    help: execPaymentHelp(),
  },
  'builder_api.ignore_preference_limit': {
    label: "Ignore the proposer's maximum execution payment",
    short: 'Preference limit',
    kind: 'bool',
    boolLabels: { on: 'ignored', off: 'respected' },
    hint: 'Serves the execution payment beyond the advertised limit: a spec-violating bid clients should reject.',
    inactiveWhen: (draft) =>
      num(draft, 'builder_api.execution_payment_gwei') > 0 ||
      num(draft, 'builder_api.execution_payment_percent') > 0
        ? null
        : 'Only matters while an execution payment is configured.',
    help: (
      <>
        <code>builder_api.ignore_preference_limit</code>. Spec-violating testing knob.
        <dl>
          <dt>respected</dt>
          <dd>the execution payment is capped at the proposer&apos;s <code>max_execution_payment</code></dd>
          <dt>ignored</dt>
          <dd>the configured execution payment is served beyond that limit; clients should reject such bids</dd>
        </dl>
      </>
    ),
  },
  'builder_api.serve_candidates': {
    label: 'Serve candidates',
    kind: 'text',
    placeholder: 'all',
    hint: 'all, canonical_only, or a comma-separated list of parent_full, parent_empty, grandparent_full, grandparent_empty.',
    validate: (value) => {
      const policy = String(value).trim();
      if (policy === 'all' || policy === 'canonical_only') return null;
      const keys = ['parent_full', 'parent_empty', 'grandparent_full', 'grandparent_empty'];
      const bad = policy.split(',').map((s) => s.trim()).find((s) => !keys.includes(s));
      return bad === undefined
        ? null
        : 'Must be all, canonical_only or a comma-separated list of candidate keys.';
    },
    help: (
      <>
        <code>builder_api.serve_candidates</code> (Gloas): which built candidate payloads a bid
        request may be answered from; the candidate matching the requested parent is served.
        <dl>
          <dt>all</dt>
          <dd>any candidate (default)</dd>
          <dt>canonical_only</dt>
          <dd>only <code>parent_full</code> and unclassified payloads</dd>
          <dt>comma list</dt>
          <dd>only the named keys: <code>parent_full</code>, <code>parent_empty</code>,{' '}
            <code>grandparent_full</code>, <code>grandparent_empty</code></dd>
        </dl>
        Per-slot action plans can override it.
      </>
    ),
  },
  'builder_api.on_demand_build': {
    label: 'Build on demand',
    short: 'On-demand build',
    kind: 'bool',
    hint: 'Builds a payload on the fly when a bid request asks for a legal parent no candidate covers yet.',
    help: (
      <>
        <code>builder_api.on_demand_build</code> (Gloas): when on, a bid request for a legal parent
        that no built candidate covers triggers a build on the fly, bounded by the request&apos;s
        response budget. Off = such requests get no bid.
      </>
    ),
  },
  'builder_api.key_strategy': {
    label: 'Key strategy',
    kind: 'select',
    options: [
      { value: '', label: 'Follow the ePBS bidder', short: 'follow ePBS' },
      { value: 'round_robin', label: 'Round robin', short: 'round robin' },
      { value: 'single', label: 'Single (primary key only)', short: 'single' },
      { value: 'random', label: 'Random', short: 'random' },
      { value: 'least_used', label: 'Least used', short: 'least used' },
    ],
    hint: 'Which managed builder key signs served bids.',
    help: (
      <>
        <code>builder_api.key_strategy</code> (Gloas): which managed builder key signs served bids.
        The choice is sticky per slot and parent, so a polling proposer keeps seeing the same
        builder.
        <dl>
          <dt>follow ePBS bidder</dt>
          <dd>empty value, use <code>epbs.key_strategy</code></dd>
          <dt>round robin</dt>
          <dd>rotate through the active keys by slot</dd>
          <dt>single</dt>
          <dd>always the lowest-index active key</dd>
          <dt>random</dt>
          <dd>per-slot shuffle (reproducible within the slot)</dd>
          <dt>least used</dt>
          <dd>prefer the keys that have bid least</dd>
        </dl>
        Keys that cannot cover the bid sort last but are still used when nothing else is left.
      </>
    ),
  },
  'builder_api.require_request_auth': {
    label: 'Require request auth',
    short: 'Request auth',
    kind: 'bool',
    boolLabels: { on: 'required', off: 'optional' },
    hint: 'Rejects bid requests without a SignedRequestAuthV1 body (401). Supplied auth is always verified either way.',
    help: (
      <>
        <code>builder_api.require_request_auth</code> (Gloas getExecutionPayloadBid).
        <dl>
          <dt>optional</dt>
          <dd>requests without a <code>SignedRequestAuthV1</code> body are served</dd>
          <dt>required</dt>
          <dd>requests without one are rejected with 401</dd>
        </dl>
        A supplied auth body is always verified either way.
      </>
    ),
  },

  // ----- Testing builder (testing_buildBlockV1) -----
  'local_build.enabled': {
    label: 'Testing builder (testing_buildBlockV1)',
    short: 'Testing builder',
    kind: 'bool',
    hint: 'Cannot be enabled while the EL does not expose the testing namespace.',
    help: (
      <>
        Builds the custom block every slot, alongside the engine build
        (<code>local_build.enabled</code>): buildoor hands the EL a transaction list through{' '}
        <code>testing_buildBlockV1</code> and the EL executes exactly that list. Whether bids use it
        is decided by <strong>Bid with</strong>. The method lives in an opt-in RPC namespace on the
        EL&apos;s JSON-RPC port (<code>--el-rpc</code>), not on the engine port; buildoor probes it
        at startup and every 5 minutes and refuses to enable the testing builder while it is
        missing. Per-slot overrides live in the action plan&apos;s build category.
      </>
    ),
  },
  'local_build.payload_source': {
    label: 'Bid with',
    kind: 'select',
    options: toOptions(PAYLOAD_SOURCES),
    hint: 'Which of the two blocks feeds bids, Builder API headers and the reveal.',
    help: (
      <>
        Which of the two blocks feeds bids, Builder API headers and the reveal
        (<code>local_build.payload_source</code>).
        {optionList(PAYLOAD_SOURCES)}
      </>
    ),
  },
  'local_build.tx_source': {
    label: 'Custom block transactions',
    short: 'Custom block txs',
    kind: 'select',
    options: toOptions(TX_SOURCES),
    hint: "Where the custom block's transactions come from.",
    help: (
      <>
        Where the custom block&apos;s transactions come from (<code>local_build.tx_source</code>).
        The engine block always uses the EL mempool. Per-slot action plans can also hand an exact
        list (<em>explicit</em>, <em>queued</em>).
        {optionList(TX_SOURCES)}
      </>
    ),
  },
  'local_build.build_el_payload': {
    label: 'Keep running the engine build when bidding with the custom block',
    short: 'Keep engine build',
    kind: 'bool',
    boolLabels: { on: 'yes', off: 'no' },
    hint: 'Off saves the EL the second build.',
    inactiveWhen: (draft) =>
      str(draft, 'local_build.payload_source') === 'local'
        ? null
        : 'Only matters when bidding with the custom block alone; the other choices always build the engine block.',
    help: (
      <>
        Only matters for <em>Bid with: Custom block</em> (<code>local_build.build_el_payload</code>).
        On, the engine block is still built for comparison. The other choices always build it. Off
        saves the EL the second build.
      </>
    ),
  },
  'local_build.allow_blobs_without_bundle': {
    label: 'Include blob transactions on ELs without a blobs bundle',
    short: 'Blobs without bundle',
    kind: 'bool',
    boolLabels: { on: 'allowed', off: 'skipped' },
    hint: 'The blobs of such a block cannot be revealed.',
    help: (
      <>
        Some ELs (reth, ethrex) return no blobs bundle from <code>testing_buildBlockV1</code>, so
        the blob sidecars of a custom block are unknown
        (<code>local_build.allow_blobs_without_bundle</code>). Off skips blob transactions on those
        ELs; on includes them anyway, for testing a reveal without blob data.
      </>
    ),
  },
  'local_build.max_attempts': {
    label: 'Maximum build attempts',
    short: 'Max attempts',
    kind: 'number',
    min: 1,
    hint: 'testing_buildBlockV1 calls per mempool build: a refused list drops the offending transactions and retries. Exact lists never retry.',
    inactiveWhen: (draft) => {
      const source = str(draft, 'local_build.tx_source');
      return source === 'txpool' || source === 'txpool_el_fill'
        ? null
        : 'Only used for the buildoor mempool transaction sources.';
    },
    help: (
      <>
        For the buildoor mempool sources (<code>local_build.max_attempts</code>): when the EL
        refuses the list, the offending transactions are dropped (and get a strike in the pool) and
        the build retries, up to this many calls. Exact lists from action plans never retry.
      </>
    ),
  },
  'local_build.start_delay_ms': {
    label: 'Start delay',
    kind: 'number',
    unit: 'ms',
    min: 0,
    max: 12000,
    zeroLabel: 'at once',
    hint: 'Wait after the engine build started before calling testing_buildBlockV1.',
    validate: (value, draft) => {
      const buildTime = num(draft, 'payload_build_time');
      return buildTime > 0 && Number(value) >= buildTime
        ? `Must be below the build time (${buildTime} ms): the testing block would miss its bids.`
        : null;
    },
    help: (
      <>
        Delay of the testing build after the engine build started
        (<code>local_build.start_delay_ms</code>, 0 = at once). testing_buildBlockV1 builds from
        the transactions known at the moment of the call, while the engine build keeps adding
        arrivals until its payload is fetched (<code>payload_build_time</code> later), so an early
        testing block misses the late ones and is often worth less. A delay of about half the build
        time catches most of them. Leave the call time to finish before the engine payload is due:
        the slot&apos;s bids wait for both builds.
      </>
    ),
  },

  // ----- buildoor mempool -----
  'txpool.enabled': {
    label: 'buildoor mempool (ingress at /rpc)',
    short: 'buildoor mempool',
    kind: 'bool',
    boolLabels: { on: 'accepting', off: 'off' },
    hint: 'Requires --el-rpc. While off, /rpc refuses every call.',
    help: (
      <>
        buildoor&apos;s own mempool, separate from the EL&apos;s (<code>txpool.enabled</code>):
        transactions sent to <code>/rpc</code> stay private and only land through custom blocks
        buildoor wins. On, <code>/rpc</code> admits transactions and serves the read passthrough;
        off, every <code>/rpc</code> call is refused and the testing builder skips the buildoor
        mempool sources. Queued transactions are kept while it is off.
      </>
    ),
  },
  'txpool.ordering': {
    label: 'Ordering',
    kind: 'select',
    options: toOptions(ORDERINGS),
    hint: 'The order in which the testing builder takes transactions from this mempool.',
    help: (
      <>
        The order in which the testing builder takes transactions from this mempool
        (<code>txpool.ordering</code>).
        {optionList(ORDERINGS.map((o) => ({ label: o.short, hint: o.hint })))}
      </>
    ),
  },
  'txpool.max_txs_per_block': {
    label: 'Maximum transactions per block',
    short: 'Max txs per block',
    kind: 'number',
    min: 0,
    zeroLabel: 'unlimited',
    hint: '0 = unlimited.',
    help: (
      <>
        <code>txpool.max_txs_per_block</code>: caps how many pool transactions one custom block
        takes. 0 = unlimited (bounded only by gas and blob limits).
      </>
    ),
  },
  'txpool.gas_fill_pct': {
    label: 'Gas fill',
    kind: 'number',
    unit: '%',
    min: 1,
    max: 100,
    hint: 'Share of the block gas limit the selection fills.',
    help: (
      <>
        <code>txpool.gas_fill_pct</code>: share of the block gas limit (1-100%) the pool selection
        fills. 100 = fill the whole block.
      </>
    ),
  },
  'txpool.max_pool_txs': {
    label: 'Maximum pool transactions',
    short: 'Max pool txs',
    kind: 'number',
    min: 1,
    hint: 'Total queued transactions.',
    help: (
      <>
        <code>txpool.max_pool_txs</code>: caps the total number of queued transactions; admission
        refuses new ones once it is reached.
      </>
    ),
  },
  'txpool.max_txs_per_sender': {
    label: 'Maximum transactions per sender',
    short: 'Max txs per sender',
    kind: 'number',
    min: 1,
    hint: 'Queued transactions of one sender.',
    help: (
      <>
        <code>txpool.max_txs_per_sender</code>: caps the queued transactions of a single sender.
      </>
    ),
  },
  'txpool.tx_ttl_slots': {
    label: 'Transaction lifetime',
    short: 'TTL',
    kind: 'number',
    unit: 'slots',
    min: 0,
    zeroLabel: 'never',
    hint: '0 = never; drops a stalled sender from its oldest expired nonce up.',
    help: (
      <>
        <code>txpool.tx_ttl_slots</code>: queued transactions older than this many slots expire
        (0 = never). Pool transactions only land when buildoor wins a slot, so an expired
        transaction marks a stalled sender chain: it is dropped together with the sender&apos;s
        higher nonces, which could never execute behind the gap, while fresh lower nonces stay. A
        generator tracking nonces locally recovers through the pool-aware <code>pending</code>{' '}
        nonce.
      </>
    ),
  },
  'txpool.max_strikes': {
    label: 'Maximum strikes',
    short: 'Max strikes',
    kind: 'number',
    min: 0,
    zeroLabel: 'never drop',
    hint: 'Attributed EL refusals before a transaction is dropped (0 = never).',
    help: (
      <>
        <code>txpool.max_strikes</code>: a queued transaction is dropped after this many build
        failures attributed to it (the EL refused the list because of it). 0 = never drop.
      </>
    ),
  },
  'txpool.base_fee_ceiling_gwei': {
    label: 'Base fee ceiling',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    zeroLabel: 'off',
    hint: 'Above it the fill drops to the gas target (0 = off).',
    help: (
      <>
        <code>txpool.base_fee_ceiling_gwei</code>: once the next base fee exceeds it, the fill is
        halved to the EIP-1559 gas target. Every full block raises the base fee 12.5 percent, and a
        long max-fill run would otherwise price its own queued transactions out. A fill knob only:
        exact lists (explicit, queued) always get the whole block. 0 = off.
      </>
    ),
  },
  'txpool.forward_to_el': {
    label: 'Forward admitted transactions to the EL mempool',
    short: 'Forward to EL',
    kind: 'bool',
    boolLabels: { on: 'yes', off: 'no' },
    hint: 'Shadow mode: the transactions can then land in blocks buildoor did not build.',
    help: (
      <>
        <code>txpool.forward_to_el</code>: also submits every admitted transaction to the EL&apos;s
        mempool. They stop being private and can land in any block, including other
        builders&apos;. A shadow mode for comparisons that defeats the pool&apos;s purpose.
      </>
    ),
  },

  // ----- RPC access -----
  'txpool.auth': {
    label: 'Access',
    kind: 'select',
    options: toOptions(AUTH_MODES),
    hint: 'Who may call /rpc. Authenticated callers are always accepted.',
    help: (
      <>
        Who may call <code>/rpc</code> (<code>txpool.auth</code>). Authenticated callers are always
        accepted.
        {optionList(AUTH_MODES.map((m) => ({ label: m.value, hint: m.hint })))}
      </>
    ),
  },
  'txpool.rpc_anon_rate': {
    label: 'Anonymous rate',
    kind: 'number',
    unit: 'calls/s',
    min: 0,
    zeroLabel: 'unlimited',
    hint: 'Anonymous /rpc calls per second. 0 = unlimited.',
    inactiveWhen: (draft) =>
      str(draft, 'txpool.auth') === 'api_key' ? 'Anonymous callers are refused while an API key is required.' : null,
    help: (
      <>
        <code>txpool.rpc_anon_rate</code>: anonymous <code>/rpc</code> calls per second (per client
        IP or shared, see the anonymous scope). 0 = unlimited.
      </>
    ),
  },
  'txpool.rpc_anon_scope': {
    label: 'Anonymous scope',
    kind: 'select',
    options: toOptions(ANON_SCOPES),
    hint: 'Whether the anonymous limit applies per client IP or to all anonymous callers together.',
    inactiveWhen: (draft) =>
      str(draft, 'txpool.auth') === 'api_key' ? 'Anonymous callers are refused while an API key is required.' : null,
    help: (
      <>
        <code>txpool.rpc_anon_scope</code>: how anonymous callers are limited and counted.
        {optionList(ANON_SCOPES.map((m) => ({ label: m.value, hint: m.hint })))}
      </>
    ),
  },
  'txpool.rpc_key_rate': {
    label: 'API key rate',
    short: 'Key rate',
    kind: 'number',
    unit: 'calls/s',
    min: 0,
    zeroLabel: 'unlimited',
    hint: 'Default calls per second per API key; a key can override it. 0 = unlimited.',
    help: (
      <>
        <code>txpool.rpc_key_rate</code>: default <code>/rpc</code> calls per second per API key; a
        key can override it. Also applies to the static token. WebUI logins are never limited. 0 =
        unlimited.
      </>
    ),
  },
  'txpool.rpc_proxy_hops': {
    label: 'Trusted proxy hops',
    short: 'Proxy hops',
    kind: 'number',
    min: 0,
    max: 16,
    zeroLabel: 'none',
    hint: 'Behind one nginx = 1. Too high lets callers spoof their IP.',
    help: (
      <>
        <code>txpool.rpc_proxy_hops</code>: trusted reverse proxies in front of <code>/rpc</code>.
        The client IP is the X-Forwarded-For entry that many hops from the right; 0 = the connection
        address, headers ignored. Behind one nginx = 1. Too high lets callers spoof their IP. At
        most 16.
      </>
    ),
  },
  'txpool.rpc_upstreams': {
    label: 'Read upstreams',
    kind: 'upstreams',
    placeholder: 'local@0',
    hint: 'Endpoints the read passthrough is sent to; lowest priority first.',
    validate: (value) => validateUpstreams(String(value)),
    help: (
      <>
        The endpoints the read passthrough (every call except{' '}
        <code>eth_sendRawTransaction</code>) is sent to (<code>txpool.rpc_upstreams</code>).{' '}
        <strong>local</strong> is the EL behind <code>--el-rpc</code>; other entries are
        http(s)/ws(s) URLs. The lowest priority is tried first, endpoints of equal priority take
        turns (round robin), and a transport failure moves on to the next endpoint. JSON-RPC errors
        are answers, not failures.
      </>
    ),
  },

  // ----- Builder keys & lifecycle -----
  // The config value is the configured target; the registry clamps it to
  // [1, max_index + 1] (BuilderKeysConfig.EffectiveTargetCount). The clamped
  // value is served as settings.target_count by GET /api/buildoor/builder-keys
  // and as key_target in the builder_info SSE event.
  'builder_keys.target_count': {
    label: 'Target keys',
    kind: 'number',
    min: 1,
    writer: 'builder-keys-target',
    hint: 'Keys kept registered and funded; 1 = single-key setup.',
    validate: (value, draft) => {
      const maxIndex = num(draft, 'builder_keys.max_index');
      return maxIndex > 0 && Number(value) > maxIndex + 1
        ? `At most ${maxIndex + 1} keys (maximum key index + 1).`
        : null;
    },
    help: (
      <>
        Number of builder keys kept registered and funded (<code>builder_keys.target_count</code>),
        derived from the entry key (index 0 is the entry key itself; 1 = single-key setup). Raising
        it deposits new keys; lowering it exits surplus keys, highest index first, when auto exit is
        on. An exited builder key can never be reactivated. Capped by the derivation limit
        (<code>builder_keys.max_index</code> + 1). Deposits and exits only run with lifecycle
        management.
      </>
    ),
  },
  'builder_keys.max_index': {
    label: 'Maximum key index',
    short: 'Max key index',
    kind: 'number',
    min: 0,
    hint: 'Caps how far key derivation may reach, bounding the target.',
    help: (
      <>
        <code>builder_keys.max_index</code>: highest derivation index buildoor may use (index 0 =
        the entry key). Caps the target at max index + 1 keys and bounds the startup scan for
        previously used keys.
      </>
    ),
  },
  'builder_keys.auto_deposit': {
    label: 'Auto-deposit keys below the target',
    short: 'Auto-deposit',
    kind: 'bool',
    hint: 'Deposits new keys until the managed count reaches the target.',
    help: (
      <>
        <code>builder_keys.auto_deposit</code>: when on, the lifecycle manager deposits new keys
        (one transaction per reconcile pass, from the funding wallet) until the managed count
        reaches the target. Off = no new keys are deposited automatically; the Deposit button still
        works.
      </>
    ),
  },
  'builder_keys.auto_exit': {
    label: 'Auto-exit keys above the target',
    short: 'Auto-exit',
    kind: 'bool',
    hint: 'Irreversible: an exited builder key can never be reactivated.',
    help: (
      <>
        <code>builder_keys.auto_exit</code>: when on, managed keys above the target are exited,
        highest index first, skipping keys with pending payments. Irreversible: an exited builder
        key can never be reactivated. Off = surplus keys stay registered.
      </>
    ),
  },
  'deposit_amount': {
    label: 'Deposit amount',
    kind: 'number',
    unit: 'ETH',
    scale: 1e9,
    integer: false,
    min: 0,
    hint: 'Sent with every builder key deposit: early onboarding, registration and each top-up.',
    validate: (value) => (Number(value) > 0 ? null : 'The deposit amount must be greater than 0.'),
    help: (
      <>
        The one amount sent with every builder key deposit (<code>deposit_amount</code>): early
        pre-Gloas onboarding, the registering builder deposit and each top-up. Must be greater than
        0.
      </>
    ),
  },
  'topup_threshold': {
    label: 'Top-up threshold',
    kind: 'number',
    unit: 'ETH',
    scale: 1e9,
    integer: false,
    min: 0,
    hint: 'Keys whose effective balance drops below this are topped up by the deposit amount.',
    help: (
      <>
        A registered key whose effective balance (balance minus pending payments) drops below this
        is topped up by the deposit amount (<code>topup_threshold</code>). Exiting or exited keys
        are never topped up.
      </>
    ),
  },
  'deposit_max_fee': {
    label: 'Maximum deposit fee',
    short: 'Max deposit fee',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    zeroLabel: 'no limit',
    hint: 'Deposits and top-ups wait while the deposit queue fee is above this (0 = no limit).',
    help: (
      <>
        Upper bound in gwei for the builder deposit contract&apos;s queue fee
        (<code>deposit_max_fee</code>). Deposits and top-ups wait while the current fee is above
        it, which also slows a fleet ramp-up when the queue grows. 0 = no limit.
      </>
    ),
  },
  'early_deposit_tip': {
    label: 'Early deposit priority fee',
    short: 'Early deposit tip',
    kind: 'number',
    unit: 'gwei',
    min: 0,
    max: 10000,
    zeroLabel: 'EL suggestion',
    hint: 'Minimum priority fee of the early onboarding deposits sent before the Gloas fork (0 = EL suggestion).',
    help: (
      <>
        Minimum priority fee in gwei of the early onboarding deposit transactions
        (<code>early_deposit_tip</code>), sent through the regular deposit contract before the
        Gloas fork. The EL&apos;s suggested tip is used when it is higher. At most 10000 gwei.
      </>
    ),
  },
};

// execPaymentHelp is shared by the absolute and the percentage execution
// payment: they are two ways to express the same claim.
function execPaymentHelp(): React.ReactNode {
  return (
    <>
      Gloas only: part of the served total value claimed as <code>execution_payment</code>, the
      rest is paid trustlessly via the bid <code>value</code>. The claim is deliberately unbacked
      (buildoor never makes the EL payment) and capped by the proposer&apos;s advertised{' '}
      <code>max_execution_payment</code> (0 when never submitted) unless the preference limit is
      ignored.
      <dl>
        <dt>Gwei (<code>builder_api.execution_payment_gwei</code>)</dt>
        <dd>absolute amount, clamped to the total value; wins over the percentage</dd>
        <dt>% (<code>builder_api.execution_payment_percent</code>)</dt>
        <dd>0-100 % of the total value, used only while the absolute amount is 0</dd>
      </dl>
      Both 0 = off (everything paid via <code>value</code>).
    </>
  );
}

// settingPath returns where a setting's current value lives: a dotted path
// into the SSE config object, or a `service_status.` path for the module
// enable flags.
export function settingPath(key: string): string {
  return SETTING_FIELDS[key]?.path ?? key;
}
