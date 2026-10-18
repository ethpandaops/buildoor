import type { FlowContext, SummaryRow } from '../../settings/flows';
import type { SettingSource } from '../../settings/types';
import { formatSetting, readSetting } from '../../settings/values';

// summaryValue renders a summary row: its own renderer, or the formatted
// values of its keys joined by " · ".
export function summaryValue(row: SummaryRow, ctx: FlowContext): string {
  if (row.value) return row.value(ctx);
  return row.keys.map((key) => formatSetting(key, readSetting(key, ctx))).join(' · ');
}

// summarySource is the source marker of a row: "ui" when any of its keys was
// changed in the UI, otherwise "cli" when any is set by flag.
export function summarySource(row: SummaryRow, ctx: FlowContext): SettingSource | undefined {
  const sources = ctx.config?.setting_sources;
  if (!sources) return undefined;
  const entries = row.keys.map((key) => sources[key]).filter((s): s is SettingSource => !!s);
  return entries.find((s) => s.source === 'ui') ?? entries[0];
}

// lastUIChange is the most recent UI write among the given keys (unix ms, 0
// when none).
export function lastUIChange(keys: string[], ctx: FlowContext): number {
  const sources = ctx.config?.setting_sources ?? {};
  return keys.reduce((max, key) => Math.max(max, sources[key]?.updated_at ?? 0), 0);
}
