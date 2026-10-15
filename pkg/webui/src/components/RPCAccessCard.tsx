import React, { useEffect, useMemo, useState } from 'react';
import { useSettings } from '../hooks/useSettings';
import { useAuthContext } from '../context/AuthContext';
import { useRPCAccess } from '../hooks/useRPCAccess';
import { InfoHint } from './InfoHint';
import type { CreateRPCKeyResponse, RPCCallerStats, RPCKey, TxPoolConfig } from '../types';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function relativeTime(iso: string | undefined): string {
  if (!iso) return '—';
  const diff = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(diff)) return '—';
  if (diff < 1000) return 'just now';
  if (diff < 60000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
  return `${Math.floor(diff / 86400000)}d ago`;
}

function formatRate(rate: number | undefined): string {
  if (rate === undefined) return '—';
  return rate === 0 ? 'unlimited' : `${rate}/s`;
}

const CopyButton: React.FC<{ value: string; title?: string }> = ({ value, title }) => {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="btn btn-sm btn-outline-secondary py-0"
      title={title ?? 'Copy to clipboard'}
      onClick={() => {
        navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }).catch(() => undefined);
      }}
    >
      <i className={`fas ${copied ? 'fa-check' : 'fa-copy'}`}></i>
    </button>
  );
};

// ---------------------------------------------------------------------------
// Upstreams (txpool.rpc_upstreams: comma-separated <target>@<priority>)
// ---------------------------------------------------------------------------

interface UpstreamRow {
  target: string;
  priority: string;
}

