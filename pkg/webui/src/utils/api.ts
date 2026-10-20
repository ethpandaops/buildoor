// Base path of buildoor's REST API (pkg/webui/routes.go APIBasePath). Every
// request builds its URL through apiPath, so a new API version changes this
// one constant.
export const API_BASE = '/api/v1';

export function apiPath(path: string): string {
  return `${API_BASE}${path}`;
}
