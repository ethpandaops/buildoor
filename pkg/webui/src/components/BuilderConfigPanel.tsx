import React, { useState, useEffect } from 'react';
import { useAuthContext } from '../context/AuthContext';
import { CandidatePolicySection } from './CandidatePolicySection';
import { HistorySettingsSection } from './HistorySettingsSection';
import { useSettings } from '../hooks/useSettings';
import { InfoHint } from './InfoHint';
import type { Config, ScheduleConfig } from '../types';

// Setting explanations shared by the view rows and the edit forms.
const HINT_BUILD_START = (
  <>
    When the slot&apos;s payload build starts, in ms relative to the proposal slot&apos;s start
    (<code>epbs.build_start_time</code>); negative = before the slot starts. Applies to p2p bids
    and the Builder API alike. Startup default: -3400 ms at 12 s slots, scaled to the slot time.
    If no payload attributes arrived by then, the previous slot&apos;s are re-used (missing-block
    fallback).
  </>
);

const HINT_BUILD_DELAY = (
  <>
    Time in ms the EL gets to fill the block between the build trigger (forkchoiceUpdated) and
    fetching it (getPayload) (<code>payload_build_time</code>). Too short returns a near-empty
    payload. Startup default: 2600 ms at 12 s slots, scaled to the slot time.
  </>
);

const HINT_EXTRA_DATA = (
  <>
    Prefix written into every built block&apos;s extra-data field (<code>extra_data</code>), followed
    by <code>/</code> and the EL&apos;s own extra data, truncated to 32 bytes. Marks blocks built by
    this instance. Empty = keep the EL&apos;s extra data unchanged.
  </>
);

const HINT_SCHEDULE_MODE = (
  <>
    Which slots the builder builds for (<code>schedule.mode</code>). Slots before the start slot
    are always skipped; a per-slot action plan that activates bidding or Builder API serving builds
    regardless.
    <dl>
      <dt>all</dt>
      <dd>Build every slot.</dd>
      <dt>every_nth</dt>
      <dd>Build only every Nth slot, counted from the start slot (or slot 0).</dd>
      <dt>next_n</dt>
      <dd>Build the next N slots, then stop. The count restarts whenever any setting is saved while this mode is active.</dd>
    </dl>
  </>
);

const HINT_EVERY_NTH = (
  <>
    Slot step for <em>every_nth</em> mode (<code>schedule.every_nth</code>): builds slots where
    (slot - start slot) is a multiple of N. 1 = every slot. Ignored in other modes.
  </>
);

const HINT_NEXT_N = (
  <>
    Build budget for <em>next_n</em> mode (<code>schedule.next_n</code>): the number of slots to
    build before stopping. 0 = build nothing. Plan-forced builds do not consume it. Ignored in
    other modes.
  </>
);

const HINT_START_SLOT = (
  <>
    First slot the schedule may build for (<code>schedule.start_slot</code>); earlier slots are
    skipped in every mode. Also the origin for <em>every_nth</em> counting. 0 = no lower bound.
  </>
);

interface BuilderConfigPanelProps {
  config: Config | null;
}

interface BuilderFormState {
  build_start_time: number;
  payload_build_delay: number;
  extra_data: string;
}

