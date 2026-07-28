import React, { useState } from 'react';
import type { SlotRule } from '../../types';
import type { SaveRulesResult } from '../../hooks/useActionPlanRules';
import { RuleEditModal } from './RuleEditModal';

// summarizeCategories renders the same chips the grid uses, so a rule reads
// like the cells it will produce.
const CategoryChips: React.FC<{ rule: SlotRule }> = ({ rule }) => {
  const chip = (label: string, mode?: 'custom' | 'disabled', title?: string) => (
    <span
      key={label}
      className={`ap-chip ${mode === 'disabled' ? 'ap-chip-disabled' : 'ap-chip-custom'}`}
      title={title}
    >
      {label}
    </span>
  );

  const t = rule.transforms;
  const hasTransform = !!(t && (t.payload || t.bid || t.envelope));

  return (
    <span className="ap-chips">
      {rule.bid && chip('B', rule.bid.mode, `bid: ${rule.bid.mode}`)}
      {rule.builder_api && chip('A', rule.builder_api.mode, `builder api: ${rule.builder_api.mode}`)}
      {rule.reveal && chip('R', rule.reveal.mode, `reveal: ${rule.reveal.mode}`)}
      {rule.build?.reorg_parent_payload && (
        <span className="ap-chip ap-chip-reorg" title="Build on n-2 payload">
          P
        </span>
      )}
      {hasTransform && (
        <span className="ap-chip ap-chip-transform" title="jq transform">
          jq
        </span>
      )}
    </span>
  );
};

const epochWindow = (rule: SlotRule): string => {
  if (rule.from_epoch === undefined && rule.to_epoch === undefined) return 'every epoch';
  if (rule.to_epoch === undefined) return `from epoch ${rule.from_epoch}`;
  if (rule.from_epoch === undefined) return `until epoch ${rule.to_epoch}`;

  return `epochs ${rule.from_epoch}–${rule.to_epoch}`;
};

interface RulesPanelProps {
  rules: SlotRule[];
  slotsPerEpoch: number;
  canEdit: boolean;
  loading: boolean;
  error: string | null;
  saveRules: (rules: SlotRule[]) => Promise<SaveRulesResult>;
}

/**
 * Lists the recurring rules and edits them through the atomic replace-all
 * endpoint: every mutation sends the full authoritative set.
 */
export const RulesPanel: React.FC<RulesPanelProps> = ({
  rules,
  slotsPerEpoch,
  canEdit,
  loading,
  error,
  saveRules,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState<{ rule: SlotRule | null } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  const commit = async (next: SlotRule[], onDone?: () => void) => {
    setSaving(true);
    setSaveError(null);

    const result = await saveRules(next);

    setSaving(false);

    if (!result.ok) {
      setSaveError(result.error || 'Failed to save rules');
      return;
    }

    onDone?.();
  };

  // Upsert by id. The modal rejects a new rule reusing an existing id, so this
  // only ever replaces the rule the operator opened for editing.
  const handleSave = (rule: SlotRule) => {
    const next = rules.some((r) => r.id === rule.id)
      ? rules.map((r) => (r.id === rule.id ? rule : r))
      : [...rules, rule];

    commit(next, () => setEditing(null));
  };

  const handleToggle = (rule: SlotRule) =>
    commit(rules.map((r) => (r.id === rule.id ? { ...r, enabled: !r.enabled } : r)));

  const handleDelete = (rule: SlotRule) =>
    commit(rules.filter((r) => r.id !== rule.id));

  const activeCount = rules.filter((r) => r.enabled).length;

  return (
    <div className="ap-rules mb-2">
      <div className="d-flex flex-wrap align-items-center gap-2">
        <button
          type="button"
          className="btn btn-sm btn-outline-secondary"
          onClick={() => setExpanded((prev) => !prev)}
        >
          <i className={`fas fa-chevron-${expanded ? 'down' : 'right'} me-1`}></i>
          Recurring Rules
          {rules.length > 0 && (
            <span className="badge bg-secondary ms-2">
              {activeCount}/{rules.length} active
            </span>
          )}
        </button>
        {loading && <span className="spinner-border spinner-border-sm text-primary"></span>}
        {expanded && canEdit && (
          <button
            type="button"
            className="btn btn-sm btn-primary ms-auto"
            disabled={saving}
            onClick={() => {
              setSaveError(null);
              setEditing({ rule: null });
            }}
          >
            <i className="fas fa-plus me-1"></i>New Rule
          </button>
        )}
      </div>

      {expanded && (
        <div className="mt-2">
          {(error || saveError) && (
            <div className="alert alert-danger small py-2">{error || saveError}</div>
          )}

          {rules.length === 0 ? (
            <div className="text-muted small">
              No recurring rules. A rule repeats a plan on the same slot index of every epoch — for
              example win slot {Math.max(0, slotsPerEpoch - 1)} and withhold its payload for a whole
              devnet run. Explicit per-slot plans always win over rules.
            </div>
          ) : (
            <div className="table-responsive">
              <table className="table table-sm align-middle mb-0">
                <thead>
                  <tr>
                    <th>Rule</th>
                    <th>Slots in epoch</th>
                    <th>Window</th>
                    <th>Plan</th>
                    <th className="text-end">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {rules.map((rule) => (
                    <tr key={rule.id} className={rule.enabled ? '' : 'text-muted'}>
                      <td>
                        <div className="d-flex align-items-center gap-2">
                          <span className={`badge ${rule.enabled ? 'bg-success' : 'bg-secondary'}`}>
                            {rule.enabled ? 'on' : 'off'}
                          </span>
                          <div>
                            <div className="fw-semibold">{rule.id}</div>
                            {rule.description && (
                              <div className="small text-muted">{rule.description}</div>
                            )}
                          </div>
                        </div>
                      </td>
                      <td className="font-monospace small">{rule.slots_in_epoch.join(', ')}</td>
                      <td className="small">{epochWindow(rule)}</td>
                      <td>
                        <CategoryChips rule={rule} />
                      </td>
                      <td className="text-end">
                        <div className="btn-group btn-group-sm">
                          <button
                            type="button"
                            className="btn btn-outline-secondary"
                            disabled={!canEdit || saving}
                            title={rule.enabled ? 'Disable rule' : 'Enable rule'}
                            onClick={() => handleToggle(rule)}
                          >
                            <i className={`fas fa-${rule.enabled ? 'pause' : 'play'}`}></i>
                          </button>
                          <button
                            type="button"
                            className="btn btn-outline-secondary"
                            disabled={!canEdit || saving}
                            title="Edit rule"
                            onClick={() => {
                              setSaveError(null);
                              setEditing({ rule });
                            }}
                          >
                            <i className="fas fa-pen"></i>
                          </button>
                          <button
                            type="button"
                            className="btn btn-outline-danger"
                            disabled={!canEdit || saving}
                            title="Delete rule"
                            onClick={() => handleDelete(rule)}
                          >
                            <i className="fas fa-trash"></i>
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {editing && (
        <RuleEditModal
          key={editing.rule?.id ?? 'new'}
          rule={editing.rule}
          existingIds={rules.map((r) => r.id)}
          slotsPerEpoch={slotsPerEpoch}
          canEdit={canEdit}
          saving={saving}
          error={saveError}
          onSave={handleSave}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
};
