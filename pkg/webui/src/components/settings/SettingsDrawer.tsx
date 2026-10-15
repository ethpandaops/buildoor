import React, { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { closeSettings, useSettingsDrawer, type FlowId } from '../../stores/settingsDrawerStore';
import { FLOWS, type FlowContext, type FlowDef } from '../../settings/flows';
import { SETTING_FIELDS } from '../../settings/fields';
import type { SettingValue } from '../../settings/types';
import {
  formatSetting,
  fromFormValue,
  readSetting,
  sameValue,
  toFormValue,
  type FormValue,
} from '../../settings/values';
import { writeSettings } from '../../settings/write';
import { InfoHint } from '../InfoHint';
import { Tabs } from '../ui/Tabs';
import { Tag } from '../ui/Tag';
import { ElStatusPanel } from './ElStatusPanel';
import { FlowToggleSwitch } from './FlowToggleSwitch';
import { SettingInput } from './SettingInput';
import { SourceTag } from './SourceTag';
import { useFlowContext } from './useFlowContext';
import '../../styles/settings.css';

// SettingsDrawer is the one place settings are edited. It is rendered once by
// the app shell and opened through openSettings(flow, tab).
export const SettingsDrawer: React.FC = () => {
  const { flow, tab } = useSettingsDrawer();
  if (!flow) return null;
  // Keyed by flow: switching flows starts a fresh draft.
  return <FlowDrawer key={flow} flowId={flow} initialTab={tab} />;
};

// --------------------------------------------------------------- draft --

// The drawer edits a snapshot: `base` is the value each field had when the
// form took it, `draft` what the inputs hold. Live updates never overwrite a
// field being edited — an untouched field silently follows the live value,
// an edited one records a conflict the operator resolves explicitly.
interface DraftState {
  base: Record<string, SettingValue | undefined>;
  draft: Record<string, FormValue>;
  conflicts: Record<string, SettingValue>;
  remote: Record<string, true>;
}

type DraftAction =
  | { type: 'live'; live: Record<string, SettingValue | undefined> }
  | { type: 'edit'; key: string; value: FormValue }
  | { type: 'take-remote'; key: string }
  | { type: 'keep-mine'; key: string }
  | { type: 'saved'; values: Record<string, SettingValue> }
  | { type: 'discard' };

function parsedEquals(key: string, form: FormValue, raw: SettingValue | undefined): boolean {
  const field = SETTING_FIELDS[key];
  if (!field) return true;
  const parsed = fromFormValue(field, form);
  if ('error' in parsed) return raw === undefined && form === toFormValue(field, undefined);
  return sameValue(parsed.value, raw);
}

function isDirty(state: DraftState, key: string): boolean {
  return !parsedEquals(key, state.draft[key], state.base[key]);
}

function draftReducer(state: DraftState, action: DraftAction): DraftState {
  switch (action.type) {
    case 'live': {
      let next = state;
      const mutate = () => {
        if (next === state) {
          next = {
            base: { ...state.base },
            draft: { ...state.draft },
            conflicts: { ...state.conflicts },
            remote: { ...state.remote },
          };
        }
        return next;
      };

      for (const [key, live] of Object.entries(action.live)) {
        if (live === undefined) continue;
        const field = SETTING_FIELDS[key];
        const base = state.base[key];

        if (base === undefined) {
          // First value delivered by the stream.
          const s = mutate();
          s.base[key] = live;
          if (!isDirty(state, key) || state.draft[key] === toFormValue(field, undefined)) {
            s.draft[key] = toFormValue(field, live);
          }
          continue;
        }

        if (sameValue(base, live)) {
          if (key in state.conflicts) {
            const s = mutate();
            delete s.conflicts[key];
          }
          continue;
        }

        const s = mutate();
        if (!isDirty(state, key)) {
          s.base[key] = live;
          s.draft[key] = toFormValue(field, live);
          s.remote[key] = true;
          delete s.conflicts[key];
        } else if (parsedEquals(key, state.draft[key], live)) {
          // Someone set exactly what is being typed: nothing left to save.
          s.base[key] = live;
          delete s.conflicts[key];
        } else {
          s.conflicts[key] = live;
        }
      }
      return next;
    }
    case 'edit':
      return { ...state, draft: { ...state.draft, [action.key]: action.value } };
    case 'take-remote': {
      const live = state.conflicts[action.key];
      const conflicts = { ...state.conflicts };
      delete conflicts[action.key];
      return {
        ...state,
        base: { ...state.base, [action.key]: live },
        draft: { ...state.draft, [action.key]: toFormValue(SETTING_FIELDS[action.key], live) },
        conflicts,
      };
    }
    case 'keep-mine': {
      const live = state.conflicts[action.key];
      const conflicts = { ...state.conflicts };
      delete conflicts[action.key];
      return { ...state, base: { ...state.base, [action.key]: live }, conflicts };
    }
    case 'saved':
      return { ...state, base: { ...state.base, ...action.values } };
    case 'discard': {
      const draft: Record<string, FormValue> = {};
      for (const key of Object.keys(state.draft)) {
        draft[key] = toFormValue(SETTING_FIELDS[key], state.base[key]);
      }
      return { ...state, draft, conflicts: {} };
    }
  }
}

function flowKeys(flow: FlowDef): string[] {
  return flow.tabs.flatMap((tab) => tab.sections.flatMap((section) => section.keys));
}

function initDraft(keys: string[], ctx: FlowContext): DraftState {
  const base: DraftState['base'] = {};
  const draft: DraftState['draft'] = {};
  for (const key of keys) {
    base[key] = readSetting(key, ctx);
    draft[key] = toFormValue(SETTING_FIELDS[key], base[key]);
  }
  return { base, draft, conflicts: {}, remote: {} };
}

// Settings changes that cannot be undone get a confirmation step.
function irreversibleWarnings(state: DraftState, dirty: string[], values: Record<string, SettingValue>): string[] {
  const warnings: string[] = [];
  const target = 'builder_keys.target_count';
  if (dirty.includes(target) && Number(values[target]) < Number(state.base[target])) {
    warnings.push(
      'Lowering the target keys exits the surplus keys, highest index first (when auto exit is on). ' +
        'An exited builder key can never be reactivated.',
    );
  }
  const autoExit = 'builder_keys.auto_exit';
  if (dirty.includes(autoExit) && values[autoExit] === true) {
    warnings.push('Auto exit exits keys above the target on its own; an exited builder key can never be reactivated.');
  }
  return warnings;
}

// --------------------------------------------------------------- drawer --

interface FlowDrawerProps {
  flowId: FlowId;
  initialTab: string | null;
}

const FlowDrawer: React.FC<FlowDrawerProps> = ({ flowId, initialTab }) => {
  const flow = FLOWS[flowId];
  const ctx = useFlowContext();
  const { isLoggedIn, login } = useAuthContext();
  const keys = useMemo(() => flowKeys(flow), [flow]);

  const [state, dispatch] = useReducer(draftReducer, undefined, () => initDraft(keys, ctx));
  const [activeTab, setActiveTab] = useState(
    () => flow.tabs.find((t) => t.id === initialTab)?.id ?? flow.tabs[0].id,
  );
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<number | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [closePrompt, setClosePrompt] = useState(false);
  const [toggleError, setToggleError] = useState<string | null>(null);
  const panelRef = useRef<HTMLElement>(null);

  // Follow the live values (see DraftState).
  const live = useMemo(() => {
    const values: Record<string, SettingValue | undefined> = {};
    for (const key of keys) values[key] = readSetting(key, ctx);
    return values;
  }, [keys, ctx]);
  useEffect(() => {
    dispatch({ type: 'live', live });
  }, [live]);

  // Every setting's value as the form would save it, for the cross-field
  // rules (inactiveWhen, validate) — including settings of other flows.
  const draftValues = useMemo(() => {
    const values: Record<string, SettingValue> = {};
    for (const key of Object.keys(SETTING_FIELDS)) {
      const current = readSetting(key, ctx);
      if (current !== undefined) values[key] = current;
    }
    for (const key of keys) {
      const parsed = fromFormValue(SETTING_FIELDS[key], state.draft[key]);
      if ('value' in parsed) values[key] = parsed.value;
    }
    return values;
  }, [keys, ctx, state.draft]);

  const dirty = keys.filter((key) => isDirty(state, key));
  const errors: Record<string, string> = {};
  for (const key of dirty) {
    const field = SETTING_FIELDS[key];
    const parsed = fromFormValue(field, state.draft[key]);
    if ('error' in parsed) {
      errors[key] = parsed.error;
      continue;
    }
    const message = field.validate?.(parsed.value, draftValues);
    if (message) errors[key] = message;
  }
  const errorCount = Object.keys(errors).length;
  const conflictCount = Object.keys(state.conflicts).length;
  const sources = ctx.config?.setting_sources ?? {};

  const requestClose = useCallback(() => {
    if (dirty.length > 0) {
      setClosePrompt(true);
      return;
    }
    closeSettings();
  }, [dirty.length]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') requestClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [requestClose]);

  useEffect(() => {
    panelRef.current?.focus();
  }, []);

  const save = async () => {
    if (dirty.length === 0 || errorCount > 0) return;
    const values: Record<string, SettingValue> = {};
    for (const key of dirty) values[key] = draftValues[key];

    if (!confirming && irreversibleWarnings(state, dirty, values).length > 0) {
      setConfirming(true);
      return;
    }

    setSaving(true);
    setSaveError(null);
    const result = await writeSettings(values);
    setSaving(false);
    setConfirming(false);
    if (!result.ok) {
      setSaveError(result.error ?? 'Saving failed');
      return;
    }
    dispatch({ type: 'saved', values });
    setSavedAt(Date.now());
  };

  const pendingWarnings = confirming
    ? irreversibleWarnings(
        state,
        dirty,
        Object.fromEntries(dirty.map((key) => [key, draftValues[key]])),
      )
    : [];
  const status = flow.status(ctx);
  const tab = flow.tabs.find((t) => t.id === activeTab) ?? flow.tabs[0];
  const lastChange = keys
    .map((key) => sources[key]?.updated_at ?? 0)
    .reduce((max, at) => Math.max(max, at), 0);

  return (
    <div className="sd-root">
      <div className="sd-overlay" onClick={requestClose} aria-hidden="true" />
      <aside
        ref={panelRef}
        className="sd-panel-root"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sd-title"
        tabIndex={-1}
      >
        <div className="sd-head">
          <div className="sd-title-row">
            <h2 id="sd-title" className="sd-title">
              {flow.title}
            </h2>
            <Tag tone={status.tone}>{status.label}</Tag>
            <div className="sd-head-actions">
              <FlowToggleSwitch flow={flow} ctx={ctx} onError={setToggleError} />
              <button type="button" className="bd-btn bd-btn-sm" aria-label="Close" onClick={requestClose}>
                <i className="fas fa-xmark" aria-hidden="true"></i>
              </button>
            </div>
          </div>
          {toggleError && <div className="sd-field-error">{toggleError}</div>}
          <p className="sd-description">{flow.description}</p>
          {flow.tabs.length > 1 && (
            <Tabs
              label={`${flow.title} settings`}
              active={tab.id}
              onChange={setActiveTab}
              tabs={flow.tabs.map((t) => {
                const tabKeys = t.sections.flatMap((s) => s.keys);
                const tabDirty = tabKeys.some((key) => dirty.includes(key));
                const tabConflict = tabKeys.some((key) => key in state.conflicts);
                return {
                  id: t.id,
                  label: t.label,
                  extra:
                    tabDirty || tabConflict ? (
                      <span
                        className={`sd-dot${tabConflict ? ' sd-dot-conflict' : ''}`}
                        aria-label={tabConflict ? 'has conflicts' : 'has changes'}
                      />
                    ) : undefined,
                };
              })}
            />
          )}
        </div>

        <div className="sd-body">
          {!isLoggedIn && (
            <div className="bd-notice mb-3">
              <i className="fas fa-lock" aria-hidden="true"></i>
              <span className="flex-grow-1">Read-only. Log in to change these settings.</span>
              <button type="button" className="bd-btn bd-btn-sm bd-btn-primary" onClick={login}>
                Log in
              </button>
            </div>
          )}

          {tab.panel === 'el-status' && <ElStatusPanel />}

          {tab.sections.map((section, index) => (
            <fieldset className="sd-section" key={section.title ?? index}>
              {section.title && <legend className="bd-group-title">{section.title}</legend>}
              <div className="sd-grid">
                {section.keys.map((key) => {
                  const field = SETTING_FIELDS[key];
                  const inactive = field.inactiveWhen?.(draftValues) ?? null;
                  const fieldDirty = dirty.includes(key);
                  const conflict = state.conflicts[key];
                  const wide = field.kind === 'upstreams' || field.kind === 'bool';
                  const id = `sd-${key.replace(/[^a-z0-9]/gi, '-')}`;
                  return (
                    <div className={`sd-field${wide ? ' sd-field-wide' : ''}${inactive ? ' sd-field-inactive' : ''}`} key={key}>
                      <div className="sd-label-row">
                        <label htmlFor={id} className="sd-label">
                          {field.label}
                        </label>
                        <InfoHint>{field.help}</InfoHint>
                        <SourceTag source={sources[key]} />
                        {fieldDirty && <span className="sd-dot" aria-label="changed" />}
                        {state.remote[key] && !fieldDirty && (
                          <span className="sd-remote" title="Changed elsewhere while this drawer was open">
                            updated
                          </span>
                        )}
                      </div>
                      <SettingInput
                        id={id}
                        field={field}
                        value={state.draft[key]}
                        disabled={!isLoggedIn || saving}
                        invalid={key in errors}
                        onChange={(value) => {
                          setConfirming(false);
                          setSavedAt(null);
                          dispatch({ type: 'edit', key, value });
                        }}
                      />
                      {errors[key] ? (
                        <div className="sd-field-error">{errors[key]}</div>
                      ) : inactive ? (
                        <div className="sd-hint">{inactive}</div>
                      ) : field.hint ? (
                        <div className="sd-hint">
                          {field.hint}
                          {fieldDirty && ` Was ${formatSetting(key, state.base[key], false)}.`}
                        </div>
                      ) : fieldDirty ? (
                        <div className="sd-hint">Was {formatSetting(key, state.base[key], false)}.</div>
                      ) : null}
                      {conflict !== undefined && (
                        <div className="sd-conflict" role="status">
                          <span>
                            Changed elsewhere to <strong>{formatSetting(key, conflict, false)}</strong> while you
                            were editing.
                          </span>
                          <span className="d-flex gap-2">
                            <button type="button" className="bd-btn bd-btn-sm" onClick={() => dispatch({ type: 'take-remote', key })}>
                              Use theirs
                            </button>
                            <button type="button" className="bd-btn bd-btn-sm" onClick={() => dispatch({ type: 'keep-mine', key })}>
                              Keep mine
                            </button>
                          </span>
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </fieldset>
          ))}

          {lastChange > 0 && (
            <p className="sd-footnote">Last changed in the UI {new Date(lastChange).toLocaleString()}.</p>
          )}
        </div>

        {isLoggedIn && (
          <div className="sd-foot">
            {closePrompt ? (
              <>
                <span className="sd-foot-text">
                  Discard {dirty.length} unsaved change{dirty.length === 1 ? '' : 's'}?
                </span>
                <button type="button" className="bd-btn" onClick={() => setClosePrompt(false)}>
                  Keep editing
                </button>
                <button type="button" className="bd-btn bd-btn-danger" onClick={() => closeSettings()}>
                  Discard and close
                </button>
              </>
            ) : (
              <>
                <span className="sd-foot-text">
                  {saveError ? (
                    <span className="sd-field-error">{saveError}</span>
                  ) : pendingWarnings.length > 0 ? (
                    <span className="sd-warn">{pendingWarnings.join(' ')}</span>
                  ) : dirty.length > 0 ? (
                    [
                      `${dirty.length} change${dirty.length === 1 ? '' : 's'}`,
                      errorCount > 0 ? `${errorCount} error${errorCount === 1 ? '' : 's'}` : null,
                      conflictCount > 0 ? `${conflictCount} conflict${conflictCount === 1 ? '' : 's'}` : null,
                    ]
                      .filter(Boolean)
                      .join(' · ')
                  ) : savedAt ? (
                    <span className="sd-saved">
                      <i className="fas fa-check" aria-hidden="true"></i> Saved
                    </span>
                  ) : (
                    'No changes'
                  )}
                </span>
                <button
                  type="button"
                  className="bd-btn"
                  disabled={dirty.length === 0 || saving}
                  onClick={() => {
                    setConfirming(false);
                    setSaveError(null);
                    dispatch({ type: 'discard' });
                  }}
                >
                  Discard
                </button>
                <button
                  type="button"
                  className={`bd-btn ${pendingWarnings.length > 0 ? 'bd-btn-danger' : 'bd-btn-primary'}`}
                  disabled={dirty.length === 0 || errorCount > 0 || saving}
                  onClick={save}
                >
                  {saving ? 'Saving…' : pendingWarnings.length > 0 ? 'Confirm and save' : 'Save changes'}
                </button>
              </>
            )}
          </div>
        )}
      </aside>
    </div>
  );
};
