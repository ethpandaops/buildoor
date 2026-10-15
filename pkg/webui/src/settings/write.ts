import { authHeaders } from '../hooks/authFetch';
import { SETTING_FIELDS } from './fields';
import type { SettingValue } from './types';

export interface WriteResult {
  ok: boolean;
  error?: string;
}

async function post(url: string, body: unknown): Promise<WriteResult> {
  try {
    const response = await fetch(url, {
      method: 'POST',
      headers: await authHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(body),
    });
    const result = await response.json().catch(() => null);
    if (!response.ok || result?.error) {
      return { ok: false, error: result?.error || `HTTP ${response.status}` };
    }
    return { ok: true };
  } catch (err) {
    return { ok: false, error: err instanceof Error ? err.message : String(err) };
  }
}

// writeSettings stores setting values through the endpoint each one needs:
// generic settings in one atomic request, the module enable flags through
// the service toggle and the key target through its own endpoint (both have
// side effects beyond storing the value). The new values come back through
// the SSE config/service_status events.
export async function writeSettings(values: Record<string, SettingValue>): Promise<WriteResult> {
  const generic: Record<string, SettingValue> = {};
  const toggles: Record<string, SettingValue> = {};
  let target: SettingValue | undefined;

  for (const [key, value] of Object.entries(values)) {
    switch (SETTING_FIELDS[key]?.writer ?? 'settings') {
      case 'service-toggle':
        toggles[key] = value;
        break;
      case 'builder-keys-target':
        target = value;
        break;
      default:
        generic[key] = value;
    }
  }

  if (Object.keys(generic).length > 0) {
    const result = await post('/api/config/settings', generic);
    if (!result.ok) return result;
  }
  if (Object.keys(toggles).length > 0) {
    const result = await post('/api/services/toggle', toggles);
    if (!result.ok) return result;
  }
  if (target !== undefined) {
    const result = await post('/api/buildoor/builder-keys/target', { target });
    if (!result.ok) return result;
  }
  return { ok: true };
}
