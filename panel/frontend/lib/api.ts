import { getBasePath } from "./basepath";
export type Telemetry = {
  peer_connected: boolean;
  source_rejected?: number;
  last_rejected_source?: string;
  invalid_packets?: number;
  mtu_rejected?: number;
  clock_rejected?: number;
  clock_skew_seconds?: number;
  tx_bytes: number;
  rx_bytes: number;
  tx_packets: number;
  rx_packets: number;
  drops: number;
  last_peer: number;
  rtt_ms?: number;
};
export type RecoveryState = {
  active: boolean;
  attempts: number;
  max_attempts: number;
  next_retry_at: number;
  exhausted: boolean;
};
export type TunnelInstance = {
  id: number;
  name: string;
  mode: string;
  engine: string;
  enabled: boolean;
  status: string;
  uptime: number;
  error?: string;
  status_error?: string;
  send_transport: string;
  recv_transport: string;
  remote_addr: string;
  client_ip: string;
  local_ip: string;
  tun_interface: string;
  tun_address: string;
  tun_peer: string;
  tun_mtu: number;
  spoof_ip: string;
  peer_spoof_ip: string;
  spoof_ip_list: string;
  peer_spoof_ip_list: string;
  remote_port: number;
  recv_port: number;
  listen_port: number;
  client_port: number;
  psk: string;
  updated_at?: string;
};
export type TesterResult = {
  ip: string;
  received: number;
  sent: number;
  loss_pct: number;
  passed: boolean;
};
export type TesterState = {
  test_id?: string;
  rejected_probes?: number;
  duplicate_probes?: number;
  status: string;
  mode: string;
  error?: string;
  progress: number;
  total_ips: number;
  passed_count: number;
  recv_count: number;
  results: TesterResult[];
  preview_limit?: number;
};
type ForwardStatus = {
  rule: {
    id: number;
    name: string;
    protocol: string;
    listen_port: number;
    target_port: number;
    enabled: boolean;
  };
  target: string;
  listening: boolean;
  stats: {
    listening: boolean;
    connections: number;
    bytes_up: number;
    bytes_down: number;
    last_error: string;
  };
};
type System = {
  hostname: string;
  os: string;
  arch: string;
  cpus: number;
  memory_mb: number;
  go_version: string;
  panel_version: string;
};
type Dashboard = {
  total: number;
  running_count: number;
  stopped_count: number;
  error_count: number;
  instances:
    | (TunnelInstance & { telemetry: Telemetry; recovery?: RecoveryState })[]
    | null;
};
type End = Record<string, string | number | boolean>;
function apiBase() {
  return typeof window === "undefined"
    ? "/api"
    : `${window.location.origin}${getBasePath()}/api`;
}
function getToken() {
  return typeof window === "undefined" ? null : localStorage.getItem("token");
}
export function setToken(token: string) {
  localStorage.setItem("token", token);
}
export function clearToken() {
  localStorage.removeItem("token");
}
async function request<T>(
  path: string,
  options: RequestInit = {},
  timeoutMs = 15000,
): Promise<T> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  const abort = () => controller.abort();
  options.signal?.addEventListener("abort", abort, { once: true });
  if (options.signal?.aborted) controller.abort();
  try {
    const headers = new Headers(options.headers);
    if (!(options.body instanceof FormData))
      headers.set("Content-Type", "application/json");
    const token = getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const res = await fetch(`${apiBase()}${path}`, {
      ...options,
      headers,
      signal: controller.signal,
    });
    if (res.status === 401 && path !== "/auth/login") {
      clearToken();
      window.location.replace(getBasePath() + "/login/");
      throw new Error("نشست شما منقضی شده است؛ دوباره وارد شوید.");
    }
    let data: unknown;
    try {
      data = await res.json();
    } catch (error) {
      if (controller.signal.aborted) throw error;
      throw new Error("پاسخ سرور معتبر نیست؛ اتصال و پراکسی را بررسی کنید.");
    }
    if (!res.ok)
      throw new Error(
        data && typeof data === "object" && "error" in data
          ? String(data.error)
          : `Request failed (${res.status})`,
      );
    return data as T;
  } catch (error) {
    if (controller.signal.aborted)
      throw new Error(
        "سرور در مهلت مقرر پاسخ نداد؛ اتصال را بررسی و دوباره تلاش کنید.",
      );
    if (error instanceof TypeError)
      throw new Error(
        "ارتباط با سرور برقرار نشد؛ اتصال و پراکسی را بررسی کنید.",
      );
    throw error;
  } finally {
    clearTimeout(timeout);
    options.signal?.removeEventListener("abort", abort);
  }
}

