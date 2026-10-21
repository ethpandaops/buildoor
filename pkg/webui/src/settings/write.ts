import { authHeaders } from '../hooks/authFetch';
import { SETTING_FIELDS } from './fields';
import type { SettingValue } from './types';
import { apiPath } from '../utils/api';

export interface WriteResult {
  ok: boolean;
  error?: string;
}

async function send(method: 'PATCH' | 'POST', url: string, body: unknown): Promise<WriteResult> {
  try {
    const response = await fetch(url, {
      method,
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
// the settings (module enable flags included) in one atomic request, the key
// target through its own endpoint (it has side effects beyond storing the
// value). The new values come back through the SSE config/service_status
// events.
export async function writeSettings(values: Record<string, SettingValue>): Promise<WriteResult> {
  const generic: Record<string, SettingValue> = {};
  let target: SettingValue | undefined;

  for (const [key, value] of Object.entries(values)) {
    if ((SETTING_FIELDS[key]?.writer ?? 'settings') === 'builder-keys-target') {
      target = value;
    } else {
      generic[key] = value;
    }
  }

  if (Object.keys(generic).length > 0) {
    const result = await send('PATCH', apiPath('/config'), generic);
    if (!result.ok) return result;
  }
  if (target !== undefined) {
    const result = await send('POST', apiPath('/builder-keys/target'), { target });
    if (!result.ok) return result;
  }
  return { ok: true };
}
