import React, { useState } from 'react';
import { useAuthContext } from '../../context/AuthContext';
import { Switch } from '../ui/Switch';
import type { FlowContext, FlowDef } from '../../settings/flows';
import { readSetting } from '../../settings/values';
import { writeSettings } from '../../settings/write';

interface FlowToggleSwitchProps {
  flow: FlowDef;
  ctx: FlowContext;
  // Receives the write error (null when a write succeeded).
  onError?: (error: string | null) => void;
}

// FlowToggleSwitch turns a flow on or off with one click. Logged-out readers
// see the state but cannot change it; a module that cannot run at all can
// only be switched off.
export const FlowToggleSwitch: React.FC<FlowToggleSwitchProps> = ({ flow, ctx, onError }) => {
  const { isLoggedIn } = useAuthContext();
  const [busy, setBusy] = useState(false);

  if (!flow.toggle) return null;

  const key = flow.toggle.key;
  const on = readSetting(key, ctx) === true;
  const unavailable = flow.toggle.unavailable?.(ctx) ?? null;
  const blocked = !isLoggedIn || (!on && unavailable !== null);
  const title = !isLoggedIn
    ? 'Log in to change'
    : !on && unavailable
      ? unavailable
      : `${on ? 'Disable' : 'Enable'} ${flow.title}`;

  const toggle = async (next: boolean) => {
    setBusy(true);
    const result = await writeSettings({ [key]: next });
    setBusy(false);
    onError?.(result.ok ? null : result.error ?? 'Update failed');
  };

  return (
    <Switch
      checked={on}
      label={flow.title}
      title={title}
      disabled={blocked}
      busy={busy}
      onChange={toggle}
    />
  );
};