export type PoolFile = {
  kind?: "default" | "file";
  id: number;
  name: string;
  count: number;
  enabled: boolean;
  position: number;
  hash: string;
};
export type PoolFiles = {
  files: PoolFile[];
  mode: string;
  probe_pps: number;
  active_size?: number;
  refresh_seconds?: number;
  auto_peer_sync: boolean;
  total: number;
  peer_count: number;
  legacy_content: boolean;
  updated_at: string;
};
export type SourceQuality = {
  selected?: boolean;
  reason?: string;
  ip: string;
  state: string;
  rtt_ms: number;
  loss_pct: number;
  probes: number;
  replies: number;
  failures: number;
  retry_at?: number;
  last_success?: number;
};
export type PoolState = {
  available: boolean;
  reason?: string;
  runtime?: {
    persistence_error?: string;
    peer_ready: boolean;
    control_fresh: boolean;
    our_sources_accepted: boolean;
    pool: {
      selected?: number;
      active_size?: number;
      refresh_at?: number;
      generation?: number;
      rebuilding?: boolean;
      mode: string;
      total: number;
      healthy: number;
      testing: number;
      quarantined: number;
      unknown: number;
      active?: string;
      offset: number;
      items: SourceQuality[];
    };
    peer: {
      in_flight?: boolean;
      supported: boolean;
      status: string;
      error?: string;
      count: number;
      revision?: string;
      progress: number;
    };
  };
};
export type UpdateStatus = {
  current: string;
  latest?: string;
  release_url?: string;
  supported: boolean;
  reason?: string;
  available: boolean;
  checked_at?: string;
  job?: {
    id: string;
    from: string;
    target: string;
    phase: string;
    progress: number;
    bytes_done?: number;
    bytes_total?: number;
    error?: string;
  };
};
export type BenchPreset = {
  id: number;
  name: string;
  role: string;
  local_ip: string;
  peer_ip: string;
  source: string;
  peer_source: string;
  source_verified: boolean;
  peer_learned: boolean;
  receive_port: number;
  send_port: number;
  subnet: string;
  updated_at: string;
};
export type BenchDirection = {
  sent: number;
  received: number;
  send_errors: number;
  seconds: number;
  mbps: number;
  loss_pct: number;
  capped: boolean;
};
export type BenchResult = {
  index: number;
  send: string;
  receive: string;
  status: string;
  error?: string;
  rtt_ms: number;
  p95_ms: number;
  ping_sent: number;
  ping_replies: number;
  upload: BenchDirection;
  download: BenchDirection;
  score: number;
};
export type BenchState = {
  endpoints?: {
    controller_ip: string;
    peer_ip: string;
    controller_source: string;
    peer_source: string;
    controller_port: number;
    peer_port: number;
  };
  role: string;
  status: string;
  phase: string;
  current: number;
  progress: number;
  expires: number;
  options: { rate_mbps: number; seconds: number };
  results: BenchResult[];
  recommended: number;
  error?: string;
  applied: boolean;
};
export type BenchPreview = {
  token: string;
  index: number;
  name: string;
  replace: boolean;
  target_id: number;
  remote_target_id: number;
  send: string;
  receive: string;
  subnet: string;
  expires: number;
};
export const api = {
  benchmarkPreset: (id: number, signal?: AbortSignal) =>
    request<BenchPreset>(`/instances/${id}/benchmark-presets`, { signal }),
  benchmarkStatus: (signal?: AbortSignal) =>
    request<BenchState>("/connection-test", { signal }),
  benchmarkReceiver: (data: unknown) =>
    request<{ code: string; state: BenchState }>(
      "/connection-test/receiver",
      { method: "POST", body: JSON.stringify(data) },
      30000,
    ),
  benchmarkStart: (data: unknown) =>
    request<BenchState>(
      "/connection-test/start",
      { method: "POST", body: JSON.stringify(data) },
      30000,
    ),
  benchmarkStop: () =>
    request<BenchState>("/connection-test/stop", { method: "POST" }, 45000),
  benchmarkPreview: (index: number) =>
    request<BenchPreview>(
      "/connection-test/preview",
      { method: "POST", body: JSON.stringify({ index }) },
      45000,
    ),
  benchmarkApply: (token: string) =>
    request<BenchState>(
      "/connection-test/apply",
      { method: "POST", body: JSON.stringify({ token }) },
      30000,
    ),
  updateStatus: (signal?: AbortSignal) =>
    request<UpdateStatus>("/updates", { signal }),
  updateCheck: () =>
    request<UpdateStatus>("/updates/check", { method: "POST" }, 30000),
  updateInstall: (version: string) =>
    request<UpdateStatus>(
      "/updates/install",
      { method: "POST", body: JSON.stringify({ version }) },
      30000,
    ),
  poolDefault: (id: number) =>
    request<{ content: string; count: number; updated_at: string }>(
      `/instances/${id}/pools/default`,
      {},
      120000,
    ),
  poolDefaultSave: (
    id: number,
    content: string,
    version: string,
    settings?: {
      ids: number[];
      mode: string;
      probe_pps: number;
      auto_peer_sync: boolean;
    },
  ) => {
    const data = new FormData();
    data.append(
      "file",
      new Blob([content], { type: "text/plain" }),
      "default.txt",
    );
    data.append("updated_at", version);
    if (settings) data.append("settings", JSON.stringify(settings));
    return request<{ count: number; total: number; updated_at: string }>(
      `/instances/${id}/pools/default`,
      { method: "POST", body: data },
      120000,
    );
  },
  poolFiles: (id: number, signal?: AbortSignal) =>
    request<PoolFiles>(`/instances/${id}/pools`, { signal }),
  poolUpload: (id: number, file: File) => {
    const data = new FormData();
    data.append("file", file);
    return request<PoolFile>(
      `/instances/${id}/pools/upload`,
      { method: "POST", body: data },
      120000,
    );
  },
  poolMigrate: (id: number) =>
    request<PoolFile>(`/instances/${id}/pools/migrate`, { method: "POST" }),
  poolSettings: (
    id: number,
    data: {
      ids: number[];
      mode: string;
      probe_pps: number;
      active_size?: number;
      refresh_seconds?: number;
      auto_peer_sync: boolean;
      updated_at: string;
    },
  ) =>
    request<{ total: number; updated_at: string }>(
      `/instances/${id}/pools/settings`,
      { method: "PUT", body: JSON.stringify(data) },
    ),
  poolDelete: (id: number, file: number) =>
    request<{ ok: boolean }>(`/instances/${id}/pools/${file}`, {
      method: "DELETE",
    }),
  poolState: (
    id: number,
    offset: number,
    state: string,
    signal?: AbortSignal,
  ) =>
    request<PoolState>(
      `/instances/${id}/pools/state?offset=${offset}&limit=50&state=${encodeURIComponent(state)}`,
      { signal },
    ),
  poolCancelSync: (id: number) =>
    request<{ ok: boolean }>(`/instances/${id}/pools/sync/cancel`, {
      method: "POST",
    }),
  poolSync: (id: number) =>
    request<{ ok: boolean }>(`/instances/${id}/pools/sync`, { method: "POST" }),

  checkAuth: () => request<{ needs_setup: boolean }>("/auth/check"),
  login: (username: string, password: string) =>
    request<{ token: string; username: string }>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  me: () => request<{ id: number; username: string }>("/auth/me"),
  dashboard: () => request<Dashboard>("/dashboard"),
  system: () => request<System>("/system"),
  listInstances: () =>
    request<(TunnelInstance & { recovery?: RecoveryState })[]>("/instances"),
  createInstance: (data: unknown) =>
    request<TunnelInstance>("/instances", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  getInstance: (id: number) =>
    request<{
      instance: TunnelInstance;
      status: string;
      uptime: number;
      error?: string;
      telemetry: Telemetry;
      recovery?: RecoveryState;
    }>(`/instances/${id}?compact=1`),
  updateInstance: (id: number, data: unknown) =>
    request<TunnelInstance>(`/instances/${id}?compact=1`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  deleteInstance: (id: number) =>
    request<{ ok: boolean }>(`/instances/${id}`, { method: "DELETE" }),
  instanceStart: (id: number) =>
    request<{ status: string }>(`/instances/${id}/start`, { method: "POST" }),
  instanceStop: (id: number) =>
    request<{ status: string }>(`/instances/${id}/stop`, { method: "POST" }),
  instanceRestart: (id: number) =>
    request<{ status: string }>(`/instances/${id}/restart`, { method: "POST" }),
  instanceStatus: (id: number) =>
    request<{
      status: string;
      uptime: number;
      error?: string;
      telemetry: Telemetry;
      recovery?: RecoveryState;
    }>(`/instances/${id}/status`),
  listForwards: (id: number) =>
    request<ForwardStatus[]>(`/instances/${id}/forwards`),
  createForward: (
    id: number,
    data: {
      name: string;
      protocol: string;
      listen_port: number;
      target_port: number;
      enabled: boolean;
    },
  ) =>
    request<ForwardStatus["rule"]>(`/instances/${id}/forwards`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  deleteForward: (id: number, ruleId: number) =>
    request<{ ok: boolean }>(`/instances/${id}/forwards/${ruleId}`, {
      method: "DELETE",
    }),
  getInstanceSpoofIPs: (id: number) =>
    request<{ content: string; count: number }>(`/instances/${id}/spoof-ips`),
  setInstanceSpoofIPs: (id: number, content: string) =>
    request<{ ok: boolean; count: number; instance: TunnelInstance }>(
      `/instances/${id}/spoof-ips`,
      { method: "PUT", body: JSON.stringify({ content }) },
    ),
  pair: (data: unknown) =>
    request<{ local: End; remote: End; saved: End | null }>("/pairs", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  testerStart: (data: unknown) =>
    request<{ status: string; ip_count: number }>("/tester/start", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  testerStatus: () => request<TesterState>("/tester/status"),
  testerStop: () =>
    request<{ status: string }>("/tester/stop", { method: "POST" }),
  testerResults: () =>
    request<{ status: string; results: TesterResult[] }>("/tester/results"),
  testerDownload: async (): Promise<Blob> => {
    const token = getToken();
    const res = await fetch(`${apiBase()}/tester/download`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) throw new Error("دریافت خروجی آزمون ناموفق بود.");
    return res.blob();
  },
  testerUpload: async (file: File): Promise<{ content: string }> => {
    const formData = new FormData();
    formData.append("file", file);
    const token = getToken();
    const res = await fetch(`${apiBase()}/tester/upload`, {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      body: formData,
    });
    if (res.status === 401) {
      clearToken();
      window.location.replace(getBasePath() + "/login/");
    }
    if (!res.ok)
      throw new Error("بارگذاری ناموفق بود؛ حجم فایل و اتصال را بررسی کنید.");
    return res.json();
  },
  changePassword: (oldPassword: string, newPassword: string) =>
    request<{ ok: boolean; token: string }>("/settings/password", {
      method: "PUT",
      body: JSON.stringify({
        old_password: oldPassword,
        new_password: newPassword,
      }),
    }),
};
