import type { ReactNode } from 'react';

// A value as the settings endpoint takes it (and the SSE config carries it).
export type SettingValue = number | string | boolean;

export interface SettingOption {
  value: string;
  // Full wording for the drawer's select.
  label: string;
  // Compact wording for read-only summaries (defaults to label).
  short?: string;
}

// How a setting is written. Most go through POST /api/config/settings; a few
// have an endpoint of their own because the write has side effects.
export type SettingWriter =
  | 'settings' // POST /api/config/settings {key: value}
  | 'service-toggle' // POST /api/services/toggle {key: bool}
  | 'builder-keys-target'; // POST /api/buildoor/builder-keys/target

export interface SettingField {
  // Full label for the drawer form.
  label: string;
  // Compact label for read-only summaries (defaults to label).
  short?: string;
  kind: 'number' | 'text' | 'select' | 'bool' | 'upstreams';
  // Dotted path into the SSE `config` object; defaults to the key itself.
  path?: string;
  // Unit shown next to inputs and values ('ms', 'gwei', '%', 'ETH', ...).
  unit?: string;
  options?: SettingOption[];
  // Wording of a bool's two states in summaries and the drawer's switch
  // caption (defaults to on / off), e.g. parallel / sequential.
  boolLabels?: { on: string; off: string };
  // Display value = raw value / scale (e.g. 1e9 shows gwei as ETH);
  // the write sends display value * scale.
  scale?: number;
  min?: number;
  max?: number;
  // Number input accepts whole numbers only (default true for numbers
  // without scale).
  integer?: boolean;
  placeholder?: string;
  // What 0 (or an empty text) means, shown instead of the raw value
  // ('off', 'auto', 'no cap', ...).
  zeroLabel?: string;
  // One-line helper text under the input.
  hint?: string;
  // Full explanation, shown in the InfoHint callout.
  help: ReactNode;
  // Explains why the field is inert for the current draft (e.g. the reveal
  // time in vote-only gate mode); null when the field applies.
  inactiveWhen?: (draft: Record<string, SettingValue>) => string | null;
  // Extra validation beyond kind/min/max; returns the error message.
  validate?: (value: SettingValue, draft: Record<string, SettingValue>) => string | null;
  // How the value is written (defaults to 'settings').
  writer?: SettingWriter;
}

// Winning layer of a setting as reported by the backend (absent = default).
export interface SettingSource {
  source: 'cli' | 'ui';
  // When a UI override was last written (unix ms; UI layer only).
  updated_at?: number;
}
