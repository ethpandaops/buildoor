import React from 'react';
import type { InstanceStatus, OverviewHost, OverviewResponse } from './types';

const GWEI_PER_ETH = 1_000_000_000;

interface Props {
  hosts: OverviewHost[];
  statuses: Record<number, InstanceStatus>;
}

function gweiToEth(gwei?: number): string {
  if (!gwei || gwei === 0) return '–';
  const eth = gwei / GWEI_PER_ETH;
  if (eth >= 100) return eth.toFixed(2);
  if (eth >= 1) return eth.toFixed(4);
  return eth.toFixed(6);
}

function weiToEthString(wei?: string): string {
  if (!wei) return '–';
  try {
    const big = BigInt(wei);
    const eth = Number(big) / 1e18;
    if (!isFinite(eth)) return '–';
    if (eth >= 100) return eth.toFixed(2);
    if (eth >= 1) return eth.toFixed(4);
    return eth.toFixed(6);
  } catch {
    return '–';
  }
}

function shortPubkey(pk?: string): string {
  if (!pk) return '–';
  if (pk.length <= 14) return pk;
  return `${pk.slice(0, 8)}…${pk.slice(-6)}`;
}

function formatRelative(ts?: number): string {
  if (!ts) return '';
  const delta = Math.max(0, Date.now() - ts);
  if (delta < 2000) return 'just now';
  if (delta < 60_000) return `${Math.round(delta / 1000)}s ago`;
  if (delta < 3_600_000) return `${Math.round(delta / 60_000)}m ago`;
  return `${Math.round(delta / 3_600_000)}h ago`;
}

function ServiceBadges({ data }: { data: OverviewResponse }) {
  const { services } = data;
  const badges: Array<{ label: string; cls: string; title?: string }> = [];

  if (services.epbs_available) {
    if (services.epbs_enabled) {
      badges.push({
        label: 'ePBS',
        cls: 'bg-success',
        title: services.epbs_registration_state
          ? `ePBS enabled (${services.epbs_registration_state})`
          : 'ePBS enabled',
      });
    } else {
      badges.push({ label: 'ePBS', cls: 'bg-secondary', title: 'ePBS available but disabled' });
    }
  }

  if (services.builder_api_available) {
    badges.push({
      label: 'BuilderAPI',
      cls: services.builder_api_enabled ? 'bg-success' : 'bg-secondary',
      title: services.builder_api_enabled ? 'Builder API enabled' : 'Builder API disabled',
    });
  }

  if (services.lifecycle_available) {
    badges.push({
      label: 'Lifecycle',
      cls: services.lifecycle_enabled ? 'bg-info' : 'bg-secondary',
      title: services.lifecycle_enabled ? 'Lifecycle management enabled' : 'Lifecycle disabled',
    });
  }

  if (badges.length === 0) {
    return <span className="text-muted">–</span>;
  }

  return (
    <div className="d-flex flex-wrap gap-1">
      {badges.map((b) => (
        <span key={b.label} className={`badge ${b.cls}`} title={b.title}>
          {b.label}
        </span>
      ))}
    </div>
  );
}

function ELClientCell({ data }: { data: OverviewResponse }) {
  const el = data.el_client;
  if (!el || (!el.name && !el.code)) {
    return <span className="text-muted">unknown</span>;
  }
  const name = el.name || el.code || '?';
  return (
    <div>
      <div className="fw-semibold text-capitalize">{name}</div>
      {el.version && <div className="small text-muted">{el.version}</div>}
    </div>
  );
}

function BalanceCell({ data }: { data: OverviewResponse }) {
  const cl = gweiToEth(data.balances.cl_balance_gwei);
  const wallet = weiToEthString(data.balances.wallet_balance_wei);
  return (
    <div className="small">
      <div>
        <span className="text-muted me-1">CL:</span>
        <span className="fw-semibold">{cl}</span>
        <span className="text-muted ms-1">ETH</span>
      </div>
      {data.balances.wallet_address && (
        <div>
          <span className="text-muted me-1">Wallet:</span>
          <span className="fw-semibold">{wallet}</span>
          <span className="text-muted ms-1">ETH</span>
        </div>
      )}
    </div>
  );
}

function StatsCell({ data }: { data: OverviewResponse }) {
  const s = data.stats;
  return (
    <div className="small">
      <div>
        <span className="text-muted">built </span>
        <span className="fw-semibold">{s.slots_built}</span>
        <span className="text-muted"> · won </span>
        <span className="fw-semibold">{s.bids_won + s.builder_api_blocks_published}</span>
      </div>
      <div className="text-muted">
        bids {s.bids_submitted} · headers {s.builder_api_headers_requested}
      </div>
    </div>
  );
}

function StatusCell({ status }: { status: InstanceStatus }) {
  if (status.state === 'loading') {
    return <span className="badge bg-secondary">loading…</span>;
  }
  if (status.state === 'error') {
    return (
      <span className="badge bg-danger" title={status.error}>
        offline
      </span>
    );
  }
  return <span className="badge bg-success">online</span>;
}

export const InstanceTable: React.FC<Props> = ({ hosts, statuses }) => {
  return (
    <div className="card">
      <div className="card-header d-flex justify-content-between align-items-center">
        <h5 className="mb-0">Builder Instances</h5>
        <span className="text-muted small">{hosts.length} configured · refreshes every 5s</span>
      </div>
      <div className="table-responsive">
        <table className="table table-sm table-hover align-middle mb-0">
          <thead className="table-light">
            <tr>
              <th>Instance</th>
              <th>Status</th>
              <th>Mode</th>
              <th>EL Client</th>
              <th>Slot</th>
              <th>Builder</th>
              <th>Balance</th>
              <th>Stats</th>
              <th className="text-end">Updated</th>
            </tr>
          </thead>
          <tbody>
            {hosts.map((host) => {
              const status = statuses[host.id] ?? { state: 'loading' as const };
              const data = status.state === 'online' ? status.data : null;
              const lastUpdated =
                status.state === 'online' || status.state === 'error' ? status.lastUpdated : undefined;

              return (
                <tr key={host.id}>
                  <td>
                    <div className="fw-semibold">{host.label}</div>
                    <a
                      className="small text-muted"
                      href={host.url}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      open ↗
                    </a>
                  </td>
                  <td>
                    <StatusCell status={status} />
                  </td>
                  <td>{data ? <ServiceBadges data={data} /> : <span className="text-muted">–</span>}</td>
                  <td>{data ? <ELClientCell data={data} /> : <span className="text-muted">–</span>}</td>
                  <td className="font-monospace">{data ? data.current_slot : '–'}</td>
                  <td>
                    {data ? (
                      <span className="font-monospace small" title={data.builder_pubkey}>
                        {shortPubkey(data.builder_pubkey)}
                      </span>
                    ) : (
                      <span className="text-muted">–</span>
                    )}
                  </td>
                  <td>{data ? <BalanceCell data={data} /> : <span className="text-muted">–</span>}</td>
                  <td>{data ? <StatsCell data={data} /> : <span className="text-muted">–</span>}</td>
                  <td className="text-end small text-muted">
                    {status.state === 'error' ? (
                      <span className="text-danger" title={status.error}>
                        {formatRelative(lastUpdated)}
                      </span>
                    ) : (
                      formatRelative(lastUpdated)
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
};