export const BuilderConfigPanel: React.FC<BuilderConfigPanelProps> = ({ config }) => {
  const { isLoggedIn } = useAuthContext();
  const { postSettings } = useSettings();
  const [collapsed, setCollapsed] = useState(true);
  const [editing, setEditing] = useState(false);
  const [editingSchedule, setEditingSchedule] = useState(false);

  const [form, setForm] = useState<BuilderFormState>({
    build_start_time: 0,
    payload_build_delay: 0,
    extra_data: '',
  });

  const [scheduleForm, setScheduleForm] = useState<ScheduleConfig>({
    mode: 'all',
    every_nth: 1,
    next_n: 0,
    start_slot: 0,
  });

  // Get payload_build_time from the root-level config (sent by SSE)
  const payloadBuildDelay = (() => {
    const rootValue = (config as unknown as Record<string, unknown>)?.payload_build_time;
    if (typeof rootValue === 'number') return rootValue;
    return config?.epbs?.payload_build_delay || 0;
  })();

  // Sync form state when not editing
  useEffect(() => {
    if (!editing && config?.epbs) {
      setForm({
        build_start_time: config.epbs.build_start_time || 0,
        payload_build_delay: payloadBuildDelay,
        extra_data: config.extra_data ?? '',
      });
    }
  }, [config, editing, payloadBuildDelay]);

  // Sync schedule form state when not editing
  useEffect(() => {
    if (!editingSchedule && config?.schedule) {
      setScheduleForm(config.schedule);
    }
  }, [config, editingSchedule]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    const result = await postSettings({
      'epbs.build_start_time': form.build_start_time,
      'payload_build_time': form.payload_build_delay,
      'extra_data': form.extra_data,
    });
    if (result.ok) {
      setEditing(false);
    } else {
      alert('Failed to update: ' + result.error);
    }
  };

  const handleScheduleSave = async (e: React.FormEvent) => {
    e.preventDefault();

    const result = await postSettings({
      'schedule.mode': scheduleForm.mode,
      'schedule.every_nth': scheduleForm.every_nth,
      'schedule.next_n': scheduleForm.next_n,
      'schedule.start_slot': scheduleForm.start_slot,
    });
    if (result.ok) {
      setEditingSchedule(false);
    } else {
      alert('Failed to update: ' + result.error);
    }
  };

  const canEdit = isLoggedIn;
  const schedule = config?.schedule;

  return (
    <div className="card mb-3">
      <div
        className="card-header d-flex align-items-center"
        style={{ cursor: 'pointer' }}
        onClick={() => setCollapsed(!collapsed)}
      >
        <i className={`fas fa-chevron-${collapsed ? 'right' : 'down'} me-2`}></i>
        <h5 className="mb-0 me-2">Payload Builder</h5>
      </div>

      {!collapsed && (
        <div className="card-body">
          {/* Build Config Section */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Build Config</div>
            {canEdit && !editing && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditing(true)}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editing ? (
            <div className="row g-2 mb-3">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Build Start <InfoHint>{HINT_BUILD_START}</InfoHint></div>
                  <div className="config-item-value">{config?.epbs?.build_start_time || 0} ms</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Payload Build Delay <InfoHint alignEnd>{HINT_BUILD_DELAY}</InfoHint></div>
                  <div className="config-item-value">{payloadBuildDelay} ms</div>
                </div>
              </div>
              <div className="col-12">
                <div className="config-item">
                  <div className="config-item-label">Extra Data <InfoHint>{HINT_EXTRA_DATA}</InfoHint></div>
                  <div className="config-item-value">{config?.extra_data || '—'}</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleSave} className="mb-3">
              <div className="mb-2">
                <label className="form-label">Build Start Time (ms) <InfoHint>{HINT_BUILD_START}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.build_start_time}
                  onChange={(e) => setForm({ ...form, build_start_time: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Payload Build Delay (ms) <InfoHint>{HINT_BUILD_DELAY}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={form.payload_build_delay}
                  onChange={(e) => setForm({ ...form, payload_build_delay: parseInt(e.target.value) || 0 })}
                  required
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Extra Data <InfoHint>{HINT_EXTRA_DATA}</InfoHint></label>
                <input
                  type="text"
                  className="form-control form-control-sm"
                  value={form.extra_data}
                  maxLength={32}
                  placeholder="buildoor/"
                  onChange={(e) => setForm({ ...form, extra_data: e.target.value })}
                />
                <div className="form-text">Prefix injected into the block's extra-data field (max 32 bytes, padded with the EL's original extra data).</div>
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditing(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}

          {/* Build candidate policy */}
          <CandidatePolicySection config={config} />

          {/* Schedule Section */}
          <div className="d-flex justify-content-between align-items-center mb-2">
            <div className="section-header">Schedule</div>
            {canEdit && !editingSchedule && (
              <button
                className="btn btn-sm btn-outline-primary"
                onClick={() => setEditingSchedule(true)}
              >
                <i className="fas fa-pencil-alt"></i>
              </button>
            )}
          </div>

          {!editingSchedule ? (
            <div className="row g-2">
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Mode <InfoHint>{HINT_SCHEDULE_MODE}</InfoHint></div>
                  <div className="config-item-value">{schedule?.mode || 'all'}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Every Nth <InfoHint alignEnd>{HINT_EVERY_NTH}</InfoHint></div>
                  <div className="config-item-value">{schedule?.every_nth || 1}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Next N <InfoHint>{HINT_NEXT_N}</InfoHint></div>
                  <div className="config-item-value">{schedule?.next_n || 0}</div>
                </div>
              </div>
              <div className="col-6">
                <div className="config-item">
                  <div className="config-item-label">Start Slot <InfoHint alignEnd>{HINT_START_SLOT}</InfoHint></div>
                  <div className="config-item-value">{schedule?.start_slot || 0}</div>
                </div>
              </div>
            </div>
          ) : (
            <form onSubmit={handleScheduleSave}>
              <div className="mb-2">
                <label className="form-label">Mode <InfoHint>{HINT_SCHEDULE_MODE}</InfoHint></label>
                <select
                  className="form-select form-select-sm"
                  value={scheduleForm.mode}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, mode: e.target.value })}
                  required
                >
                  <option value="all">All</option>
                  <option value="every_nth">Every Nth</option>
                  <option value="next_n">Next N</option>
                </select>
              </div>
              <div className="mb-2">
                <label className="form-label">Every Nth <InfoHint>{HINT_EVERY_NTH}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.every_nth}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, every_nth: parseInt(e.target.value) || 1 })}
                  min={1}
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Next N <InfoHint>{HINT_NEXT_N}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.next_n}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, next_n: parseInt(e.target.value) || 0 })}
                  min={0}
                />
              </div>
              <div className="mb-2">
                <label className="form-label">Start Slot <InfoHint>{HINT_START_SLOT}</InfoHint></label>
                <input
                  type="number"
                  className="form-control form-control-sm"
                  value={scheduleForm.start_slot}
                  onChange={(e) => setScheduleForm({ ...scheduleForm, start_slot: parseInt(e.target.value) || 0 })}
                  min={0}
                />
              </div>
              <div className="d-flex gap-2">
                <button type="submit" className="btn btn-sm btn-primary">Save</button>
                <button type="button" className="btn btn-sm btn-secondary" onClick={() => setEditingSchedule(false)}>
                  Cancel
                </button>
              </div>
            </form>
          )}

          {/* Slot result / artifact history */}
          <HistorySettingsSection config={config} />
        </div>
      )}
    </div>
  );
};
