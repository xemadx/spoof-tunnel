"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import Link from "@/lib/link";
import { Dialog } from "@/lib/dialog";
import { api, type RecoveryState } from "@/lib/api";
import { RecoveryNotice } from "@/lib/recovery";
import { useBasePath } from "@/lib/basepath";
import { useToast } from "@/lib/toast";
import {
  IconPlay,
  IconStop,
  IconRestart,
  IconSettings,
  IconTrash,
  IconUpload,
  IconTunnels,
} from "@/lib/icons";

type Instance = {
 recovery?: RecoveryState;
  id: number;
  name: string;
  mode: string;
  engine: string;
  status: string;
  uptime: number;
  status_error?: string;
  send_transport: string;
  recv_transport: string;
  remote_addr?: string;
  client_ip?: string;
  tun_interface?: string;
  tun_address?: string;
  tun_peer?: string;
  spoof_ip?: string;
  peer_spoof_ip?: string;
  remote_port?: number;
  recv_port?: number;
  listen_port?: number;
  client_port?: number;
  enabled: boolean;
};

type Telemetry = { peer_connected: boolean; tx_bytes: number; rx_bytes: number; rtt_ms?: number; drops: number };

const LABEL: Record<string, string> = {
  running: "در حال اجرا",
  stopped: "متوقف",
  starting: "در حال شروع",
  stopping: "در حال توقف",
  error: "خطا",
};

const CARRIER_LABELS: Record<string, string> = {
  icmp: "ICMP", tcp: "TCP", udp: "UDP", ipip: "IPIP", experimental: "IP 253",
};
function carrierLabel(value: string) { return CARRIER_LABELS[value] || value || "—"; }

