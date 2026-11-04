import { getBasePath } from './basepath';
export type Telemetry = { peer_connected: boolean; source_rejected?: number; last_rejected_source?: string; invalid_packets?: number; mtu_rejected?: number; clock_rejected?: number; clock_skew_seconds?: number; tx_bytes: number; rx_bytes: number; tx_packets: number; rx_packets: number; drops: number; last_peer: number; rtt_ms?: number };
export type RecoveryState = { active: boolean; attempts: number; max_attempts: number; next_retry_at: number; exhausted: boolean };
export type TunnelInstance = {
 id: number; name: string; mode: string; engine: string; enabled: boolean; status: string; uptime: number; error?: string; status_error?: string;
 send_transport: string; recv_transport: string; remote_addr: string; client_ip: string; local_ip: string; tun_interface: string; tun_address: string; tun_peer: string; tun_mtu: number;
 spoof_ip: string; peer_spoof_ip: string; spoof_ip_list: string; peer_spoof_ip_list: string; remote_port: number; recv_port: number; listen_port: number; client_port: number; psk: string; updated_at?: string;
};
export type TesterResult = { ip: string; received: number; sent: number; loss_pct: number; passed: boolean };
export type TesterState = { test_id?: string; rejected_probes?: number; duplicate_probes?: number; status: string; mode: string; error?: string; progress: number; total_ips: number; passed_count: number; recv_count: number; results: TesterResult[]; preview_limit?: number };
type ForwardStatus = { rule: { id: number; name: string; protocol: string; listen_port: number; target_port: number; enabled: boolean }; target: string; listening: boolean; stats: { listening: boolean; connections: number; bytes_up: number; bytes_down: number; last_error: string } };
type System = { hostname: string; os: string; arch: string; cpus: number; memory_mb: number; go_version: string; panel_version: string };
type Dashboard = { total: number; running_count: number; stopped_count: number; error_count: number; instances: (TunnelInstance & { telemetry: Telemetry; recovery?: RecoveryState })[] | null };
type End = Record<string, string | number | boolean>;
function apiBase() { return typeof window === 'undefined' ? '/api' : `${window.location.origin}${getBasePath()}/api`; }
function getToken() { return typeof window === 'undefined' ? null : localStorage.getItem('token'); }
export function setToken(token: string) { localStorage.setItem('token', token); }
export function clearToken() { localStorage.removeItem('token'); }
async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
 const controller = new AbortController();
 const timeout = setTimeout(() => controller.abort(), 15000);
 const abort = () => controller.abort();
 options.signal?.addEventListener('abort', abort, { once: true });
 if (options.signal?.aborted) controller.abort();
 try {
  const headers = new Headers(options.headers); headers.set('Content-Type', 'application/json');
  const token = getToken(); if (token) headers.set('Authorization', `Bearer ${token}`);
  const res = await fetch(`${apiBase()}${path}`, { ...options, headers, signal: controller.signal });
  if (res.status === 401 && path !== '/auth/login') { clearToken(); window.location.replace(getBasePath() + '/login/'); throw new Error('نشست شما منقضی شده است؛ دوباره وارد شوید.'); }
  let data: unknown;
  try { data = await res.json(); } catch (error) {
   if (controller.signal.aborted) throw error;
   throw new Error('پاسخ سرور معتبر نیست؛ اتصال و پراکسی را بررسی کنید.');
  }
  if (!res.ok) throw new Error(data && typeof data === 'object' && 'error' in data ? String(data.error) : `Request failed (${res.status})`);
  return data as T;
 } catch (error) {
  if (controller.signal.aborted) throw new Error('سرور در مهلت مقرر پاسخ نداد؛ اتصال را بررسی و دوباره تلاش کنید.');
  if (error instanceof TypeError) throw new Error('ارتباط با سرور برقرار نشد؛ اتصال و پراکسی را بررسی کنید.');
  throw error;
 } finally { clearTimeout(timeout); options.signal?.removeEventListener('abort', abort); }
}

