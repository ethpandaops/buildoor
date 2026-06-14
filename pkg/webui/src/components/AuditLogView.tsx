import React, { useState } from 'react';
import { AuditLogTable } from './AuditLogTable';
import { useAuditLog } from '../hooks/useAuditLog';
import { useAuthContext } from '../context/AuthContext';

// AuditLogContent holds the data hook; it is only mounted for authenticated
// users so the privileged endpoint is never queried when not logged in.
const AuditLogContent: React.FC = () => {
  const [offset, setOffset] = useState(0);
  const limit = 50;
  const { entries, total, loading, refetch } = useAuditLog(offset, limit);

  return (
    <AuditLogTable
      entries={entries}
      total={total}
      offset={offset}
      limit={limit}
      loading={loading}
      onPageChange={setOffset}
      onRefresh={refetch}
    />
  );
};

export const AuditLogView: React.FC = () => {
  const { isLoggedIn } = useAuthContext();

  return (
    <div className="container-fluid mt-2">
      {isLoggedIn ? (
        <AuditLogContent />
      ) : (
        <div className="card">
          <div className="card-body text-center py-5 text-muted">
            <i className="fas fa-lock mb-2 d-block fs-4"></i>
            You must be logged in to view the audit log.
          </div>
        </div>
      )}
    </div>
  );
};
