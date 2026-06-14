import React from 'react';
import { AuditLogEntry } from '../types';
import { Pagination } from './Pagination';

interface AuditLogTableProps {
  entries: AuditLogEntry[];
  total: number;
  offset: number;
  limit: number;
  loading: boolean;
  onPageChange: (offset: number) => void;
  onRefresh: () => void;
}

const formatTimestamp = (ts: number) => {
  const date = new Date(ts);
  const diff = Date.now() - ts;

  if (diff < 60000) return 'Just now';
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
  return date.toLocaleString();
};

const resultBadge = (result: string) => {
  if (result === 'ok') {
    return <span className="badge bg-success">ok</span>;
  }
  if (result.startsWith('error')) {
    return <span className="badge bg-danger" title={result}>error</span>;
  }
  return <span className="badge bg-secondary">{result || '-'}</span>;
};

export const AuditLogTable: React.FC<AuditLogTableProps> = ({
  entries,
  total,
  offset,
  limit,
  loading,
  onPageChange,
  onRefresh,
}) => {
  return (
    <div className="card">
      <div className="card-header d-flex align-items-center justify-content-between">
        <h5 className="mb-0">
          Audit Log <span className="badge bg-primary ms-2">{total}</span>
        </h5>
        <button className="btn btn-sm btn-outline-secondary" onClick={onRefresh} disabled={loading}>
          <i className="fas fa-rotate-right me-1"></i> Refresh
        </button>
      </div>
      <div className="card-body p-0 position-relative">
        {loading && (
          <div className="position-absolute top-0 start-0 w-100 h-100 d-flex align-items-center justify-content-center bg-body bg-opacity-75" style={{ zIndex: 10 }}>
            <div className="spinner-border text-primary" role="status">
              <span className="visually-hidden">Loading...</span>
            </div>
          </div>
        )}

        {!loading && entries.length === 0 ? (
          <div className="text-center py-5 text-muted">
            No audit entries yet
            <div className="small mt-1">Authenticated changes are recorded here when a state-db (<code>--state-db</code>) is configured.</div>
          </div>
        ) : (
          <table className="table table-sm table-hover mb-0">
            <thead className="sticky-top bids-won-header">
              <tr>
                <th>Time</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Detail</th>
                <th>Result</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id}>
                  <td className="text-muted text-nowrap">
                    <small title={new Date(e.timestamp).toLocaleString()}>{formatTimestamp(e.timestamp)}</small>
                  </td>
                  <td className="text-nowrap">
                    {e.actor}
                    {e.remote_addr && <small className="text-muted ms-1">({e.remote_addr})</small>}
                  </td>
                  <td className="font-monospace text-nowrap">{e.action}</td>
                  <td className="font-monospace text-truncate" style={{ maxWidth: '420px' }} title={e.detail}>
                    <small>{e.detail}</small>
                  </td>
                  <td>{resultBadge(e.result)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        <Pagination
          total={total}
          offset={offset}
          limit={limit}
          onPageChange={onPageChange}
        />
      </div>
    </div>
  );
};
