import React, { useEffect, useState } from 'react';
import { parseUpstreams, serializeUpstreams, type UpstreamRow } from '../../settings/fields';
import type { SettingField } from '../../settings/types';
import type { FormValue } from '../../settings/values';

interface SettingInputProps {
  id: string;
  field: SettingField;
  value: FormValue;
  disabled: boolean;
  invalid: boolean;
  onChange: (value: FormValue) => void;
}

// SettingInput renders the editor matching a field's kind. Inputs hold the
// raw text the user typed (see settings/values.ts) so partial input survives
// re-renders; conversion and validation happen in the drawer.
export const SettingInput: React.FC<SettingInputProps> = ({ id, field, value, disabled, invalid, onChange }) => {
  const invalidClass = invalid ? ' is-invalid' : '';

  switch (field.kind) {
    case 'bool':
      return (
        <div className="form-check form-switch m-0">
          <input
            id={id}
            className="form-check-input"
            type="checkbox"
            role="switch"
            checked={value === true}
            disabled={disabled}
            onChange={(e) => onChange(e.target.checked)}
          />
          <label className="form-check-label sd-bool-label" htmlFor={id}>
            {value === true ? field.boolLabels?.on ?? 'on' : field.boolLabels?.off ?? 'off'}
          </label>
        </div>
      );

    case 'select':
      return (
        <select
          id={id}
          className={`form-select form-select-sm${invalidClass}`}
          value={String(value)}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
        >
          {!field.options?.some((o) => o.value === String(value)) && (
            <option value={String(value)}>{String(value) || '—'}</option>
          )}
          {field.options?.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      );

    case 'upstreams':
      return <UpstreamsEditor id={id} value={String(value)} disabled={disabled} onChange={onChange} />;

    default:
      return (
        <div className={`input-group input-group-sm${invalidClass}`}>
          <input
            id={id}
            className={`form-control font-monospace${invalidClass}`}
            type="text"
            inputMode={field.kind === 'number' ? 'decimal' : undefined}
            value={String(value)}
            placeholder={field.placeholder ?? (field.zeroLabel && field.kind === 'number' ? `0 = ${field.zeroLabel}` : undefined)}
            disabled={disabled}
            onChange={(e) => onChange(e.target.value)}
          />
          {field.unit && <span className="input-group-text">{field.unit}</span>}
        </div>
      );
  }
};

interface UpstreamsEditorProps {
  id: string;
  value: string;
  disabled: boolean;
  onChange: (value: string) => void;
}

// UpstreamsEditor edits txpool.rpc_upstreams as rows of target + priority
// (serialized as "<target>@<priority>,…").
const UpstreamsEditor: React.FC<UpstreamsEditorProps> = ({ id, value, disabled, onChange }) => {
  // Rows live locally so a freshly added, still empty row is kept; the value
  // only carries complete entries. An outside change (discard, remote
  // update) replaces the rows.
  const [rows, setRows] = useState<UpstreamRow[]>(() => parseUpstreams(value));
  useEffect(() => {
    setRows((current) => (serializeUpstreams(current) === value ? current : parseUpstreams(value)));
  }, [value]);

  const update = (next: UpstreamRow[]) => {
    setRows(next);
    onChange(serializeUpstreams(next));
  };
  const setRow = (index: number, patch: Partial<UpstreamRow>) =>
    update(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  return (
    <div className="sd-upstreams" id={id}>
      {rows.map((row, index) => (
        <div className="sd-upstream-row" key={index}>
          <input
            className="form-control form-control-sm font-monospace"
            aria-label={`Upstream ${index + 1} target`}
            value={row.target}
            placeholder="local or https://…"
            disabled={disabled}
            onChange={(e) => setRow(index, { target: e.target.value })}
          />
          <input
            className="form-control form-control-sm font-monospace sd-upstream-prio"
            aria-label={`Upstream ${index + 1} priority`}
            value={row.priority}
            inputMode="numeric"
            disabled={disabled}
            onChange={(e) => setRow(index, { priority: e.target.value })}
          />
          <button
            type="button"
            className="bd-btn bd-btn-sm"
            aria-label={`Remove upstream ${index + 1}`}
            disabled={disabled}
            onClick={() => update(rows.filter((_, i) => i !== index))}
          >
            <i className="fas fa-xmark" aria-hidden="true"></i>
          </button>
        </div>
      ))}
      <div className="d-flex gap-2">
        <button
          type="button"
          className="bd-btn bd-btn-sm"
          disabled={disabled}
          onClick={() => update([...rows, { target: '', priority: String(rows.length) }])}
        >
          <i className="fas fa-plus" aria-hidden="true"></i>Add upstream
        </button>
        {!rows.some((row) => row.target === 'local') && (
          <button
            type="button"
            className="bd-btn bd-btn-sm"
            disabled={disabled}
            onClick={() => update([...rows, { target: 'local', priority: '0' }])}
          >
            Add local EL
          </button>
        )}
      </div>
    </div>
  );
};
