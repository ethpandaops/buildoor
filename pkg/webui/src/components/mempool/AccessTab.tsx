import React, { useMemo, useState } from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { useEventStream } from '../../hooks/useEventStream';
import { useRPCAccess } from '../../hooks/useRPCAccess';
import { InfoHint } from '../InfoHint';
import { Switch } from '../ui/Switch';
import { Tag, type Tone } from '../ui/Tag';
import { FlowSettingsSummary } from '../settings/FlowSettingsSummary';
import { ConfirmButton } from './ConfirmButton';
import { CopyButton } from './CopyButton';
import { ErrorNotice, LockNotice } from './LockNotice';
import { formatRate, relativeTime } from './format';
import { usePageVisible } from './usePageVisible';
import type { CreateRPCKeyResponse, RPCCallerStats, RPCKey } from '../../types';

type RPCAccess = ReturnType<typeof useRPCAccess>;
type RPCAccessUpstreams = NonNullable<RPCAccess['data']>['upstreams'];

const UpstreamsHint: React.FC = () => (
  <>
    The endpoints the read passthrough (every call except <code>eth_sendRawTransaction</code>) is sent to.{' '}
    <strong>local</strong> is the EL behind <code>--el-rpc</code>; other entries are http(s)/ws(s) URLs. The
    lowest priority is tried first, endpoints of equal priority take turns (round robin), and a transport
    failure moves on to the next endpoint. JSON-RPC errors are answers, not failures.
  </>
);

// ---------------------------------------------------------------------------
// New key secret (shown once)
// ---------------------------------------------------------------------------

const SecretNotice: React.FC<{ created: CreateRPCKeyResponse; onDismiss: () => void }> = ({ created, onDismiss }) => {
  const url = `${window.location.origin}/rpc/${created.secret}`;
  const header = `X-API-Key: ${created.secret}`;
  const rows: Array<[string, string]> = [
    ['Key', created.secret],
    ['URL', url],
    ['Header', header],
  ];

  return (
    <div className="bd-mp-secret">
      <div className="d-flex align-items-center mb-1">
        <strong>API key &quot;{created.key.label}&quot; created.</strong>
        <button type="button" className="btn-close btn-sm ms-auto" aria-label="Dismiss" onClick={onDismiss}></button>
      </div>
      <div className="mb-2">
        Copy it now: the key is stored only as a hash and <strong>cannot be shown again</strong>.
      </div>
      {rows.map(([label, value]) => (
        <div className="bd-mp-secret-row" key={label}>
          <span className="bd-muted">{label}</span>
          <code className="text-break">{value}</code>
          <CopyButton value={value} />
        </div>
      ))}
      <div className="bd-muted mt-1">
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
  onUpdate: RPCAccess['updateKey'];
  onDelete: RPCAccess['deleteKey'];
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

  if (keys.length === 0) {
    return <div className="bd-muted small">No API keys yet.</div>;
  }

  return (
    <div className="table-responsive">
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
                      aria-label="Label"
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
                        aria-label="Rate limit"
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
                          className="form-control form-control-sm bd-mp-input-rate"
                          placeholder="calls/s"
                          aria-label="Calls per second"
                          value={edit.rate}
                          onChange={(e) => setEdit({ ...edit, rate: e.target.value })}
                        />
                      )}
                    </div>
                  ) : key.rate_limit === undefined ? (
                    <span className="bd-muted">default ({formatRate(defaultRate)})</span>
                  ) : (
                    <span>{formatRate(key.rate_limit)} <Tag tone="info">override</Tag></span>
                  )}
                </td>
                <td>
                  <Switch
                    checked={key.enabled}
                    label={`API key ${key.label}`}
                    busy={busy}
                    onChange={(next) => run(() => onUpdate(key.id, { enabled: next }))}
                  />
                </td>
                <td title={key.created_at}>{relativeTime(key.created_at)}</td>
                <td className="text-end">{(callsByKey[key.id] ?? 0).toLocaleString()}</td>
                <td className="text-end text-nowrap">
                  {editingRow ? (
                    <span className="d-inline-flex gap-1">
                      <button type="button" className="bd-btn bd-btn-sm bd-btn-primary" disabled={busy} onClick={() => saveEdit(key)}>Save</button>
                      <button type="button" className="bd-btn bd-btn-sm" onClick={() => setEditId(null)}>Cancel</button>
                    </span>
                  ) : (
                    <span className="d-inline-flex gap-1 align-items-center">
                      <button type="button" className="bd-btn bd-btn-sm bd-mp-icon-btn" title="Edit" aria-label="Edit" onClick={() => startEdit(key)}>
                        <i className="fas fa-pencil-alt"></i>
                      </button>
                      <ConfirmButton
                        className="bd-btn bd-btn-sm bd-btn-danger bd-mp-icon-btn"
                        title="Delete"
                        disabled={busy}
                        prompt={<>Delete &quot;{key.label}&quot;? Callers using it are refused from the next request on.</>}
                        confirmLabel="Delete"
                        onConfirm={async () => { await run(() => onDelete(key.id)); }}
                      >
                        <i className="fas fa-trash"></i>
                      </ConfirmButton>
                    </span>
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
  onCreate: RPCAccess['createKey'];
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
    <form className="bd-mp-create-key" onSubmit={submit}>
      <input
        type="text"
        className="form-control form-control-sm bd-mp-input-label"
        placeholder="label (e.g. spamoor-1)"
        aria-label="New key label"
        value={label}
        onChange={(e) => setLabel(e.target.value)}
      />
      <input
        type="number"
        min={0}
        className="form-control form-control-sm bd-mp-input-rate-wide"
        placeholder="rate (default)"
        aria-label="New key rate limit"
        value={rate}
        onChange={(e) => setRate(e.target.value)}
      />
      <InfoHint>
        Optional calls per second for the new key (0 = unlimited). Empty follows the key rate setting.
      </InfoHint>
      <button type="submit" className="bd-btn bd-btn-sm bd-btn-primary" disabled={busy || label.trim() === ''}>
        <i className="fas fa-key"></i>Create API key
      </button>
    </form>
  );
};