// parseUpstreams mirrors config.ParseRPCUpstreams: the priority is the part
// after the last '@' unless that part holds a '/' (an '@' inside a URL).
function parseUpstreams(spec: string | undefined): UpstreamRow[] {
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

function serializeUpstreams(rows: UpstreamRow[]): string {
  return rows
    .filter((row) => row.target.trim() !== '')
    .map((row) => `${row.target.trim()}@${row.priority.trim() === '' ? '0' : row.priority.trim()}`)
    .join(',');
}

const UpstreamEditor: React.FC<{ rows: UpstreamRow[]; onChange: (rows: UpstreamRow[]) => void }> = ({ rows, onChange }) => {
  const update = (i: number, patch: Partial<UpstreamRow>) =>
    onChange(rows.map((row, j) => (j === i ? { ...row, ...patch } : row)));

  return (
    <div>
      {rows.map((row, i) => (
        <div className="d-flex gap-1 mb-1" key={i}>
          <input
            type="text"
            className="form-control form-control-sm font-monospace"
            placeholder="local or http(s)/ws(s) URL"
            value={row.target}
            onChange={(e) => update(i, { target: e.target.value })}
          />
          <button
            type="button"
            className="btn btn-sm btn-outline-secondary text-nowrap"
            title="The EL behind --el-rpc"
            onClick={() => update(i, { target: 'local' })}
          >
            local
          </button>
          <input
            type="number"
            min={0}
            className="form-control form-control-sm"
            style={{ width: '6rem' }}
            title="Priority (lowest tried first)"
            value={row.priority}
            onChange={(e) => update(i, { priority: e.target.value })}
          />
          <button
            type="button"
            className="btn btn-sm btn-outline-danger"
            title="Remove"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            <i className="fas fa-times"></i>
          </button>
        </div>
      ))}
      <button
        type="button"
        className="btn btn-sm btn-outline-secondary"
        onClick={() => onChange([...rows, { target: '', priority: String(rows.length) }])}
      >
        <i className="fas fa-plus me-1"></i>Add upstream
      </button>
    </div>
  );
};

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

const AUTH_MODES: Array<{ value: string; hint: string }> = [
  { value: 'open', hint: 'Anonymous callers are allowed, rate-limited by the anonymous rate.' },
  { value: 'api_key', hint: 'An API key, the static --txpool-auth-token or a WebUI login is required.' },
];

const ANON_SCOPES: Array<{ value: string; hint: string }> = [
  { value: 'per_ip', hint: 'Each client IP gets its own limit and stats row.' },
  { value: 'shared', hint: 'One limit and one stats row for all anonymous callers together.' },
];

const HINT_ANON_RATE = 'Anonymous /rpc calls per second (per client IP or shared, see the anonymous scope). 0 = unlimited.';
const HINT_KEY_RATE = 'Default /rpc calls per second per API key; a key can override it. Also applies to the static token. 0 = unlimited.';
const HINT_PROXY_HOPS =
  'Trusted reverse proxies in front of /rpc. The client IP is the X-Forwarded-For entry that many hops from the right; ' +
  '0 = the connection address, headers ignored. Behind one nginx = 1. Too high lets callers spoof their IP.';

const UpstreamsHint: React.FC = () => (
  <>
    The endpoints the read passthrough (every call except <code>eth_sendRawTransaction</code>) is sent to.{' '}
    <strong>local</strong> is the EL behind <code>--el-rpc</code>; other entries are http(s)/ws(s) URLs. The
    lowest priority is tried first, endpoints of equal priority take turns (round robin), and a transport
    failure moves on to the next endpoint. JSON-RPC errors are answers, not failures.
  </>
);

interface AccessForm {
  auth: string;
  rpc_anon_rate: number;
  rpc_anon_scope: string;
  rpc_key_rate: number;
  rpc_proxy_hops: number;
  upstreams: UpstreamRow[];
}

function accessForm(cfg: TxPoolConfig | undefined): AccessForm {
  return {
    auth: cfg?.auth || 'open',
    rpc_anon_rate: cfg?.rpc_anon_rate ?? 20,
    rpc_anon_scope: cfg?.rpc_anon_scope || 'per_ip',
    rpc_key_rate: cfg?.rpc_key_rate ?? 0,
    rpc_proxy_hops: cfg?.rpc_proxy_hops ?? 0,
    upstreams: parseUpstreams(cfg?.rpc_upstreams || 'local'),
  };
}

const AccessSettings: React.FC<{ cfg: TxPoolConfig | undefined }> = ({ cfg }) => {
  const { isLoggedIn, postSettings } = useSettings();
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [form, setForm] = useState<AccessForm>(() => accessForm(cfg));

  useEffect(() => {
    if (!editing) setForm(accessForm(cfg));
  }, [cfg, editing]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMessage(null);
    const result = await postSettings({
      'txpool.auth': form.auth,
      'txpool.rpc_anon_rate': form.rpc_anon_rate,
      'txpool.rpc_anon_scope': form.rpc_anon_scope,
      'txpool.rpc_key_rate': form.rpc_key_rate,
      'txpool.rpc_proxy_hops': form.rpc_proxy_hops,
      'txpool.rpc_upstreams': serializeUpstreams(form.upstreams),
    });
    setBusy(false);
    if (!result.ok) {
      setMessage(result.error ?? 'failed to update RPC access settings');
      return;
    }
    setEditing(false);
  };

  const current = accessForm(cfg);

  return (
    <div className="mb-3">
      <div className="d-flex align-items-center gap-2 mb-1">
        <div className="section-header mb-0">Settings</div>
        {isLoggedIn && !editing && (
          <button className="btn btn-sm btn-outline-primary ms-auto" onClick={() => setEditing(true)}>
            <i className="fas fa-pencil-alt"></i>
          </button>
        )}
      </div>
      {message && <div className="alert alert-warning small py-1 px-2 mb-2">{message}</div>}
      {!editing ? (
        <div className="row g-2">
          <div className="col-6 col-md-4 col-xl-2">
            <div className="config-item">
              <div className="config-item-label">
                Access
                <InfoHint>
                  Who may call <code>/rpc</code>. Authenticated callers are always accepted.
                  <dl>
                    {AUTH_MODES.map((m) => (
                      <React.Fragment key={m.value}>
                        <dt>{m.value}</dt>
                        <dd>{m.hint}</dd>
                      </React.Fragment>
                    ))}
                  </dl>
                </InfoHint>
              </div>
              <div className="config-item-value">{current.auth}</div>
            </div>
          </div>
          <div className="col-6 col-md-4 col-xl-2">
            <div className="config-item">
              <div className="config-item-label">Anonymous rate<InfoHint>{HINT_ANON_RATE}</InfoHint></div>
              <div className="config-item-value">{formatRate(current.rpc_anon_rate)}</div>
            </div>
          </div>
          <div className="col-6 col-md-4 col-xl-2">
            <div className="config-item">
              <div className="config-item-label">
                Anonymous scope
                <InfoHint>
                  <dl>
                    {ANON_SCOPES.map((m) => (
                      <React.Fragment key={m.value}>
                        <dt>{m.value}</dt>
                        <dd>{m.hint}</dd>
                      </React.Fragment>
                    ))}
                  </dl>
                </InfoHint>
              </div>
              <div className="config-item-value">{current.rpc_anon_scope}</div>
            </div>
          </div>
          <div className="col-6 col-md-4 col-xl-2">
            <div className="config-item">
              <div className="config-item-label">Key rate<InfoHint>{HINT_KEY_RATE}</InfoHint></div>
              <div className="config-item-value">{formatRate(current.rpc_key_rate)}</div>
            </div>
          </div>
          <div className="col-6 col-md-4 col-xl-2">
            <div className="config-item">
              <div className="config-item-label">Proxy hops<InfoHint alignEnd>{HINT_PROXY_HOPS}</InfoHint></div>
              <div className="config-item-value">{current.rpc_proxy_hops}</div>
            </div>
          </div>
          <div className="col-12">
            <div className="config-item">
              <div className="config-item-label">Read upstreams<InfoHint><UpstreamsHint /></InfoHint></div>
              <div className="config-item-value font-monospace small">
                {current.upstreams.map((u) => `${u.target} (priority ${u.priority})`).join(', ') || '—'}
              </div>
            </div>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSave}>
          <div className="row g-2">
            <div className="col-6 col-md-4">
              <label className="form-label small" htmlFor="rpc-auth">Access</label>
              <select
                id="rpc-auth"
                className="form-select form-select-sm"
                value={form.auth}
                onChange={(e) => setForm({ ...form, auth: e.target.value })}
              >
                {AUTH_MODES.map((m) => <option key={m.value} value={m.value}>{m.value}</option>)}
              </select>
              <div className="form-text">{AUTH_MODES.find((m) => m.value === form.auth)?.hint}</div>
            </div>
            <div className="col-6 col-md-4">
              <label className="form-label small" htmlFor="rpc-anon-rate">Anonymous rate (calls/s)</label>
              <input
                type="number"
                id="rpc-anon-rate"
                min={0}
                className="form-control form-control-sm"
                value={form.rpc_anon_rate}
                onChange={(e) => setForm({ ...form, rpc_anon_rate: Number(e.target.value) })}
              />
              <div className="form-text">{HINT_ANON_RATE}</div>
            </div>
            <div className="col-6 col-md-4">
              <label className="form-label small" htmlFor="rpc-anon-scope">Anonymous scope</label>
              <select
                id="rpc-anon-scope"
                className="form-select form-select-sm"
                value={form.rpc_anon_scope}
                onChange={(e) => setForm({ ...form, rpc_anon_scope: e.target.value })}
              >
                {ANON_SCOPES.map((m) => <option key={m.value} value={m.value}>{m.value}</option>)}
              </select>
              <div className="form-text">{ANON_SCOPES.find((m) => m.value === form.rpc_anon_scope)?.hint}</div>
            </div>
            <div className="col-6 col-md-4">
              <label className="form-label small" htmlFor="rpc-key-rate">Key rate (calls/s)</label>
              <input
                type="number"
                id="rpc-key-rate"
                min={0}
                className="form-control form-control-sm"
                value={form.rpc_key_rate}
                onChange={(e) => setForm({ ...form, rpc_key_rate: Number(e.target.value) })}
              />
              <div className="form-text">{HINT_KEY_RATE}</div>
            </div>
            <div className="col-6 col-md-4">
              <label className="form-label small" htmlFor="rpc-proxy-hops">Proxy hops</label>
              <input
                type="number"
                id="rpc-proxy-hops"
                min={0}
                max={16}
                className="form-control form-control-sm"
                value={form.rpc_proxy_hops}
                onChange={(e) => setForm({ ...form, rpc_proxy_hops: Number(e.target.value) })}
              />
              <div className="form-text">{HINT_PROXY_HOPS}</div>
            </div>
            <div className="col-12">
              <label className="form-label small mb-1">
                Read upstreams<InfoHint><UpstreamsHint /></InfoHint>
              </label>
              <UpstreamEditor rows={form.upstreams} onChange={(upstreams) => setForm({ ...form, upstreams })} />
            </div>
          </div>
          <div className="d-flex gap-2 mt-2">
            <button type="submit" className="btn btn-sm btn-primary" disabled={busy}>Save</button>
            <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------
// New key secret (shown once)
// ---------------------------------------------------------------------------

const SecretAlert: React.FC<{ created: CreateRPCKeyResponse; onDismiss: () => void }> = ({ created, onDismiss }) => {
  const url = `${window.location.origin}/rpc/${created.secret}`;
  const header = `X-API-Key: ${created.secret}`;
  const rows: Array<[string, string]> = [
    ['Key', created.secret],
    ['URL', url],
    ['Header', header],
  ];

  return (
    <div className="alert alert-success small py-2 px-2 mb-2">
      <div className="d-flex align-items-center mb-1">
        <strong>API key &quot;{created.key.label}&quot; created.</strong>
        <button type="button" className="btn-close ms-auto" aria-label="Dismiss" onClick={onDismiss}></button>
      </div>
      <div className="mb-1">
        Copy it now: the key is stored only as a hash and <strong>cannot be shown again</strong>.
      </div>
      {rows.map(([label, value]) => (
        <div className="d-flex align-items-center gap-2 mb-1" key={label}>
          <span className="text-muted" style={{ width: '4rem' }}>{label}</span>
          <code className="text-break flex-grow-1">{value}</code>
          <CopyButton value={value} />
        </div>
      ))}
      <div className="text-muted">
        Send it as the URL above, as <code>X-API-Key: &lt;key&gt;</code> or as{' '}
        <code>Authorization: Bearer &lt;key&gt;</code>.
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

type RateMode = 'default' | 'override';

interface KeyEdit {
  label: string;
  mode: RateMode;
  rate: string;
}

const KeysTable: React.FC<{
  keys: RPCKey[];
  callers: RPCCallerStats[];
  defaultRate: number;
  onUpdate: ReturnType<typeof useRPCAccess>['updateKey'];
  onDelete: ReturnType<typeof useRPCAccess>['deleteKey'];
  onError: (message: string | null) => void;
}> = ({ keys, callers, defaultRate, onUpdate, onDelete, onError }) => {
  const [editId, setEditId] = useState<string | null>(null);
  const [edit, setEdit] = useState<KeyEdit>({ label: '', mode: 'default', rate: '' });
  const [busy, setBusy] = useState(false);

  const callsByKey = useMemo(() => {
    const map: Record<string, number> = {};
    for (const caller of callers) {
      if (caller.kind === 'api_key') map[caller.id] = caller.calls;
    }
    return map;
  }, [callers]);

  const startEdit = (key: RPCKey) => {
    setEditId(key.id);
    setEdit({
      label: key.label,
      mode: key.rate_limit === undefined ? 'default' : 'override',
      rate: key.rate_limit === undefined ? '' : String(key.rate_limit),
    });
  };

  const run = async (action: () => Promise<{ ok: boolean; error?: string }>) => {
    setBusy(true);
    onError(null);
    const result = await action();
    setBusy(false);
    if (!result.ok) onError(result.error ?? 'request failed');
    return result.ok;
  };

  const saveEdit = async (key: RPCKey) => {
    const update: Parameters<typeof onUpdate>[1] = {};
    if (edit.label.trim() !== key.label) update.label = edit.label.trim();
    if (edit.mode === 'default') {
      if (key.rate_limit !== undefined) update.default_rate_limit = true;
    } else {
      const rate = Number(edit.rate);
      if (edit.rate.trim() === '' || !Number.isInteger(rate) || rate < 0) {
        onError('rate limit must be a whole number ≥ 0 (0 = unlimited)');
        return;
      }
      if (rate !== key.rate_limit) update.rate_limit = rate;
    }
    if (Object.keys(update).length === 0) {
      setEditId(null);
      return;
    }
    if (await run(() => onUpdate(key.id, update))) setEditId(null);
  };

  const remove = async (key: RPCKey) => {
    if (!window.confirm(`Delete API key "${key.label}"? Callers using it are refused from the next request on.`)) return;
    await run(() => onDelete(key.id));
  };

  if (keys.length === 0) {
    return <div className="text-muted small mb-2">No API keys yet.</div>;
  }

  return (
    <div className="table-responsive mb-2">
      <table className="table table-sm mb-0 small align-middle">
        <thead>
          <tr>
            <th>Label</th>
            <th>
              Prefix<InfoHint>The first characters of the key, to recognise it. The full key is only shown at creation.</InfoHint>
            </th>
            <th>
              Rate<InfoHint>Calls per second for this key: <strong>default</strong> follows the key rate setting, an override is the key&apos;s own limit, 0 = unlimited.</InfoHint>
            </th>
            <th>
              Enabled<InfoHint>A disabled key is refused (401) without being deleted.</InfoHint>
            </th>
            <th>Created</th>
            <th className="text-end">
              Calls<InfoHint alignEnd>/rpc calls made with this key since buildoor started.</InfoHint>
            </th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {keys.map((key) => {
            const editingRow = editId === key.id;
            return (
              <tr key={key.id}>
                <td>
                  {editingRow ? (
                    <input
                      type="text"
                      className="form-control form-control-sm"
                      value={edit.label}
                      onChange={(e) => setEdit({ ...edit, label: e.target.value })}
                    />
                  ) : key.label}
                </td>
                <td className="font-monospace">{key.prefix}…</td>
                <td>
                  {editingRow ? (
                    <div className="d-flex gap-1">
                      <select
                        className="form-select form-select-sm w-auto"
                        value={edit.mode}
                        onChange={(e) => setEdit({ ...edit, mode: e.target.value as RateMode })}
                      >
                        <option value="default">default ({formatRate(defaultRate)})</option>
                        <option value="override">override</option>
                      </select>
                      {edit.mode === 'override' && (
                        <input
                          type="number"
                          min={0}
                          className="form-control form-control-sm"
                          style={{ width: '6rem' }}
                          placeholder="calls/s"
                          value={edit.rate}
                          onChange={(e) => setEdit({ ...edit, rate: e.target.value })}
                        />
                      )}
                    </div>
                  ) : key.rate_limit === undefined ? (
                    <span className="text-muted">default ({formatRate(defaultRate)})</span>
                  ) : (
                    <span>{formatRate(key.rate_limit)} <span className="badge bg-info text-dark">override</span></span>
                  )}
                </td>
                <td>
                  <div className="form-check form-switch mb-0">
                    <input
                      className="form-check-input"
                      type="checkbox"
                      checked={key.enabled}
                      disabled={busy}
                      onChange={(e) => run(() => onUpdate(key.id, { enabled: e.target.checked }))}
                    />
                  </div>
                </td>
                <td title={key.created_at}>{relativeTime(key.created_at)}</td>
                <td className="text-end">{(callsByKey[key.id] ?? 0).toLocaleString()}</td>
                <td className="text-end text-nowrap">
                  {editingRow ? (
                    <>
                      <button className="btn btn-sm btn-primary py-0 me-1" disabled={busy} onClick={() => saveEdit(key)}>Save</button>
                      <button className="btn btn-sm btn-secondary py-0" onClick={() => setEditId(null)}>Cancel</button>
                    </>
                  ) : (
                    <>
                      <button className="btn btn-sm btn-outline-primary py-0 me-1" title="Edit" onClick={() => startEdit(key)}>
                        <i className="fas fa-pencil-alt"></i>
                      </button>
                      <button className="btn btn-sm btn-outline-danger py-0" title="Delete" disabled={busy} onClick={() => remove(key)}>
                        <i className="fas fa-trash"></i>
                      </button>
                    </>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
};

const CreateKeyForm: React.FC<{
  onCreate: ReturnType<typeof useRPCAccess>['createKey'];
  onCreated: (created: CreateRPCKeyResponse) => void;
  onError: (message: string | null) => void;
}> = ({ onCreate, onCreated, onError }) => {
  const [label, setLabel] = useState('');
  const [rate, setRate] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    onError(null);
    let rateLimit: number | undefined;
    if (rate.trim() !== '') {
      rateLimit = Number(rate);
      if (!Number.isInteger(rateLimit) || rateLimit < 0) {
        onError('rate limit must be a whole number ≥ 0 (0 = unlimited)');
        return;
      }
    }
    setBusy(true);
    const result = await onCreate(label.trim(), rateLimit);
    setBusy(false);
    if (!result.ok) {
      onError(result.error);
      return;
    }
    setLabel('');
    setRate('');
    onCreated(result.value);
  };

  return (
    <form className="d-flex flex-wrap gap-1 align-items-center mb-2" onSubmit={submit}>
      <input
        type="text"
        className="form-control form-control-sm"
        style={{ width: '16rem' }}
        placeholder="label (e.g. spamoor-1)"
        value={label}
        onChange={(e) => setLabel(e.target.value)}
      />
      <input
        type="number"
        min={0}
        className="form-control form-control-sm"
        style={{ width: '9rem' }}
        placeholder="rate (default)"
        value={rate}
        onChange={(e) => setRate(e.target.value)}
      />
      <InfoHint>
        Optional calls per second for the new key (0 = unlimited). Empty follows the key rate setting.
      </InfoHint>
      <button type="submit" className="btn btn-sm btn-outline-primary" disabled={busy || label.trim() === ''}>
        <i className="fas fa-key me-1"></i>Create API key
      </button>
    </form>
  );
};

// ---------------------------------------------------------------------------
// Callers + upstreams
// ---------------------------------------------------------------------------

const KIND_BADGES: Record<RPCCallerStats['kind'], string> = {
  anonymous: 'bg-secondary',
  api_key: 'bg-primary',
  static: 'bg-info text-dark',
  login: 'bg-success',
};

const MethodSummary: React.FC<{ methods: Record<string, number> }> = ({ methods }) => {
  const sorted = Object.entries(methods ?? {}).sort((a, b) => b[1] - a[1]);
  if (sorted.length === 0) return <span className="text-muted">—</span>;
  const all = sorted.map(([m, n]) => `${m}: ${n}`).join('\n');
  return (
    <span title={all}>
      {sorted.slice(0, 3).map(([m, n]) => `${m} ${n}`).join(', ')}
      {sorted.length > 3 && <span className="text-muted"> +{sorted.length - 3} more</span>}
    </span>
  );
};

const CallersTable: React.FC<{ callers: RPCCallerStats[] }> = ({ callers }) => {
  if (callers.length === 0) {
    return <div className="text-muted small mb-2">No /rpc calls yet.</div>;
  }
  const sorted = [...callers].sort((a, b) => new Date(b.last_seen).getTime() - new Date(a.last_seen).getTime());
  return (
    <div className="table-responsive mb-2">
      <table className="table table-sm mb-0 small align-middle">
        <thead>
          <tr>
            <th>
              Kind
              <InfoHint>
                <dl>
                  <dt>anonymous</dt><dd>no credentials; one row per client IP, or one shared row (anonymous scope)</dd>
                  <dt>api_key</dt><dd>an API key managed above</dd>
                  <dt>static</dt><dd>the static --txpool-auth-token</dd>
                  <dt>login</dt><dd>a WebUI login token</dd>
                </dl>
              </InfoHint>
            </th>
            <th>Caller</th>
            <th>Limit<InfoHint>Effective calls per second for this caller (0 = unlimited).</InfoHint></th>
            <th className="text-end">Calls</th>
            <th className="text-end">Errors<InfoHint>Calls that ended in an error answer.</InfoHint></th>
            <th className="text-end">Rate-limited<InfoHint>Calls refused because the caller exceeded its limit (counted in Calls too).</InfoHint></th>
            <th>Methods<InfoHint>The three most called methods; hover the cell for all.</InfoHint></th>
            <th>Last seen</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((c) => (
            <tr key={`${c.kind}:${c.id}`}>
              <td><span className={`badge ${KIND_BADGES[c.kind] ?? 'bg-secondary'}`}>{c.kind}</span></td>
              <td title={c.id}>
                {c.label ? <>{c.label} <span className="text-muted">{c.id}</span></> : c.id}
              </td>
              <td>{formatRate(c.rate_limit)}</td>
              <td className="text-end">{c.calls.toLocaleString()}</td>
              <td className="text-end">{c.errors ? <span className="text-danger">{c.errors.toLocaleString()}</span> : 0}</td>
              <td className="text-end">{c.rate_limited ? <span className="badge bg-warning text-dark">{c.rate_limited.toLocaleString()}</span> : 0}</td>
              <td><MethodSummary methods={c.methods} /></td>
              <td title={`first seen ${c.first_seen}, last seen ${c.last_seen}`}>{relativeTime(c.last_seen)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

const UpstreamsTable: React.FC<{ upstreams: RPCAccessUpstreams }> = ({ upstreams }) => {
  if (upstreams.length === 0) {
    return <div className="text-muted small">No upstreams.</div>;
  }
  return (
    <div className="table-responsive">
      <table className="table table-sm mb-0 small align-middle">
        <thead>
          <tr>
            <th>Target</th>
            <th className="text-end">Priority<InfoHint>Lowest tried first; equal priorities take turns.</InfoHint></th>
            <th>Status<InfoHint>Unavailable: the endpoint could not be set up (the error says why, e.g. no --el-rpc for local or a failed dial), so calls skip it.</InfoHint></th>
            <th className="text-end">Calls</th>
            <th className="text-end">Failures<InfoHint alignEnd>Transport failures that moved the call on to the next endpoint. JSON-RPC errors are answers and do not count.</InfoHint></th>
          </tr>
        </thead>
        <tbody>
          {upstreams.map((u) => (
            <tr key={u.target}>
              <td className="font-monospace text-break">{u.target}</td>
              <td className="text-end">{u.priority}</td>
              <td>
                {u.available
                  ? <span className="badge bg-success">available</span>
                  : <span className="badge bg-danger" title={u.error}>unavailable</span>}
                {u.error && <span className="text-muted ms-1">{u.error}</span>}
              </td>
              <td className="text-end">{u.calls.toLocaleString()}</td>
              <td className="text-end">{u.failures ? <span className="text-danger">{u.failures.toLocaleString()}</span> : 0}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

type RPCAccessUpstreams = NonNullable<ReturnType<typeof useRPCAccess>['data']>['upstreams'];

// usePageVisible pauses polling while the tab is hidden.
function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => document.visibilityState !== 'hidden');
  useEffect(() => {
    const onChange = () => setVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', onChange);
    return () => document.removeEventListener('visibilitychange', onChange);
  }, []);
  return visible;
}

// ---------------------------------------------------------------------------
// Card
// ---------------------------------------------------------------------------

// RPCAccessCard manages who may call the /rpc ingress: the access settings,
// API keys (secret shown once at creation), per-caller call stats and the
// read upstreams.
export const RPCAccessCard: React.FC<{ cfg: TxPoolConfig | undefined }> = ({ cfg }) => {
  const { isLoggedIn, authEnabled, login } = useAuthContext();
  const visible = usePageVisible();
  const { data, error, unauthorized, unavailable, createKey, updateKey, deleteKey } = useRPCAccess(visible);
  const [message, setMessage] = useState<string | null>(null);
  const [created, setCreated] = useState<CreateRPCKeyResponse | null>(null);

  const showData = isLoggedIn && !unauthorized;

  return (
    <div className="card mb-3">
      <div className="card-header py-1 d-flex align-items-center gap-2">
        <strong className="small">RPC access</strong>
        <span className="text-muted small">who may call <code>/rpc</code></span>
      </div>
      <div className="card-body py-2">
        <div className="small text-muted mb-2">
          Transactions sent to <code>/rpc</code> are private: anonymous callers can never read the pool
          back, an API key only sees the transactions it submitted, and operators (the static
          token or a WebUI login) see all of them.
        </div>

        <AccessSettings cfg={cfg} />

        {!showData ? (
          <div className="alert alert-secondary small py-1 px-2 mb-0 d-flex align-items-center gap-2">
            <i className="fas fa-lock"></i>
            Log in to manage API keys and see per-caller stats and upstreams.
            {authEnabled && (
              <button className="btn btn-sm btn-outline-primary py-0 ms-auto" onClick={login}>Log in</button>
            )}
          </div>
        ) : unavailable ? (
          <div className="alert alert-secondary small py-1 px-2 mb-0">
            The /rpc ingress is not available (no --el-rpc).
          </div>
        ) : (
          <>
            {error && <div className="alert alert-warning small py-1 px-2 mb-2">{error}</div>}
            {message && <div className="alert alert-warning small py-1 px-2 mb-2">{message}</div>}

            <div className="section-header mb-1">
              API keys
              <InfoHint>
                Per-client credentials for <code>/rpc</code>, sent as <code>/rpc/&lt;key&gt;</code>,{' '}
                <code>X-API-Key</code> or <code>Authorization: Bearer</code>. Only a hash is stored: the key
                is shown once, at creation. A key sees only the transactions it submitted.
              </InfoHint>
            </div>
            {created && <SecretAlert created={created} onDismiss={() => setCreated(null)} />}
            <CreateKeyForm onCreate={createKey} onCreated={setCreated} onError={setMessage} />
            {data && (
              <KeysTable
                keys={data.keys ?? []}
                callers={data.callers ?? []}
                defaultRate={cfg?.rpc_key_rate ?? 0}
                onUpdate={updateKey}
                onDelete={deleteKey}
                onError={setMessage}
              />
            )}

            <div className="section-header mb-1 mt-3">
              Callers
              <InfoHint>
                Call statistics per caller since buildoor started: anonymous callers per client IP (or one
                shared row), each API key, the static token and WebUI logins.
              </InfoHint>
            </div>
            {data && <CallersTable callers={data.callers ?? []} />}

            <div className="section-header mb-1 mt-3">Read upstreams<InfoHint><UpstreamsHint /></InfoHint></div>
            {data && <UpstreamsTable upstreams={data.upstreams ?? []} />}
          </>
        )}
      </div>
    </div>
  );
};
