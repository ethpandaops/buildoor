import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import { InfoHint } from './InfoHint';
import type { BuildConfig, Config } from '../types';

interface CandidatePolicySectionProps {
  config: Config | null;
}

// The build-parent candidates, in canonical order. A slot may build one
// payload per candidate so bids and Builder API requests can be answered on
// whichever parent the proposer ends up on.
const CANDIDATES: Array<{
  key: keyof BuildConfig;
  label: string;
  hint: string;
  // What "auto" reacts to for this candidate.
  auto: string;
  defaultMode: string;
}> = [
  {
    key: 'candidate_parent_full',
    label: 'Parent (full)',
    hint: 'The normal build: head block and its revealed payload.',
    auto: 'builds unless the head block’s payload is known to be withheld.',
    defaultMode: 'always',
  },
  {
    key: 'candidate_parent_empty',
    label: 'Parent (empty payload)',
    hint: 'Head block, but on the payload it built upon — the Gloas payload-miss case.',
    auto: 'builds until the head block’s payload is confirmed revealed.',
    defaultMode: 'auto',
  },
  {
    key: 'candidate_grandparent_full',
    label: 'Grandparent (reorg)',
    hint: 'Head block’s parent: the proposer reorgs the head block out.',
    auto: 'builds while the head block’s vote participation is below the weak head threshold.',
    defaultMode: 'auto',
  },
  {
    key: 'candidate_grandparent_empty',
    label: 'Grandparent (empty payload)',
    hint: 'Reorg combined with a withheld grandparent payload (rare).',
    auto: 'builds while the head is weak and the grandparent’s payload is not confirmed revealed.',
    defaultMode: 'never',
  },
];

// candidateHint explains one candidate's setting and each of its modes.
const candidateHint = (candidate: (typeof CANDIDATES)[number]) => (
  <>
    {candidate.hint} Setting <code>build.{candidate.key}</code>, default{' '}
    <strong>{candidate.defaultMode}</strong>; per-slot action plans can override it.
    <dl>
      <dt>auto</dt>
      <dd>Follows chain signals: {candidate.auto}</dd>
      <dt>always</dt>
      <dd>Builds every scheduled slot.</dd>
      <dt>never</dt>
      <dd>Never builds this candidate.</dd>
    </dl>
  </>
);

const HINT_WEAK_HEAD = (
  <>
    Head-vote participation in percent below which the head block counts as contested
    (<code>build.auto_weak_head_pct</code>), arming the grandparent (reorg) candidates set to auto.
    0 = off: auto reorg candidates never build.
  </>
);

const HINT_PARALLEL = (
  <>
    How the slot&apos;s candidate builds run against the EL (<code>build.parallel</code>).
    <dl>
      <dt>parallel</dt>
      <dd>All selected candidates build concurrently, each with its own payload ID and the full payload build delay.</dd>
      <dt>sequential</dt>
      <dd>One after another, canonical (parent full) first so it keeps its scheduled start; speculative ones follow.</dd>
    </dl>
  </>
);

const HINT_SPECULATIVE_TIME = (
  <>
    EL build time in ms for the speculative candidates (everything but parent full) when builds
    run sequentially (<code>build.speculative_build_time_ms</code>). Ignored for parallel builds.
    0 = same as the payload build delay.
  </>
);

const HINT_ENFORCE_GAS_LIMIT = (
  <>
    Gloas only (<code>build.enforce_bid_gas_limit</code>): when the EL ignored the proposer&apos;s
    target gas limit, rewrites the built payload&apos;s gas limit to the exact value the bid gossip
    rules require (EL parent gas limit stepped toward the target) and recomputes the block hash.
    Skipped when the payload&apos;s gas used would not fit.
  </>
);

const MODE_LABELS: Record<string, string> = {
  auto: 'auto (chain signals)',
  always: 'always',
  never: 'never',
};

const MODE_SHORT: Record<string, string> = {
  auto: 'auto',
  always: 'always',
  never: 'never',
};

