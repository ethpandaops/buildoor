import React from 'react';
import type { InstanceStatus, OverviewHost, OverviewResponse } from './types';
import { CopyableHash } from '../components/CopyableHash';

const GWEI_PER_ETH = 1_000_000_000;

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
    badges.push({
      label: 'ePBS',
      cls: services.epbs_enabled ? 'bg-success' : 'bg-secondary',
      title: services.epbs_registration_state
        ? `ePBS ${services.epbs_enabled ? 'enabled' : 'available'} (${services.epbs_registration_state})`
        : services.epbs_enabled
          ? 'ePBS enabled'
          : 'ePBS available but disabled',
    });
  }

  if (services.builder_api_available) {
    badges.push({
      label: 'Builder API',
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
    return <span className="text-muted small">no services</span>;
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

function StatusPill({ status }: { status: InstanceStatus }) {
  if (status.state === 'loading') {
    return <span className="badge bg-secondary">loading</span>;
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

interface RowProps {
  label: string;
  children: React.ReactNode;
  mono?: boolean;
  title?: string;
}

const Row: React.FC<RowProps> = ({ label, children, mono, title }) => (
  <>
    <dt className="col-4 text-secondary fw-normal">{label}</dt>
    <dd className={`col-8 mb-1 ${mono ? 'font-monospace' : ''}`} title={title}>
      {children}
    </dd>
  </>
);

interface Props {
  host: OverviewHost;
  status: InstanceStatus;
}

export const InstanceCard: React.FC<Props> = ({ host, status }) => {
  const data = status.state === 'online' ? status.data : null;
  const lastUpdated =
    status.state === 'online' || status.state === 'error' ? status.lastUpdated : undefined;

  return (
    <div className="card h-100 shadow-sm">
      <a
        className="card-header instance-card-header d-flex align-items-center gap-2 flex-wrap text-decoration-none text-body"
        href={host.url}
        target="_blank"
        rel="noopener noreferrer"
        title={host.url}
      >
        <StatusPill status={status} />
        <h6 className="mb-0 flex-grow-1 text-truncate">{host.label}</h6>
        <span className="small text-secondary">
          open <i className="fas fa-external-link-alt ms-1"></i>
        </span>
      </a>

      <div className="card-body">
        {!data && status.state === 'loading' && (
          <div className="text-muted small text-center py-3">Loading…</div>
        )}

        {!data && status.state === 'error' && (
          <div className="alert alert-danger small mb-0">
            <strong>Offline.</strong> {status.error || 'request failed'}
          </div>
        )}

        {data && (
          <>
            <div className="mb-3">
              <ServiceBadges data={data} />
            </div>

            <div className="row g-md-4">
              <div className="col-12 col-md-6">
                <dl className="row mb-0 small">
                  <Row label="Last Built" mono>
                    {data.current_slot > 0 ? (
                      `slot ${data.current_slot}`
                    ) : (
                      <span className="text-muted">none yet</span>
                    )}
                  </Row>

                  <Row label="EL Client">
                    {data.el_client && (data.el_client.name || data.el_client.code) ? (
                      <>
                        <span className="text-capitalize fw-semibold">
                          {data.el_client.name || data.el_client.code}
                        </span>
                        {data.el_client.version && (
                          <span className="text-muted ms-2">{data.el_client.version}</span>
                        )}
                      </>
                    ) : (
                      <span className="text-muted">unknown</span>
                    )}
                  </Row>

                  <Row label="Builder" mono>
                    {data.builder_pubkey ? (
                      <>
                        <CopyableHash value={data.builder_pubkey} chars={8} />
                        {typeof data.builder_index === 'number' && (
                          <span className="text-muted ms-2">idx {data.builder_index}</span>
                        )}
                        <span
                          className={`ms-2 badge ${
                            data.is_registered
                              ? 'bg-success-subtle text-success-emphasis'
                              : 'bg-warning-subtle text-warning-emphasis'
                          }`}
                        >
                          {data.is_registered ? 'registered' : 'unregistered'}
                        </span>
                      </>
                    ) : (
                      <span className="text-muted">–</span>
                    )}
                  </Row>

                  {data.version && (
                    <Row label="Version">
                      <span className="text-muted">{data.version}</span>
                    </Row>
                  )}
                </dl>
              </div>

              <div className="col-12 col-md-6">
                <dl className="row mb-0 small">
                  <Row label="CL Balance">
                    <span className="fw-semibold">{gweiToEth(data.balances.cl_balance_gwei)}</span>
                    <span className="text-muted ms-1">ETH</span>
                  </Row>

                  {typeof data.balances.pending_payments_gwei === 'number' &&
                    data.balances.pending_payments_gwei > 0 && (
                      <Row label="Pending">
                        <span className="text-warning">
                          −{gweiToEth(data.balances.pending_payments_gwei)}
                        </span>
                        <span className="text-muted ms-1">ETH</span>
                      </Row>
                    )}

                  {typeof data.balances.pending_payments_gwei === 'number' &&
                    data.balances.pending_payments_gwei > 0 &&
                    typeof data.balances.effective_balance_gwei === 'number' && (
                      <Row label="Effective">
                        <span className="fw-semibold text-success">
                          {gweiToEth(data.balances.effective_balance_gwei)}
                        </span>
                        <span className="text-muted ms-1">ETH</span>
                      </Row>
                    )}

                  {data.balances.wallet_address && (
                    <>
                      <Row label="Wallet" mono>
                        <CopyableHash value={data.balances.wallet_address} chars={6} />
                      </Row>
                      <Row label="Wallet Balance">
                        <span className="fw-semibold">
                          {weiToEthString(data.balances.wallet_balance_wei)}
                        </span>
                        <span className="text-muted ms-1">ETH</span>
                      </Row>
                    </>
                  )}

                  <Row label="Build Stats">
                    <span className="text-muted me-1">built</span>
                    <span className="fw-semibold">{data.stats.slots_built}</span>
                    <span className="text-muted mx-1">·</span>
                    <span className="text-muted me-1">won</span>
                    <span className="fw-semibold">
                      {data.stats.bids_won + data.stats.builder_api_blocks_published}
                    </span>
                    <span className="text-muted mx-1">·</span>
                    <span className="text-muted me-1">bids</span>
                    <span className="fw-semibold">{data.stats.bids_submitted}</span>
                  </Row>

                  {data.services.builder_api_available && (
                    <Row label="Builder API">
                      <span className="text-muted me-1">hdrs</span>
                      <span className="fw-semibold">{data.stats.builder_api_headers_requested}</span>
                      <span className="text-muted mx-1">·</span>
                      <span className="text-muted me-1">blocks</span>
                      <span className="fw-semibold">{data.stats.builder_api_blocks_published}</span>
                      <span className="text-muted mx-1">·</span>
                      <span className="text-muted me-1">vals</span>
                      <span className="fw-semibold">
                        {data.stats.builder_api_registered_validators}
                      </span>
                    </Row>
                  )}
                </dl>
              </div>
            </div>
          </>
        )}
      </div>

      <div className="card-footer text-muted small d-flex justify-content-between">
        <span>
          {status.state === 'error' ? (
            <span className="text-danger">request failed</span>
          ) : status.state === 'online' ? (
            <span>updated {formatRelative(lastUpdated)}</span>
          ) : (
            <span>waiting…</span>
          )}
        </span>
        <a className="text-muted text-truncate ms-2" href={host.url} title={host.url}>
          {host.url}
        </a>
      </div>
    </div>
  );
};
