import React from 'react';
import type { PolledList } from '../../hooks/usePolledList';
import type { SearchPager } from '../../hooks/useSearchPager';
import { InfoHint } from '../InfoHint';
import { RegistrationTable, type Column } from './RegistrationTable';
import { UpdatedAgo } from './UpdatedAgo';

interface RegistrationListCardProps<T> {
  title: string;
  hint?: React.ReactNode;
  list: PolledList<T>;
  pager: SearchPager<T>;
  searchPlaceholder: string;
  // Extra toolbar controls between the search box and the refresh status.
  filters?: React.ReactNode;
  columns: Column<T>[];
  rowKey: (row: T) => string;
  // Shown when the endpoint answers 404 (backing service not running).
  notEnabledText: React.ReactNode;
  emptyText: string;
  noMatchText: string;
}

const StateRow: React.FC<{ icon: string; tone?: 'danger'; children: React.ReactNode }> = ({
  icon,
  tone,
  children,
}) => (
  <div className={`bd-reg-state${tone === 'danger' ? ' bd-reg-state-danger' : ''}`} role={tone ? 'alert' : undefined}>
    <i className={`fas ${icon}`} aria-hidden="true"></i>
    <div>{children}</div>
  </div>
);

// RegistrationListCard is the shared frame of the registration tabs: toolbar
// (search, filters, refresh status), the table, and an a–b of N pager, with
// the same loading, empty, not-enabled and error states on every tab. A failed
// refresh with data on screen keeps the last list and shows the error above it.
export function RegistrationListCard<T>({
  title,
  hint,
  list,
  pager,
  searchPlaceholder,
  filters,
  columns,
  rowKey,
  notEnabledText,
  emptyText,
  noMatchText,
}: RegistrationListCardProps<T>) {
  const total = pager.filtered.length;
  const first = total === 0 ? 0 : pager.offset + 1;
  const last = Math.min(pager.offset + pager.limit, total);
  const hasRows = list.items.length > 0;

  let body: React.ReactNode;
  if (list.loading) {
    body = (
      <StateRow icon="fa-spinner fa-spin">Loading…</StateRow>
    );
  } else if (list.notEnabled) {
    body = <StateRow icon="fa-power-off">{notEnabledText}</StateRow>;
  } else if (list.error && !hasRows) {
    body = (
      <StateRow icon="fa-triangle-exclamation" tone="danger">
        <div>{list.error}</div>
        <button type="button" className="bd-btn bd-btn-sm mt-2" onClick={list.refetch}>
          Retry
        </button>
      </StateRow>
    );
  } else if (!hasRows) {
    body = <StateRow icon="fa-inbox">{emptyText}</StateRow>;
  } else if (total === 0) {
    body = <StateRow icon="fa-magnifying-glass">{noMatchText}</StateRow>;
  } else {
    body = <RegistrationTable columns={columns} rows={pager.page} rowKey={rowKey} />;
  }

  return (
    <div className="card bd-reg-card">
      <div className="bd-reg-head">
        <h2 className="bd-card-title">
          {title}
          {hint && <InfoHint>{hint}</InfoHint>}
        </h2>
      </div>
      <div className="bd-reg-toolbar">
        <div className="bd-reg-search">
          <i className="fas fa-magnifying-glass" aria-hidden="true"></i>
          <input
            type="search"
            className="form-control form-control-sm"
            placeholder={searchPlaceholder}
            aria-label={searchPlaceholder}
            value={pager.search}
            onChange={(e) => pager.setSearch(e.target.value)}
            disabled={!hasRows}
          />
        </div>
        {filters}
        <div className="bd-reg-status">
          <UpdatedAgo updatedAt={list.updatedAt} intervalMs={list.intervalMs} />
        </div>
      </div>
      {list.error && hasRows && (
        <div className="bd-reg-banner" role="alert">
          <i className="fas fa-triangle-exclamation" aria-hidden="true"></i>
          <span>{list.error} — showing the last loaded list.</span>
          <button type="button" className="bd-btn bd-btn-sm ms-auto" onClick={list.refetch}>
            Retry
          </button>
        </div>
      )}
      {body}
      {hasRows && total > 0 && (
        <div className="bd-reg-footer">
          <span className="bd-muted small">
            {first.toLocaleString()}–{last.toLocaleString()} of {total.toLocaleString()}
            {total !== list.items.length && ` (filtered from ${list.items.length.toLocaleString()})`}
          </span>
          <div className="d-flex gap-2">
            <button
              type="button"
              className="bd-btn bd-btn-sm"
              disabled={pager.offset === 0}
              onClick={() => pager.setOffset(Math.max(0, pager.offset - pager.limit))}
            >
              <i className="fas fa-chevron-left" aria-hidden="true"></i> Previous
            </button>
            <button
              type="button"
              className="bd-btn bd-btn-sm"
              disabled={pager.offset + pager.limit >= total}
              onClick={() => pager.setOffset(pager.offset + pager.limit)}
            >
              Next <i className="fas fa-chevron-right" aria-hidden="true"></i>
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