function size(n = 0) {
  if (n < 1024) return `${n} B`;
  const u = ["KB", "MB", "GB"];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(1)} ${u[i]}`;
}

function duration(s = 0) {
  if (!s) return "—";
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  return h ? `${h}h ${m}m` : `${m}m`;
}

export default function TunnelsPage() {
  const [instances, setInstances] = useState<Instance[]>([]);
  const [telemetry, setTelemetry] = useState<Record<number, Telemetry>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [toDelete, setToDelete] = useState<Instance | null>(null);
  const closeDelete = useCallback(() => setToDelete(null), []);
  const fileRef = useRef<HTMLInputElement>(null);
  const base = useBasePath();
  const { toast } = useToast();
  const lastLoadRef = useRef("");
 const requestId = useRef(0);
 const fetching = useRef(false);

  const load = useCallback(async (force = false) => {
 if (fetching.current && !force) return;
 const ticket = ++requestId.current; fetching.current = true;
    try {
      const [list, dash] = await Promise.all([api.listInstances(), api.dashboard()]);
 if (ticket !== requestId.current) return;
      const map: Record<number, Telemetry> = {};
      for (const d of dash.instances ?? []) if (d.telemetry) map[d.id] = d.telemetry;
      /* Same polling problem as the dashboard: a 4s interval rebuilt
         new arrays/objects every time and re-rendered the whole list
         even when nothing changed. Skip identical payloads. */
      const key = JSON.stringify([list, map]);
      setError("");
      if (lastLoadRef.current === key) return;
      lastLoadRef.current = key;
      setInstances(list);
      setTelemetry(map);
      setError("");
    } catch (e) {
      if (ticket === requestId.current) setError(e instanceof Error ? e.message : "دریافت فهرست ناموفق بود");
    } finally {
      if (ticket === requestId.current) { fetching.current = false; setLoading(false); }
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(() => {
      if (document.visibilityState === "visible") load();
    }, 4000);
    return () => clearInterval(t);
  }, [load]);

  const action = async (id: number, kind: "start" | "stop" | "restart", name: string) => {
    setBusy(id);
    try {
      if (kind === "start") {
        await api.instanceStart(id);
        toast.success(`اتصال «${name}» با موفقیت راه‌اندازی شد.`);
      } else if (kind === "stop") {
        await api.instanceStop(id);
        toast.info(`اتصال «${name}» متوقف گردید.`);
      } else {
        await api.instanceRestart(id);
        toast.success(`اتصال «${name}» مجدداً راه‌اندازی شد.`);
      }
      await load(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "عملیات ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally {
      setBusy(null);
    }
  };

  const remove = async () => {
    if (!toDelete) return;
    try {
      await api.deleteInstance(toDelete.id);
      toast.success(`اتصال «${toDelete.name}» حذف گردید.`);
      setToDelete(null);
      await load(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "حذف ناموفق بود";
      setError(msg);
      toast.error(msg);
    }
  };

  const importFile = async (file: File) => {
    try {
      const config = JSON.parse(await file.text());
      if (!config || typeof config !== "object") throw new Error("فایل JSON نامعتبر است.");
      delete config.id;
      delete config.created_at;
      delete config.updated_at;
      delete config.status;
      delete config.uptime;
      if (!config.name) config.name = file.name.replace(/\.json$/i, "");
      await api.createInstance(config);
      toast.success(`اتصال «${config.name}» از فایل وارد شد.`);
      await load(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "ورود فایل ناموفق بود";
      setError(msg);
      toast.error(msg);
    }
  };

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">CONNECTIONS / 03</div>
          <h1>مدیریت اتصال‌ها</h1>
          <p className="page-intro">
            هر تونل یک رابط مجازی TUN اختصاصی، کلید رمزنگاری مجزا و زیرشبکهٔ /30 مستقل دارد.
          </p>
        </div>
        <div className="row">
          <button className="btn btn-ghost" onClick={() => fileRef.current?.click()}>
            <IconUpload size={15} />
            <span>ورود فایل JSON</span>
          </button>
          <input
            ref={fileRef}
            type="file"
            accept="application/json,.json"
            hidden
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) void importFile(f);
              e.target.value = "";
            }}
          />
          <Link className="btn btn-primary" href={base + "/dashboard/pair"}>+ ساخت اتصال جدید</Link>
        </div>
      </div>

      {error && (
        <div role="alert" className="banner error">
          <span>{error}</span>
          <button className="btn btn-ghost btn-sm" onClick={() => setError("")}>بستن</button>
        </div>
      )}

      {loading && <p className="muted">در حال دریافت وضعیت…</p>}

      {!loading && instances.length === 0 && (
        <div className="empty panel">
          <div className="empty-icon"><IconTunnels size={42} /></div>
          <h3>هنوز اتصالی ثبت نشده است</h3>
          <p>با «ساخت اتصال جدید» تنظیمات هر دو سمت را به راحتی تولید کن یا فایل JSON موجود را وارد کن.</p>
          <Link className="btn btn-primary" href={base + "/dashboard/pair"}>ساخت اتصال دوطرفه</Link>
        </div>
      )}

      <div className="cards">
        {instances.map((i) => {
          const t = telemetry[i.id];
          const peer = i.engine === "tun" && !!t?.peer_connected;
          const endpoint = i.mode === "local" ? i.remote_addr : i.client_ip;
          return (
            <article className="panel conn-card" key={i.id}>
              <header className="conn-head">
                <div className="conn-identity">
                  <span className={`status-dot ${i.status}`} aria-hidden="true" />
                  <Link className="conn-title" href={`${base}/dashboard/tunnels/edit?id=${i.id}`}><bdi dir="auto">{i.name}</bdi></Link>
                </div>
                <span className={`pill ${i.status}`}>{LABEL[i.status] || i.status}</span>
              </header>

              <dl className="conn-meta" aria-label="مشخصات اتصال">
                <div className={`conn-meta-item mode-badge ${i.mode}`}><dt>سمت</dt><dd>{i.mode === "local" ? "ایران" : "خارج"}</dd></div>
                <div className="conn-meta-item"><dt>ارسال</dt><dd className="mono"><bdi dir="ltr">{carrierLabel(i.send_transport)}</bdi></dd></div>
                <div className="conn-meta-item"><dt>دریافت</dt><dd className="mono"><bdi dir="ltr">{carrierLabel(i.recv_transport)}</bdi></dd></div>
                {i.engine === "tun" && <div className="conn-meta-item"><dt>رابط</dt><dd className="mono"><bdi dir="ltr">{i.tun_interface || "—"}</bdi></dd></div>}
              </dl>

              <div className="conn-grid">
                <div><span className="k">سرور طرف مقابل</span><span className="v mono" dir="ltr">{endpoint || "—"}</span></div>
                <div><span className="k">شبکهٔ داخلی TUN</span><span className="v mono" dir="ltr">{i.tun_address || "—"} ➔ {i.tun_peer || "—"}</span></div>
                <div><span className="k">مبدأ جعلی بسته</span><span className="v mono" dir="ltr">{i.spoof_ip || "—"}</span></div>
                <div><span className="k">پورت‌های رفت/برگشت</span><span className="v mono" dir="ltr">{i.mode === "local" ? `${i.remote_port}/${i.recv_port}` : `${i.listen_port}/${i.client_port}`}</span></div>
              </div>

              {i.status_error && <div className="inline-error">{i.status_error}</div>}
              <RecoveryNotice state={i.recovery} />

              <footer className="conn-foot">
                <div className="conn-telemetry">
                  <span className={peer ? "pill ok" : i.status === "running" ? "pill warn" : "pill muted"}>
                    {i.engine !== "tun" ? "رله قدیمی" : peer ? "پاسخ معتبر ✓" : i.status === "running" ? "در انتظار پاسخ طرف مقابل" : "بدون ارتباط"}
                  </span>
                  <span className="mono" dir="ltr">↑ {size(t?.tx_bytes)}  ↓ {size(t?.rx_bytes)}</span>
                  {peer && t?.rtt_ms ? <span className="mono" style={{ color: "var(--accent-cyan)" }}>{Math.round(t.rtt_ms)} ms</span> : null}
                  <span className="mono" dir="ltr">{duration(i.uptime)}</span>
                </div>
                <div className="row">
                  {i.status === "running" ? (
                    <>
                      <button className="btn btn-ghost btn-sm" disabled={busy === i.id} onClick={() => action(i.id, "stop", i.name)}>
                        <IconStop size={12} />
                        <span>توقف</span>
                      </button>
                      <button className="btn btn-ghost btn-sm" disabled={busy === i.id} onClick={() => action(i.id, "restart", i.name)}>
                        <IconRestart size={12} />
                        <span>راه‌اندازی مجدد</span>
                      </button>
                    </>
                  ) : (
                    <button className="btn btn-primary btn-sm" disabled={busy === i.id} onClick={() => action(i.id, "start", i.name)}>
                      <IconPlay size={12} />
                      <span>{busy === i.id ? "…" : "راه‌اندازی"}</span>
                    </button>
                  )}
                  <Link className="btn btn-ghost btn-sm" href={`${base}/dashboard/tunnels/edit?id=${i.id}`}>
                    <IconSettings size={13} />
                    <span>تنظیمات</span>
                  </Link>
                  <button className="btn btn-ghost btn-sm danger-text" onClick={() => setToDelete(i)} aria-label={`حذف ${i.name}`}>
                    <IconTrash size={13} />
                  </button>
                </div>
              </footer>
            </article>
          );
        })}
      </div>

      {toDelete && (
        <Dialog title="حذف اتصال" onClose={closeDelete}>
            <p className="page-intro" style={{ marginBottom: 20 }}>
              آیا از حذف اتصال «{toDelete.name}» مطمئن هستید؟ فایل‌های تنظیمات محلی پاک خواهند شد.
            </p>
            <div className="row end">
              <button className="btn btn-ghost" onClick={() => setToDelete(null)}>انصراف</button>
              <button className="btn btn-danger" onClick={remove}>حذف قطعی</button>
            </div>
        </Dialog>
      )}
    </div>
  );
}