export const api = {
 checkAuth: () => request<{ needs_setup: boolean }>('/auth/check'),
 login: (username: string, password: string) => request<{ token: string; username: string }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
 me: () => request<{ id: number; username: string }>('/auth/me'),
 dashboard: () => request<Dashboard>('/dashboard'), system: () => request<System>('/system'),
 listInstances: () => request<(TunnelInstance & { recovery?: RecoveryState })[]>('/instances'),
 createInstance: (data: unknown) => request<TunnelInstance>('/instances', { method: 'POST', body: JSON.stringify(data) }),
 getInstance: (id: number) => request<{ instance: TunnelInstance; status: string; uptime: number; error?: string; telemetry: Telemetry; recovery?: RecoveryState }>(`/instances/${id}`),
 updateInstance: (id: number, data: unknown) => request<TunnelInstance>(`/instances/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
 deleteInstance: (id: number) => request<{ ok: boolean }>(`/instances/${id}`, { method: 'DELETE' }),
 instanceStart: (id: number) => request<{ status: string }>(`/instances/${id}/start`, { method: 'POST' }),
 instanceStop: (id: number) => request<{ status: string }>(`/instances/${id}/stop`, { method: 'POST' }),
 instanceRestart: (id: number) => request<{ status: string }>(`/instances/${id}/restart`, { method: 'POST' }),
 instanceStatus: (id: number) => request<{ status: string; uptime: number; error?: string; telemetry: Telemetry; recovery?: RecoveryState }>(`/instances/${id}/status`),
 listForwards: (id: number) => request<ForwardStatus[]>(`/instances/${id}/forwards`),
 createForward: (id: number, data: { name: string; protocol: string; listen_port: number; target_port: number; enabled: boolean }) => request<ForwardStatus['rule']>(`/instances/${id}/forwards`, { method: 'POST', body: JSON.stringify(data) }),
 deleteForward: (id: number, ruleId: number) => request<{ ok: boolean }>(`/instances/${id}/forwards/${ruleId}`, { method: 'DELETE' }),
 getInstanceSpoofIPs: (id: number) => request<{ content: string; count: number }>(`/instances/${id}/spoof-ips`),
 setInstanceSpoofIPs: (id: number, content: string) => request<{ ok: boolean; count: number; instance: TunnelInstance }>(`/instances/${id}/spoof-ips`, { method: 'PUT', body: JSON.stringify({ content }) }),
 pair: (data: unknown) => request<{ local: End; remote: End; saved: End | null }>('/pairs', { method: 'POST', body: JSON.stringify(data) }),
 testerStart: (data: unknown) => request<{ status: string; ip_count: number }>('/tester/start', { method: 'POST', body: JSON.stringify(data) }),
 testerStatus: () => request<TesterState>('/tester/status'), testerStop: () => request<{ status: string }>('/tester/stop', { method: 'POST' }),
 testerResults: () => request<{ status: string; results: TesterResult[] }>('/tester/results'),
 testerDownload: async (): Promise<Blob> => {
  const token = getToken(); const res = await fetch(`${apiBase()}/tester/download`, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!res.ok) throw new Error('دریافت خروجی آزمون ناموفق بود.'); return res.blob();
 },
 testerUpload: async (file: File): Promise<{ content: string }> => {
  const formData = new FormData(); formData.append('file', file); const token = getToken();
  const res = await fetch(`${apiBase()}/tester/upload`, { method: 'POST', headers: token ? { Authorization: `Bearer ${token}` } : {}, body: formData });
  if (res.status === 401) { clearToken(); window.location.replace(getBasePath() + '/login/'); }
  if (!res.ok) throw new Error('بارگذاری ناموفق بود؛ حجم فایل و اتصال را بررسی کنید.'); return res.json();
 },
 changePassword: (oldPassword: string, newPassword: string) => request<{ ok: boolean; token: string }>('/settings/password', { method: 'PUT', body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }) }),
};