const DEFAULT_FORM: BuildConfig = {
  candidate_parent_full: 'always',
  candidate_parent_empty: 'auto',
  candidate_grandparent_full: 'auto',
  candidate_grandparent_empty: 'never',
  parallel: false,
  speculative_build_time_ms: 0,
  auto_weak_head_pct: 40,
  enforce_bid_gas_limit: false,
};

// CandidatePolicySection is the global build-candidate policy, rendered as a
// section of the Payload Builder card: which parent candidates a slot builds
// payloads for, how they are sequenced, and the signals auto mode reacts to.
// Per-slot overrides live in the action plan; edits here go through the
// generic path-based settings endpoint with build.* keys.
export const CandidatePolicySection: React.FC<CandidatePolicySectionProps> = ({ config }) => {
  const { isLoggedIn, getAuthHeader } = useAuthContext();
  const [editing, setEditing] = useState(false);

  const build = config?.build;

  const [form, setForm] = useState<BuildConfig>(DEFAULT_FORM);

  useEffect(() => {
    if (!editing && build) {
      setForm({ ...DEFAULT_FORM, ...build });
    }
  }, [build, editing]);

  const postSettings = async (settings: Record<string, unknown>): Promise<boolean> => {
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const authToken = await getAuthHeader();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
    }
    try {
      const response = await fetch('/api/config/settings', {
        method: 'POST',
        headers,
        body: JSON.stringify(settings),
      });
      const result = await response.json();
      if (result.error) {
        alert('Failed to update: ' + result.error);
        return false;
      }
      return true;
    } catch (err) {
      alert('Error: ' + err);
      return false;
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const ok = await postSettings({
      'build.candidate_parent_full': form.candidate_parent_full,
      'build.candidate_parent_empty': form.candidate_parent_empty,
      'build.candidate_grandparent_full': form.candidate_grandparent_full,
      'build.candidate_grandparent_empty': form.candidate_grandparent_empty,
      'build.parallel': form.parallel,
      'build.speculative_build_time_ms': form.speculative_build_time_ms,
      'build.auto_weak_head_pct': form.auto_weak_head_pct,
      'build.enforce_bid_gas_limit': form.enforce_bid_gas_limit,
    });
    if (ok) setEditing(false);
  };

  // How many candidates can produce a payload at all (never = off).
  const activeCount = CANDIDATES.filter(
    (c) => (build?.[c.key] as string | undefined) !== 'never'
  ).length;

  return (
    <>
      <div className="d-flex justify-content-between align-items-center mb-2">
        <div className="section-header">
          Build Candidates <span className="badge bg-secondary ms-1">{activeCount} enabled</span>
        </div>
        {isLoggedIn && !editing && (
          <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>
            <i className="fas fa-pencil-alt"></i>
          </button>
        )}
      </div>
      <div className="form-text mt-0 mb-2">
            A slot can build one payload per parent candidate, so a bid or
            Builder API request can be answered on whichever parent the
            proposer ends up on after a reorg or a missed payload.
            <strong> auto</strong> builds a candidate only when the chain
            suggests it is needed (parent payload withheld, or a weakly
            attested head), so a healthy chain normally produces just the
            parent (full) payload.
      </div>

      {!editing ? (
            <div className="row g-2">
              {CANDIDATES.map((candidate, i) => (
                <div className="col-6" key={candidate.key}>
                  <div className="config-item">
                    <div className="config-item-label">
                      {candidate.label}{' '}
                      <InfoHint alignEnd={i % 2 === 1}>{candidateHint(candidate)}</InfoHint>
                    </div>
                    <div className="config-item-value">
                      {MODE_SHORT[(build?.[candidate.key] as string) ?? ''] ??
                        (build?.[candidate.key] as string) ?? '—'}
                    </div>
                  </div>
                </div>
              ))}
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Weak Head Below <InfoHint>{HINT_WEAK_HEAD}</InfoHint></div>
                  <div className="config-item-value">
                    {build?.auto_weak_head_pct ? `${build.auto_weak_head_pct}%` : 'off'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Engine Builds <InfoHint alignEnd>{HINT_PARALLEL}</InfoHint></div>
                  <div className="config-item-value">
                    {build?.parallel ? 'parallel' : 'sequential'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Speculative Time <InfoHint>{HINT_SPECULATIVE_TIME}</InfoHint></div>
                  <div className="config-item-value">
                    {build?.speculative_build_time_ms
                      ? `${build.speculative_build_time_ms} ms`
                      : 'same as build time'}
                  </div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Enforce Bid Gas Limit <InfoHint alignEnd>{HINT_ENFORCE_GAS_LIMIT}</InfoHint></div>
                  <div className="config-item-value">
                    {build?.enforce_bid_gas_limit ? 'on' : 'off'}
                  </div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave}>
              {CANDIDATES.map((candidate) => (
                <div className="mb-2" key={candidate.key}>
                  <label className="form-label">
                    {candidate.label} <InfoHint>{candidateHint(candidate)}</InfoHint>
                  </label>
                  <select
                    className="form-select form-select-sm"
                    value={(form[candidate.key] as string) ?? 'auto'}
                    onChange={(e) => setForm({ ...form, [candidate.key]: e.target.value })}
                  >
                    {Object.entries(MODE_LABELS).map(([value, label]) => (
                      <option key={value} value={value}>{label}</option>
                    ))}
                  </select>
                  <div className="form-text">{candidate.hint}</div>
                </div>
              ))}

              <div className="mb-2">
                <label className="form-label">Weak Head Threshold (%) <InfoHint>{HINT_WEAK_HEAD}</InfoHint></label>
                <input
                  type="number"
                  min={0}
                  max={100}
                  className="form-control form-control-sm"
                  value={form.auto_weak_head_pct ?? 0}
                  onChange={(e) =>
                    setForm({ ...form, auto_weak_head_pct: parseInt(e.target.value) || 0 })
                  }
                  required
                />
                <div className="form-text">
                  Head-vote participation below which the head counts as
                  contested, arming the auto-mode reorg candidates. 0 disables
                  the signal.
                </div>
              </div>

              <div className="mb-2">
                <label className="form-label">Speculative Build Time (ms) <InfoHint>{HINT_SPECULATIVE_TIME}</InfoHint></label>
                <input
                  type="number"
                  min={0}
                  className="form-control form-control-sm"
                  value={form.speculative_build_time_ms ?? 0}
                  onChange={(e) =>
                    setForm({ ...form, speculative_build_time_ms: parseInt(e.target.value) || 0 })
                  }
                  required
                />
                <div className="form-text">
                  Only used when parallel building is off: shortens the
                  speculative candidate builds so they fit alongside the
                  canonical one, which always builds first. 0 uses the global
                  payload build time.
                </div>
              </div>

              <div className="form-check mb-2">
                <input
                  className="form-check-input"
                  type="checkbox"
                  id="build-parallel"
                  checked={form.parallel === true}
                  onChange={(e) => setForm({ ...form, parallel: e.target.checked })}
                />
                <label className="form-check-label" htmlFor="build-parallel">
                  Build candidates in parallel
                </label>
                <InfoHint>{HINT_PARALLEL}</InfoHint>
                <div className="form-text">
                  Builds every selected candidate concurrently, each with its
                  own payload ID, so they all complete within the designated
                  build window. Turn off to serialize them (canonical first).
                </div>
              </div>

              <div className="form-check mb-2">
                <input
                  className="form-check-input"
                  type="checkbox"
                  id="build-enforce-gas-limit"
                  checked={form.enforce_bid_gas_limit === true}
                  onChange={(e) => setForm({ ...form, enforce_bid_gas_limit: e.target.checked })}
                />
                <label className="form-check-label" htmlFor="build-enforce-gas-limit">
                  Enforce bid gas limit
                </label>
                <InfoHint>{HINT_ENFORCE_GAS_LIMIT}</InfoHint>
                <div className="form-text">
                  Rewrites the built payload&apos;s gas limit to the exact value
                  the bid gossip rules require when the EL ignored the
                  proposer&apos;s target.
                </div>
              </div>

              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
                  Cancel
                </button>
              </div>
            </form>
      )}
    </>
  );
};
