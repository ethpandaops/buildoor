import React from 'react';
import type { RevealTarget } from '../../types';
import { InfoHint } from '../InfoHint';

const VALIDATION_OPTIONS = ['gossip', 'consensus', 'consensus_and_equivocation'];

// The redaction marker the server puts into a target URL for readers without
// a login (config.RedactedMarker); such a URL cannot be saved back.
const REDACTED_MARKER = '***';

// One target row as typed: URLs one per line or comma separated, '' = unset.
export interface RevealTargetFormRow {
  urls: string;
  delayMs: string;
  validation: string;
  transform: string;
}

export function initRevealTargets(targets: RevealTarget[] | undefined): RevealTargetFormRow[] {
  return (targets ?? []).map((target) => ({
    urls: (target.urls ?? []).join('\n'),
    delayMs: target.delay_ms ? String(target.delay_ms) : '',
    validation: target.broadcast_validation ?? '',
    transform: target.transform ?? '',
  }));
}

// revealTargetsFromRows parses the rows into the plan's targets (undefined
// when there are none). The server validates the rest (URL shape, delay
// bound, jq).
export function revealTargetsFromRows(
  rows: RevealTargetFormRow[]
): { targets?: RevealTarget[]; error: string | null } {
  const targets: RevealTarget[] = [];

  for (const [i, row] of rows.entries()) {
    const urls = row.urls.split(/[\s,]+/).filter((url) => url !== '');
    if (urls.some((url) => url.includes(REDACTED_MARKER))) {
      return { error: `reveal target ${i + 1}: a URL was hidden from this reader; reload logged in before saving` };
    }

    const delay = row.delayMs.trim();
    const delayMs = delay === '' ? 0 : Number(delay);
    if (!Number.isInteger(delayMs) || delayMs < 0) {
      return { error: `reveal target ${i + 1}: delay must be a non-negative integer (ms)` };
    }

    targets.push({
      ...(urls.length > 0 ? { urls } : {}),
      ...(delayMs > 0 ? { delay_ms: delayMs } : {}),
      ...(row.transform.trim() ? { transform: row.transform.trim() } : {}),
      ...(row.validation ? { broadcast_validation: row.validation } : {}),
    });
  }

  return { targets: targets.length > 0 ? targets : undefined, error: null };
}

export const REVEAL_TARGETS_HINT: React.ReactNode = (
  <>
    Additional publications of the slot&apos;s envelope, each after the reveal succeeded. A target
    rebuilds the envelope under its own transform, re-signs it and submits it to its beacon nodes
    after its delay; each publication is recorded as its own reveal attempt.
    <dl>
      <dt>Beacon nodes</dt>
      <dd>beacon REST base URLs, one per line; empty = the primary beacon node</dd>
      <dt>Delay</dt>
      <dd>wait after the reveal, at most two slots</dd>
      <dt>Transform</dt>
      <dd>jq on this copy&apos;s envelope message; empty = the envelope as built, even when the reveal itself is transformed</dd>
    </dl>
  </>
);

// RevealTargetsForm edits the reveal targets of a custom reveal category.
export const RevealTargetsForm: React.FC<{
  rows: RevealTargetFormRow[];
  disabled: boolean;
  onChange: (next: RevealTargetFormRow[]) => void;
}> = ({ rows, disabled, onChange }) => {
  const update = (index: number, patch: Partial<RevealTargetFormRow>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));

  return (
    <div className="mt-2">
      <div className="d-flex align-items-center gap-2 mb-1">
        <span className="small fw-semibold">Reveal targets</span>
        <InfoHint>{REVEAL_TARGETS_HINT}</InfoHint>
        <button
          type="button"
          className="btn btn-sm btn-outline-secondary py-0 ms-auto"
          disabled={disabled}
          onClick={() => onChange([...rows, { urls: '', delayMs: '', validation: '', transform: '' }])}
        >
          + Add target
        </button>
      </div>

      {rows.length === 0 && <div className="small text-muted">No additional publications.</div>}

      {rows.map((row, i) => (
        <div key={i} className="border rounded p-2 mb-2">
          <div className="row g-2">
            <div className="col-12 col-lg-6">
              <label className="form-label small mb-0">Beacon nodes</label>
              <textarea
                className="form-control form-control-sm font-monospace"
                rows={2}
                placeholder="the primary beacon node"
                value={row.urls}
                disabled={disabled}
                onChange={(e) => update(i, { urls: e.target.value })}
              />
            </div>
            <div className="col-6 col-lg-3">
              <label className="form-label small mb-0">Delay</label>
              <div className="input-group input-group-sm">
                <input
                  type="number"
                  className="form-control"
                  placeholder="0"
                  value={row.delayMs}
                  disabled={disabled}
                  onChange={(e) => update(i, { delayMs: e.target.value })}
                />
                <span className="input-group-text">ms</span>
              </div>
            </div>
            <div className="col-6 col-lg-3">
              <label className="form-label small mb-0">Broadcast Validation</label>
              <select
                className="form-select form-select-sm"
                value={row.validation}
                disabled={disabled}
                onChange={(e) => update(i, { validation: e.target.value })}
              >
                <option value="">the reveal&apos;s</option>
                {VALIDATION_OPTIONS.map((opt) => (
                  <option key={opt} value={opt}>{opt}</option>
                ))}
              </select>
            </div>
            <div className="col-12">
              <label className="form-label small mb-0">Transform</label>
              <div className="d-flex gap-2 align-items-start">
                <textarea
                  className="form-control form-control-sm font-monospace"
                  rows={1}
                  placeholder="envelope as built"
                  value={row.transform}
                  disabled={disabled}
                  onChange={(e) => update(i, { transform: e.target.value })}
                />
                <button
                  type="button"
                  className="btn btn-sm btn-outline-danger"
                  disabled={disabled}
                  title="Remove this target"
                  onClick={() => onChange(rows.filter((_, j) => j !== i))}
                >
                  Remove
                </button>
              </div>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
};

// RevealTargetsSummary lists resolved targets read-only (frozen plans).
export const RevealTargetsSummary: React.FC<{ targets: RevealTarget[] }> = ({ targets }) => (
  <ul className="list-unstyled small mb-0">
    {targets.map((target, i) => (
      <li key={i} className="text-break">
        <span className="font-monospace">{target.urls?.length ? target.urls.join(', ') : 'primary node'}</span>
        {target.delay_ms ? ` · +${target.delay_ms} ms` : ''}
        {target.broadcast_validation ? ` · ${target.broadcast_validation}` : ''}
        {target.transform ? (
          <>
            {' · '}
            <code title={target.transform}>transformed</code>
          </>
        ) : null}
      </li>
    ))}
  </ul>
);
