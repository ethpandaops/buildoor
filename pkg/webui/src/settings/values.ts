import type { BuilderKeysAggregate, Config, ServiceStatus } from '../types';
import { SETTING_FIELDS, settingPath } from './fields';
import type { SettingField, SettingValue } from './types';

// The live state a setting value can be read from: most live in the SSE
// config, the module enable flags in the service status.
export interface SettingContext {
  config: Config | null;
  serviceStatus: ServiceStatus | null;
  builderKeysAggregate?: BuilderKeysAggregate | null;
}

const SERVICE_STATUS_PREFIX = 'service_status.';

function readPath(obj: unknown, path: string): unknown {
  let cur: unknown = obj;
  for (const part of path.split('.')) {
    if (cur === null || typeof cur !== 'object') return undefined;
    cur = (cur as Record<string, unknown>)[part];
  }
  return cur;
}

// readSetting returns a setting's current raw value (as the settings
// endpoint takes it), or undefined while the stream has not delivered it.
export function readSetting(key: string, ctx: SettingContext): SettingValue | undefined {
  const path = settingPath(key);
  const value = path.startsWith(SERVICE_STATUS_PREFIX)
    ? readPath(ctx.serviceStatus, path.slice(SERVICE_STATUS_PREFIX.length))
    : readPath(ctx.config, path);

  if (typeof value === 'number' || typeof value === 'string' || typeof value === 'boolean') {
    return value;
  }
  return undefined;
}

// Form values are what the inputs hold: strings for number/text/select
// inputs (so partial input like "-" or "1." survives typing), booleans for
// checkboxes.
export type FormValue = string | boolean;

function scaled(field: SettingField, raw: number): number {
  return field.scale ? raw / field.scale : raw;
}

export function toFormValue(field: SettingField, raw: SettingValue | undefined): FormValue {
  if (field.kind === 'bool') return raw === true;
  if (raw === undefined) return '';
  if (field.kind === 'number' && typeof raw === 'number') return String(scaled(field, raw));
  return String(raw);
}

// fromFormValue converts an input value back to the raw value, or returns
// an error message when it is not a valid value for the field.
export function fromFormValue(
  field: SettingField,
  value: FormValue,
): { value: SettingValue } | { error: string } {
  switch (field.kind) {
    case 'bool':
      return { value: value === true };
    case 'number': {
      const text = String(value).trim();
      if (text === '') return { error: 'Enter a value' };
      const parsed = Number(text);
      if (!Number.isFinite(parsed)) return { error: 'Enter a number' };
      const integer = field.integer ?? !field.scale;
      if (integer && !Number.isInteger(parsed)) return { error: 'Enter a whole number' };
      if (field.min !== undefined && parsed < field.min) {
        return { error: `Must be at least ${field.min}${field.unit ? ` ${field.unit}` : ''}` };
      }
      if (field.max !== undefined && parsed > field.max) {
        return { error: `Must be at most ${field.max}${field.unit ? ` ${field.unit}` : ''}` };
      }
      const raw = field.scale ? Math.round(parsed * field.scale) : parsed;
      return { value: raw };
    }
    default:
      return { value: String(value) };
  }
}

const numberFormat = new Intl.NumberFormat('en-US', { maximumFractionDigits: 6 });

// formatNumber renders a number with thousands separators.
export function formatNumber(value: number): string {
  return numberFormat.format(value);
}

// formatSetting renders a raw value for read-only display: option labels,
// zero labels ("off", "auto"), scaled numbers with units, on/off.
export function formatSetting(key: string, raw: SettingValue | undefined, compact = true): string {
  const field = SETTING_FIELDS[key];
  if (!field || raw === undefined) return '—';

  switch (field.kind) {
    case 'bool':
      return raw ? field.boolLabels?.on ?? 'on' : field.boolLabels?.off ?? 'off';
    case 'select': {
      const option = field.options?.find((o) => o.value === String(raw));
      if (!option) return String(raw) || field.zeroLabel || '—';
      return compact ? option.short ?? option.label : option.label;
    }
    case 'number': {
      const num = Number(raw);
      if (num === 0 && field.zeroLabel) return field.zeroLabel;
      const text = formatNumber(scaled(field, num));
      return field.unit ? `${text} ${field.unit}` : text;
    }
    case 'upstreams':
    case 'text': {
      const text = String(raw);
      if (text === '') return field.zeroLabel ?? '—';
      return text;
    }
  }
}

// sameValue compares raw values (numbers compare numerically).
export function sameValue(a: SettingValue | undefined, b: SettingValue | undefined): boolean {
  if (typeof a === 'number' || typeof b === 'number') return Number(a) === Number(b);
  return a === b;
}
