import React, { useState } from 'react';
import type { BuilderAPIConfig, BuilderAPIStatus, Config, ServiceStatus } from '../types';
import { useAuth } from '../hooks/useAuth';
import { useSettings } from '../hooks/useSettings';
import { InfoHint } from './InfoHint';

interface BuilderAPIConfigPanelProps {
  status: BuilderAPIStatus | null;
  serviceStatus: ServiceStatus | null;
  config: Config | null;
  loading?: boolean;
}

function formatGwei(gwei: number | undefined): string {
  if (!gwei) return '0';
  return gwei.toLocaleString() + ' Gwei';
}

// Which managed key signs served bids. Empty follows the ePBS bidder's
// strategy.
const KEY_STRATEGY_LABELS: Record<string, string> = {
  '': 'follow ePBS bidder',
  round_robin: 'round robin',
  single: 'single (primary key only)',
  random: 'random',
  least_used: 'least used',
};

// Setting explanations, shared by the view rows and the edit form.
const HINT_SUBSIDY = (
  <>
    <code>builder_api.block_value_subsidy_gwei</code> (gwei), added on top of the block value of
    every served bid (pre-Gloas getHeader and Gloas getExecutionPayloadBid). 0 = bid the plain
    block value. Per-slot action plans can override it.
  </>
);
const HINT_VALUE_OVERRIDE = (
  <>
    <code>builder_api.value_override_gwei</code> (gwei): when non-zero, the served bid&apos;s total
    value is exactly this amount, replacing block value + subsidy (may exceed or undercut the real
    block value). 0 = off. Per-slot action plans can override it.
  </>
);
const HINT_EXEC_PAYMENT = (
  <>
    Gloas only: part of the served total value claimed as <code>execution_payment</code>, the rest
    is paid trustlessly via the bid <code>value</code>. The claim is deliberately unbacked (buildoor
    never makes the EL payment) and capped by the proposer&apos;s advertised
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
const HINT_PREF_LIMIT = (
  <>
    <code>builder_api.ignore_preference_limit</code>. Spec-violating testing knob.
    <dl>
      <dt>respected</dt>
      <dd>the execution payment is capped at the proposer&apos;s <code>max_execution_payment</code></dd>
      <dt>ignored</dt>
      <dd>the configured execution payment is served beyond that limit; clients should reject such bids</dd>
    </dl>
  </>
);
const HINT_SERVE_CANDIDATES = (
  <>
    <code>builder_api.serve_candidates</code> (Gloas): which built candidate payloads a bid request
    may be answered from; the candidate matching the requested parent is served.
    <dl>
      <dt>all</dt>
      <dd>any candidate (default)</dd>
      <dt>canonical_only</dt>
      <dd>only <code>parent_full</code> and unclassified payloads</dd>
      <dt>comma list</dt>
      <dd>only the named keys: <code>parent_full</code>, <code>parent_empty</code>,
        <code>grandparent_full</code>, <code>grandparent_empty</code></dd>
    </dl>
    Per-slot action plans can override it.
  </>
);
const HINT_ON_DEMAND = (
  <>
    <code>builder_api.on_demand_build</code> (Gloas): when on, a bid request for a legal parent that
    no built candidate covers triggers a build on the fly, bounded by the request&apos;s response
    budget. Off = such requests get no bid.
  </>
);
const HINT_KEY_STRATEGY = (
  <>
    <code>builder_api.key_strategy</code> (Gloas): which managed builder key signs served bids. The
    choice is sticky per slot and parent, so a polling proposer keeps seeing the same builder.
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
);
const HINT_REQUEST_AUTH = (
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
);

interface BuilderAPIForm {
  block_value_subsidy_gwei: number;
  value_override_gwei: number;
  execution_payment_gwei: number;
  execution_payment_percent: number;
  ignore_preference_limit: boolean;
  serve_candidates: string;
  on_demand_build: boolean;
  key_strategy: string;
  require_request_auth: boolean;
}

const formFromConfig = (api: BuilderAPIConfig | undefined): BuilderAPIForm => ({
  block_value_subsidy_gwei: api?.block_value_subsidy_gwei ?? 0,
  value_override_gwei: api?.value_override_gwei ?? 0,
  execution_payment_gwei: api?.execution_payment_gwei ?? 0,
  execution_payment_percent: api?.execution_payment_percent ?? 0,
  ignore_preference_limit: api?.ignore_preference_limit ?? false,
  serve_candidates: api?.serve_candidates || 'all',
  on_demand_build: api?.on_demand_build ?? false,
  key_strategy: api?.key_strategy ?? '',
  require_request_auth: api?.require_request_auth ?? false,
});

// BuilderAPIConfigPanel shows and edits the global Builder API bid-serving
// settings (the baseline per-slot action plans override). Values come from the
// SSE config; edits go through the generic path-based settings endpoint with
// builder_api.* keys.
export const BuilderAPIConfigPanel: React.FC<BuilderAPIConfigPanelProps> = ({ status, serviceStatus, config, loading }) => {
  const [collapsed, setCollapsed] = useState(true);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [formData, setFormData] = useState<BuilderAPIForm>(formFromConfig(undefined));
  const { getAuthHeader, isLoggedIn } = useAuth();
  const { postSettings } = useSettings();

  const api = config?.builder_api;
  const isActive = serviceStatus?.builder_api_enabled ?? false;
  const isAvailable = serviceStatus?.builder_api_available ?? false;

  const startEditing = () => {
    setFormData(formFromConfig(api));
    setSaveError('');
    setEditing(true);
  };

  const cancelEditing = () => {
    setEditing(false);
  };

  const saveConfig = async () => {
    setSaving(true);

    const result = await postSettings({
      'builder_api.block_value_subsidy_gwei': formData.block_value_subsidy_gwei,
      'builder_api.value_override_gwei': formData.value_override_gwei,
      'builder_api.execution_payment_gwei': formData.execution_payment_gwei,
      'builder_api.execution_payment_percent': formData.execution_payment_percent,
      'builder_api.ignore_preference_limit': formData.ignore_preference_limit,
      'builder_api.serve_candidates': formData.serve_candidates.trim(),
      'builder_api.on_demand_build': formData.on_demand_build,
      'builder_api.key_strategy': formData.key_strategy,
      'builder_api.require_request_auth': formData.require_request_auth,
    });

    setSaving(false);

    if (result.ok) {
      setSaveError('');
      setEditing(false);
    } else {
      setSaveError(result.error ?? 'failed to update Builder API config');
    }
  };

  const handleToggle = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!isLoggedIn) return;
    const headers: HeadersInit = { 'Content-Type': 'application/json' };
    const token = await getAuthHeader();
    if (token) {
      headers['Authorization'] = `Bearer ${token}`;
    }
    setToggling(true);
    try {
      await fetch('/api/services/toggle', {
        method: 'POST',
        headers,
        body: JSON.stringify({ builder_api_enabled: !isActive }),
      });
    } catch (err) {
      console.error('Failed to toggle Builder API:', err);
    } finally {
      setToggling(false);
    }
  };

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">Builder API</h5>
        {loading ? (
          <span className="badge bg-secondary">Loading...</span>
        ) : !isAvailable ? (
          <span className="badge bg-dark">Not Available</span>
        ) : isActive ? (
          <span className="badge bg-success">Active</span>
        ) : (
          <span className="badge bg-secondary">Inactive</span>
        )}
        {isLoggedIn && (
          <button
            className={`btn btn-sm ms-auto ${isActive ? 'btn-outline-danger' : 'btn-outline-success'}`}
            onClick={handleToggle}
            disabled={toggling || !isAvailable}
            title={!isAvailable ? 'Builder API not available (no port configured)' : isActive ? 'Disable Builder API' : 'Enable Builder API'}
          >
            <i className={`fas ${isActive ? 'fa-pause' : 'fa-play'}`}></i>
          </button>
        )}
      </div>
      {!collapsed && (
        <div className="card-body">
          {!isAvailable && (
            <div className="alert alert-secondary small mb-2 py-1 px-2">
              <i className="fas fa-info-circle me-1"></i>
              Builder API is not available. Set <code>--api-port</code> to enable it.
            </div>
          )}
          {loading ? (
            <div className="text-muted text-center">Loading...</div>
          ) : !status ? (
            <div className="text-muted text-center">Status unavailable</div>
          ) : (
            <>
              {/* Info row */}
              <div className="row g-2 mb-2">
                <div className="col-6">
                  <div className="config-item">
                    <div className="config-item-label">
                      Validators
                      <InfoHint>
                        Number of validator registrations (<code>registerValidators</code>) stored
                        for the pre-Gloas Builder API; payloads for a registered proposer pay
                        its registered fee recipient.
                      </InfoHint>
                    </div>
                    <div className="config-item-value">{status.validator_count}</div>
                  </div>
                </div>
                <div className="col-6">
                  <div className="config-item">
                    <div className="config-item-label">
                      Builder URL
                      <InfoHint alignEnd>
                        <code>--builder-api-url</code>: this builder&apos;s public URL. Its hostname
                        (or the URL verbatim) must match <code>auth.message.data</code> of the
                        proposer&apos;s request auth on the Gloas bid and preferences endpoints.
                        Empty (—) = the match is skipped.
                      </InfoHint>
                    </div>
                    <div className="config-item-value">{api?.builder_url || '—'}</div>
                  </div>
                </div>
              </div>

              {/* Configuration */}
              {!editing ? (
                <>
                  <div className="d-flex justify-content-between align-items-center mb-1">
                    <div className="section-header">Configuration</div>
                    {isLoggedIn && (
                      <button className="btn btn-sm btn-outline-primary" onClick={(e) => { e.stopPropagation(); startEditing(); }}>
                        <i className="fas fa-pencil-alt"></i>
                      </button>
                    )}
                  </div>
                  <div className="row g-2">
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Block Value Subsidy<InfoHint>{HINT_SUBSIDY}</InfoHint></div>
                        <div className="config-item-value">{formatGwei(api?.block_value_subsidy_gwei)}</div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Value Override<InfoHint alignEnd>{HINT_VALUE_OVERRIDE}</InfoHint></div>
                        <div className="config-item-value">
                          {api?.value_override_gwei ? formatGwei(api.value_override_gwei) : 'off'}
                        </div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Execution Payment<InfoHint>{HINT_EXEC_PAYMENT}</InfoHint></div>
                        <div className="config-item-value">
                          {api?.execution_payment_gwei
                            ? formatGwei(api.execution_payment_gwei)
                            : api?.execution_payment_percent
                              ? `${api.execution_payment_percent}%`
                              : 'off'}
                        </div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Preference Limit<InfoHint alignEnd>{HINT_PREF_LIMIT}</InfoHint></div>
                        <div className="config-item-value">
                          {api?.ignore_preference_limit ? 'ignored' : 'respected'}
                        </div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Serve Candidates<InfoHint>{HINT_SERVE_CANDIDATES}</InfoHint></div>
                        <div className="config-item-value">{api?.serve_candidates || 'all'}</div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">On-Demand Build<InfoHint alignEnd>{HINT_ON_DEMAND}</InfoHint></div>
                        <div className="config-item-value">{api?.on_demand_build ? 'on' : 'off'}</div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Key Strategy<InfoHint>{HINT_KEY_STRATEGY}</InfoHint></div>
                        <div className="config-item-value">
                          {KEY_STRATEGY_LABELS[api?.key_strategy ?? ''] ?? api?.key_strategy}
                        </div>
                      </div>
                    </div>
                    <div className="col-6">
                      <div className="config-item">
                        <div className="config-item-label">Request Auth<InfoHint alignEnd>{HINT_REQUEST_AUTH}</InfoHint></div>
                        <div className="config-item-value">
                          {api?.require_request_auth ? 'required' : 'optional'}
                        </div>
                      </div>
                    </div>
                  </div>
                </>
              ) : (
                <>
                  <div className="section-header mb-1">Configuration</div>
                  <div className="mb-2">
                    <label className="form-label">Block Value Subsidy (Gwei)<InfoHint>{HINT_SUBSIDY}</InfoHint></label>
                    <input
                      type="number"
                      min={0}
                      className="form-control form-control-sm"
                      value={formData.block_value_subsidy_gwei}
                      onChange={(e) => setFormData({ ...formData, block_value_subsidy_gwei: parseInt(e.target.value) || 0 })}
                    />
                    <div className="form-text">Added to the block value of every served bid.</div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label">Value Override (Gwei)<InfoHint>{HINT_VALUE_OVERRIDE}</InfoHint></label>
                    <input
                      type="number"
                      min={0}
                      className="form-control form-control-sm"
                      value={formData.value_override_gwei}
                      onChange={(e) => setFormData({ ...formData, value_override_gwei: parseInt(e.target.value) || 0 })}
                    />
                    <div className="form-text">
                      Absolute total value served instead of block value + subsidy. 0 = off.
                    </div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label">Execution Payment (Gwei)<InfoHint>{HINT_EXEC_PAYMENT}</InfoHint></label>
                    <input
                      type="number"
                      min={0}
                      className="form-control form-control-sm"
                      value={formData.execution_payment_gwei}
                      onChange={(e) => setFormData({ ...formData, execution_payment_gwei: parseInt(e.target.value) || 0 })}
                    />
                    <div className="form-text">
                      Portion of a served Gloas bid claimed as execution_payment — an
                      unbacked claim, capped by the proposer's max_execution_payment.
                      Wins over the percentage. 0 = off.
                    </div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label">Execution Payment (%)<InfoHint>{HINT_EXEC_PAYMENT}</InfoHint></label>
                    <input
                      type="number"
                      min={0}
                      max={100}
                      className="form-control form-control-sm"
                      value={formData.execution_payment_percent}
                      disabled={formData.execution_payment_gwei > 0}
                      onChange={(e) => setFormData({ ...formData, execution_payment_percent: parseInt(e.target.value) || 0 })}
                    />
                    <div className="form-text">
                      Same claim as a percentage of the total value; used only while the
                      absolute amount is 0.
                    </div>
                  </div>
                  <div className="form-check mb-2">
                    <input
                      type="checkbox"
                      className="form-check-input"
                      id="builder-api-ignore-preference-limit"
                      checked={formData.ignore_preference_limit}
                      onChange={(e) => setFormData({ ...formData, ignore_preference_limit: e.target.checked })}
                    />
                    <label className="form-check-label" htmlFor="builder-api-ignore-preference-limit">
                      Ignore the proposer's max_execution_payment
                      <InfoHint>{HINT_PREF_LIMIT}</InfoHint>
                    </label>
                    <div className="form-text">
                      Serves the execution payment beyond the advertised limit — a
                      spec-violating bid clients should reject.
                    </div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label">Serve Candidates<InfoHint>{HINT_SERVE_CANDIDATES}</InfoHint></label>
                    <input
                      type="text"
                      className="form-control form-control-sm"
                      value={formData.serve_candidates}
                      onChange={(e) => setFormData({ ...formData, serve_candidates: e.target.value })}
                    />
                    <div className="form-text">
                      <code>all</code>, <code>canonical_only</code>, or a comma-separated list
                      of parent_full, parent_empty, grandparent_full, grandparent_empty.
                    </div>
                  </div>
                  <div className="form-check mb-2">
                    <input
                      type="checkbox"
                      className="form-check-input"
                      id="builder-api-on-demand-build"
                      checked={formData.on_demand_build}
                      onChange={(e) => setFormData({ ...formData, on_demand_build: e.target.checked })}
                    />
                    <label className="form-check-label" htmlFor="builder-api-on-demand-build">
                      Build on demand
                      <InfoHint>{HINT_ON_DEMAND}</InfoHint>
                    </label>
                    <div className="form-text">
                      Builds a payload on the fly when a bid request asks for a legal
                      parent no candidate covers yet.
                    </div>
                  </div>
                  <div className="mb-2">
                    <label className="form-label">Key Strategy<InfoHint>{HINT_KEY_STRATEGY}</InfoHint></label>
                    <select
                      className="form-select form-select-sm"
                      value={formData.key_strategy}
                      onChange={(e) => setFormData({ ...formData, key_strategy: e.target.value })}
                    >
                      {Object.entries(KEY_STRATEGY_LABELS).map(([value, label]) => (
                        <option key={value} value={value}>{label}</option>
                      ))}
                    </select>
                  </div>
                  <div className="form-check mb-2">
                    <input
                      type="checkbox"
                      className="form-check-input"
                      id="builder-api-require-request-auth"
                      checked={formData.require_request_auth}
                      onChange={(e) => setFormData({ ...formData, require_request_auth: e.target.checked })}
                    />
                    <label className="form-check-label" htmlFor="builder-api-require-request-auth">
                      Require request auth
                      <InfoHint>{HINT_REQUEST_AUTH}</InfoHint>
                    </label>
                    <div className="form-text">
                      Rejects bid requests without a SignedRequestAuthV1 body (401).
                      Supplied auth is always verified either way.
                    </div>
                  </div>
                  {saveError && <div className="small text-danger mb-2">{saveError}</div>}
                  <div className="d-flex gap-2">
                    <button className="btn btn-sm btn-primary" onClick={saveConfig} disabled={saving}>
                      {saving ? 'Saving...' : 'Save'}
                    </button>
                    <button className="btn btn-sm btn-outline-secondary" onClick={cancelEditing}>
                      Cancel
                    </button>
                  </div>
                </>
              )}
            </>
          )}
        </div>
      )}
    </div>
  );
};
