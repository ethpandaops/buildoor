import React, { useState, useEffect } from 'react';
import { useSettings } from '../hooks/useSettings';
import type { Config } from '../types';

interface HistorySettingsSectionProps {
  config: Config | null;
}

interface HistoryForm {
  slot_result_retention_epochs: number;
  slot_artifact_retention_epochs: number;
  slot_artifact_capture_enabled: boolean;
}

const formFromConfig = (config: Config | null): HistoryForm => ({
  slot_result_retention_epochs: config?.slot_result_retention_epochs ?? 100,
  slot_artifact_retention_epochs: config?.slot_artifact_retention_epochs ?? 100,
  slot_artifact_capture_enabled: config?.slot_artifact_capture_enabled ?? true,
});

// HistorySettingsSection is the per-slot history retention policy, rendered as
// a section of the Payload Builder card: how long slot results and raw SSZ
// artifacts are kept, and whether artifacts are captured at all. Edits go
// through the generic path-based settings endpoint.
export const HistorySettingsSection: React.FC<HistorySettingsSectionProps> = ({ config }) => {
  const { isLoggedIn, postSettings } = useSettings();
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<HistoryForm>(formFromConfig(null));

  useEffect(() => {
    if (!editing) {
      setForm(formFromConfig(config));
    }
  }, [config, editing]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    const result = await postSettings({
      'slot_result_retention_epochs': form.slot_result_retention_epochs,
      'slot_artifact_retention_epochs': form.slot_artifact_retention_epochs,
      'slot_artifact_capture_enabled': form.slot_artifact_capture_enabled,
    });
    if (result.ok) {
      setEditing(false);
    } else {
      alert('Failed to update: ' + result.error);
    }
  };

  return (
    <>
      <div className="d-flex justify-content-between align-items-center mb-2 mt-3">
        <div className="section-header">Slot History</div>
        {isLoggedIn && !editing && (
          <button className="btn btn-sm btn-outline-primary" onClick={() => setEditing(true)}>
            <i className="fas fa-pencil-alt"></i>
          </button>
        )}
      </div>

      {!editing ? (
        <div className="row g-2">
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Result Retention</div>
              <div className="config-item-value">
                {config?.slot_result_retention_epochs ?? 0} epochs
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Artifact Retention</div>
              <div className="config-item-value">
                {config?.slot_artifact_retention_epochs ?? 0} epochs
              </div>
            </div>
          </div>
          <div className="col-6">
            <div className="config-item">
              <div className="config-item-label">Artifact Capture</div>
              <div className="config-item-value">
                {config?.slot_artifact_capture_enabled ? 'on' : 'off'}
              </div>
            </div>
          </div>
        </div>
      ) : (
        <form onSubmit={handleSave}>
          <div className="mb-2">
            <label className="form-label">Result Retention (epochs)</label>
            <input
              type="number"
              min={1}
              className="form-control form-control-sm"
              value={form.slot_result_retention_epochs}
              onChange={(e) =>
                setForm({ ...form, slot_result_retention_epochs: parseInt(e.target.value) || 1 })
              }
              required
            />
            <div className="form-text">
              Epochs of action plans and slot results kept before pruning.
            </div>
          </div>
          <div className="mb-2">
            <label className="form-label">Artifact Retention (epochs)</label>
            <input
              type="number"
              min={1}
              className="form-control form-control-sm"
              value={form.slot_artifact_retention_epochs}
              onChange={(e) =>
                setForm({ ...form, slot_artifact_retention_epochs: parseInt(e.target.value) || 1 })
              }
              required
            />
            <div className="form-text">
              Epochs of raw SSZ payloads, signed bids and envelopes kept in the
              state-db. Raw payloads dominate disk usage.
            </div>
          </div>
          <div className="form-check mb-2">
            <input
              type="checkbox"
              className="form-check-input"
              id="slot-artifact-capture-enabled"
              checked={form.slot_artifact_capture_enabled}
              onChange={(e) => setForm({ ...form, slot_artifact_capture_enabled: e.target.checked })}
            />
            <label className="form-check-label" htmlFor="slot-artifact-capture-enabled">
              Capture raw SSZ artifacts
            </label>
            <div className="form-text">
              Slot results are recorded either way; this only controls the raw objects.
            </div>
          </div>
          <div className="d-flex gap-2">
            <button type="submit" className="btn btn-sm btn-primary">Save</button>
            <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </form>
      )}
    </>
  );
};
