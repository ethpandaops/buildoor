import React from 'react';
import { InfoHint } from '../InfoHint';

export interface Column<T> {
  key: string;
  header: React.ReactNode;
  hint?: React.ReactNode;
  // Numbers align right (header, cells and hint callout).
  align?: 'start' | 'end';
  mono?: boolean;
  muted?: boolean;
  render: (row: T) => React.ReactNode;
}

interface RegistrationTableProps<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
}

// RegistrationTable is the one table layout of the registration tabs: sticky
// header inside a scroll box, monospace hashes, right-aligned numbers.
export function RegistrationTable<T>({ columns, rows, rowKey }: RegistrationTableProps<T>) {
  return (
    <div className="bd-reg-scroll">
      <table className="table table-sm mb-0 bd-reg-table">
        <thead>
          <tr>
            {columns.map((col) => (
              <th key={col.key} className={col.align === 'end' ? 'text-end' : undefined} scope="col">
                {col.header}
                {col.hint && <InfoHint alignEnd={col.align === 'end'}>{col.hint}</InfoHint>}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={rowKey(row)}>
              {columns.map((col) => (
                <td
                  key={col.key}
                  className={[
                    col.align === 'end' ? 'text-end' : '',
                    col.mono ? 'font-monospace' : '',
                    col.muted ? 'bd-muted' : '',
                  ]
                    .filter(Boolean)
                    .join(' ') || undefined}
                >
                  {col.render(row)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