// ---------------------------------------------------------------------------
// Callers + upstreams
// ---------------------------------------------------------------------------

const KIND_TONES: Record<RPCCallerStats['kind'], Tone> = {
  anonymous: 'neutral',
  api_key: 'accent',
  static: 'info',
  login: 'ok',
};

const MethodSummary: React.FC<{ methods: Record<string, number> }> = ({ methods }) => {
  const sorted = Object.entries(methods ?? {}).sort((a, b) => b[1] - a[1]);
  if (sorted.length === 0) return <span className="bd-muted">—</span>;
  const all = sorted.map(([m, n]) => `${m}: ${n}`).join('\n');
  return (
    <span title={all}>
      {sorted.slice(0, 3).map(([m, n]) => `${m} ${n}`).join(', ')}
      {sorted.length > 3 && <span className="bd-muted"> +{sorted.length - 3} more</span>}
    </span>
  );
};

const CallersTable: React.FC<{ callers: RPCCallerStats[] }> = ({ callers }) => {
  if (callers.length === 0) {
    return <div className="bd-muted small">No /rpc calls yet.</div>;
  }
  const sorted = [...callers].sort((a, b) => new Date(b.last_seen).getTime() - new Date(a.last_seen).getTime());
  return (
    <div className="table-responsive">
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
              <td><Tag tone={KIND_TONES[c.kind] ?? 'neutral'}>{c.kind}</Tag></td>
              <td title={c.id}>
                {c.label ? <>{c.label} <span className="bd-muted">{c.id}</span></> : c.id}
              </td>
              <td>{formatRate(c.rate_limit)}</td>
              <td className="text-end">{c.calls.toLocaleString()}</td>
              <td className="text-end">{c.errors ? <span className="bd-mp-text-danger">{c.errors.toLocaleString()}</span> : 0}</td>
              <td className="text-end">{c.rate_limited ? <Tag tone="warn">{c.rate_limited.toLocaleString()}</Tag> : 0}</td>
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
    return <div className="bd-muted small">No upstreams.</div>;
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
                  ? <Tag tone="ok">available</Tag>
                  : <Tag tone="danger" title={u.error}>unavailable</Tag>}
                {u.error && <span className="bd-muted ms-1">{u.error}</span>}
              </td>
              <td className="text-end">{u.calls.toLocaleString()}</td>
              <td className="text-end">{u.failures ? <span className="bd-mp-text-danger">{u.failures.toLocaleString()}</span> : 0}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

const Section: React.FC<{ title: React.ReactNode; hint?: React.ReactNode; children: React.ReactNode }> = ({ title, hint, children }) => (
  <div className="card bd-mp-section">
    <div className="card-body">
      <h3 className="bd-card-title mb-2">
        {title}
        {hint && <InfoHint>{hint}</InfoHint>}
      </h3>
      {children}
    </div>
  </div>
);

// AccessTab manages who may call the /rpc ingress: API keys (secret shown
// once at creation), per-caller call stats and the read upstreams, next to
// a read-only summary of the access settings (edited in the settings
// drawer). Keys, callers and upstreams are operator-only; the access state
// is polled only while this tab is shown, the document is visible and the
// reader is logged in.
export const AccessTab: React.FC = () => {
  const { isLoggedIn } = useAuthContext();
  const { config } = useEventStream();
  const visible = usePageVisible();
  const { data, error, unauthorized, unavailable, createKey, updateKey, deleteKey } =
    useRPCAccess(visible && isLoggedIn);
  const [message, setMessage] = useState<string | null>(null);
  const [created, setCreated] = useState<CreateRPCKeyResponse | null>(null);

  const showData = isLoggedIn && !unauthorized;

  return (
    <div className="row g-3">
      <div className="col-12 col-xl-4 order-xl-last">
        <div className="card">
          <div className="card-body">
            <h3 className="bd-card-title mb-2">Access settings</h3>
            <FlowSettingsSummary flow="mempool" tab="access" />
          </div>
        </div>
      </div>
      <div className="col-12 col-xl-8 d-flex flex-column gap-3">
        <div className="bd-muted small">
          Transactions sent to <code>/rpc</code> are private: anonymous callers can never read the pool
          back, an API key only sees the transactions it submitted, and operators (the static
          token or a WebUI login) see all of them.
        </div>

        {!showData ? (
          <LockNotice>Log in to manage API keys and see per-caller stats and upstreams.</LockNotice>
        ) : unavailable ? (
          <div className="bd-notice">The /rpc ingress is not available (no --el-rpc).</div>
        ) : (
          <>
            <ErrorNotice message={error} />
            <ErrorNotice message={message} onDismiss={() => setMessage(null)} />

            <Section
              title="API keys"
              hint={
                <>
                  Per-client credentials for <code>/rpc</code>, sent as <code>/rpc/&lt;key&gt;</code>,{' '}
                  <code>X-API-Key</code> or <code>Authorization: Bearer</code>. Only a hash is stored: the key
                  is shown once, at creation. A key sees only the transactions it submitted.
                </>
              }
            >
              {created && <SecretNotice created={created} onDismiss={() => setCreated(null)} />}
              <CreateKeyForm onCreate={createKey} onCreated={setCreated} onError={setMessage} />
              {data && (
                <KeysTable
                  keys={data.keys ?? []}
                  callers={data.callers ?? []}
                  defaultRate={config?.txpool?.rpc_key_rate ?? 0}
                  onUpdate={updateKey}
                  onDelete={deleteKey}
                  onError={setMessage}
                />
              )}
            </Section>

            <Section
              title="Callers"
              hint={
                <>
                  Call statistics per caller since buildoor started: anonymous callers per client IP (or one
                  shared row), each API key, the static token and WebUI logins.
                </>
              }
            >
              {data && <CallersTable callers={data.callers ?? []} />}
            </Section>

            <Section title="Read upstreams" hint={<UpstreamsHint />}>
              {data && <UpstreamsTable upstreams={data.upstreams ?? []} />}
            </Section>
          </>
        )}
      </div>
    </div>
  );
};
