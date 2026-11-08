"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import Link from "@/lib/link";
import { api, type RecoveryState } from "@/lib/api";
import { RecoveryNotice } from "@/lib/recovery";
import { useBasePath } from "@/lib/basepath";
import { useToast } from "@/lib/toast";
import { IconRestart, IconPlay, IconStop, IconSettings, IconTunnels } from "@/lib/icons";

type Telemetry = {
 source_rejected?: number; last_rejected_source?: string; invalid_packets?: number; mtu_rejected?: number; clock_rejected?: number; clock_skew_seconds?: number;
  peer_connected: boolean;
  tx_bytes: number;
  rx_bytes: number;
  tx_packets?: number;
  rx_packets?: number;
  drops: number;
  last_peer: number;
  rtt_ms?: number;
  jitter_ms?: number;
};

type Instance = {
 recovery?: RecoveryState;
  id: number;
  name: string;
  mode: string;
  status: string;
  uptime: number;
  error?: string;
  engine: string;
  telemetry?: Telemetry;
};

type Dash = {
  total: number;
  running_count: number;
  stopped_count: number;
  error_count: number;
  instances: Instance[] | null;
};

type System = { hostname: string; os: string; arch: string; memory_mb: number; cpus: number; go_version?: string; panel_version?: string };

const STATUS_LABEL: Record<string, string> = {
  running: "در حال اجرا",
  stopped: "متوقف",
  starting: "در حال شروع",
  stopping: "در حال توقف",
  error: "خطا",
};

function bytes(n = 0): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : 1)} ${units[i]}`;
}

function uptime(seconds = 0): string {
  if (seconds <= 0) return "—";
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h > 0 ? `${h} ساعت ${m} دقیقه` : `${m} دقیقه`;
}

const fa = (n: number) => n.toLocaleString("fa-IR");

export default function Dashboard() {
  const [dash, setDash] = useState<Dash | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [sys, setSys] = useState<System | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<number | null>(null);
  const [updated, setUpdated] = useState("");
  const base = useBasePath();
  const { toast } = useToast();
  const lastPollRef = useRef("");
 const requestId = useRef(0);
 const fetching = useRef(false);

  const refresh = useCallback(async (force = false) => {
 if (fetching.current && !force) return;
 const ticket = ++requestId.current; fetching.current = true;
    try {
      const [d, s] = await Promise.all([api.dashboard(), api.system()]);
 if (ticket !== requestId.current) return;
      /* Polling every 3.5s used to re-render the whole page even when
         nothing changed (new objects every time, plus a new timestamp).
         Skip the render when the payload is identical; live data still
         updates the moment tx/rx, status or uptime actually changes. */
      const key = JSON.stringify([d, s]);
      setError("");
 setUpdated(new Date().toLocaleTimeString("fa-IR"));
      if (lastPollRef.current === key) return;
      lastPollRef.current = key;
      setSelectedId(current => current !== null && !(d.instances ?? []).some(instance => instance.id === current) ? null : current);
      setDash(d);
      setSys(s);
      setError("");
      setUpdated(new Date().toLocaleTimeString("fa-IR"));
    } catch (e) {
      if (ticket === requestId.current) setError(e instanceof Error ? e.message : "دریافت وضعیت ناموفق بود");
    } finally { if (ticket === requestId.current) fetching.current = false; }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, 3500);
    return () => clearInterval(t);
  }, [refresh]);

  async function toggle(i: Instance) {
    setBusy(i.id);
    try {
      if (i.status === "running") {
        await api.instanceStop(i.id);
        toast.info(`اتصال «${i.name}» متوقف شد.`);
      } else {
        await api.instanceStart(i.id);
        toast.success(`اتصال «${i.name}» راه‌اندازی شد.`);
      }
      await refresh(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "عملیات ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally {
      setBusy(null);
    }
  }

  const instances = dash?.instances ?? [];
  const primary = instances.find(i => i.id === selectedId) || instances.find(i => i.engine === "tun" && i.status === "running") || instances[0];
  const primaryIndex = instances.findIndex(i => i.id === primary?.id);
  function selectNeighbour(direction: number) {
    if (instances.length < 2) return;
    setSelectedId(instances[(primaryIndex + direction + instances.length) % instances.length].id);
  }
  const primaryTelemetry = primary?.telemetry;
  const totalTraffic = instances.reduce((sum, instance) => ({ tx: sum.tx + (instance.telemetry?.tx_bytes || 0), rx: sum.rx + (instance.telemetry?.rx_bytes || 0) }), { tx: 0, rx: 0 });
  const isPeerConnected = primary?.engine === "tun" && !!primaryTelemetry?.peer_connected;

  return (
    <div className="page">
      {/* Page Header */}
      <div className="page-head">
        <div>
          <div className="eyebrow">NETWORK OPERATIONS / 01</div>
          <h1>کنترل مسیر، در یک نگاه.</h1>
          <p className="page-intro">
            مانیتورینگ بلادرنگ تونل‌های لایهٔ ۳، وضعیت احراز هویت بسته‌ها، پینگ زنده و ترافیک عبوری.
          </p>
        </div>
        <div className="row">
          <button className="btn btn-ghost" onClick={() => { void refresh(true); toast.info("اطلاعات به‌روزرسانی شد."); }}>
            <IconRestart size={14} />
            <span>به‌روزرسانی</span>
          </button>
          <Link className="btn btn-primary" href={base + "/dashboard/pair"}>
            + ساخت اتصال دوطرفه
          </Link>
        </div>
      </div>

      {error && (
        <div role="alert" className="banner error">
          <span>{error}</span>
          <button className="btn btn-ghost btn-sm" onClick={() => void refresh(true)}>تلاش دوباره</button>
        </div>
      )}

      {/* Top Bento Showcase Grid */}
      <div className="grid-2-1">
        {/* Bento Hero Card */}
        <section className="panel bento-hero" aria-labelledby="link-health-title">
          <div className="section-head">
            <div>
              <span className="eyebrow" style={{ color: "var(--accent-cyan)" }}>SELECTED LINK HEALTH</span>
              <h2 id="link-health-title">{primary ? primary.name : "وضعیت پیوند شبکه"}</h2>
            </div>
            <span className={`pill ${isPeerConnected ? "ok" : primary?.status === "running" ? "warn" : "muted"}`}>
              {isPeerConnected ? "پاسخ معتبر دریافت شد" : primary?.status === "running" ? "در انتظار پاسخ طرف مقابل" : "پیوند خاموش"}
            </span>
          </div>

          {instances.length > 0 && (
            <div role="group" aria-label="انتخاب اتصال برای نمایش وضعیت" style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, marginBottom: 12 }}>
              <button type="button" className="btn btn-ghost btn-sm" aria-label="اتصال قبلی" disabled={instances.length < 2} onClick={() => selectNeighbour(-1)}>→ قبلی</button>
              <span className="hint" role="status" aria-live="polite">اتصال {fa(primaryIndex + 1)} از {fa(instances.length)}</span>
              <button type="button" className="btn btn-ghost btn-sm" aria-label="اتصال بعدی" disabled={instances.length < 2} onClick={() => selectNeighbour(1)}>بعدی ←</button>
            </div>
          )}

          <div style={{ display: "flex", alignItems: "center", gap: 24, padding: "12px 0", flexWrap: "wrap" }}>
            <div style={{ display: "flex", alignItems: "center", gap: 14 }}>
              <div style={{
                width: 48, height: 48, borderRadius: "50%",
                background: isPeerConnected ? "rgba(34, 197, 94, 0.15)" : "rgba(148, 163, 184, 0.1)",
                border: `2px solid ${isPeerConnected ? "#22c55e" : "rgba(255,255,255,0.1)"}`,
                display: "grid", placeItems: "center",
                boxShadow: isPeerConnected ? "0 0 25px rgba(34, 197, 94, 0.4)" : "none",
              }}>
                <span style={{ fontSize: 20 }}>{isPeerConnected ? "✓" : "●"}</span>
              </div>
              <div>
                <div style={{ fontSize: 13, color: "var(--text-muted)" }}>پروتکل رمزنگاری</div>
                <div style={{ fontSize: 15, fontWeight: 700, color: "var(--text)" }}>AES-256-GCM (لایهٔ ۳)</div>
              </div>
            </div>

            <div style={{ height: 36, width: 1, background: "var(--border-subtle)" }} className="hide-mobile" />

            <div>
              <div style={{ fontSize: 13, color: "var(--text-muted)" }}>تأخیر واقعی (RTT)</div>
              <div className="mono" style={{ fontSize: 20, fontWeight: 800, color: isPeerConnected ? "var(--accent-cyan)" : "var(--text-muted)" }}>
                {isPeerConnected && primaryTelemetry?.rtt_ms ? `${fa(Math.round(primaryTelemetry.rtt_ms))} ms${primaryTelemetry.jitter_ms ? ` (±${fa(Math.round(primaryTelemetry.jitter_ms))})` : ""}` : "—"}
              </div>
            </div>

            <div style={{ height: 36, width: 1, background: "var(--border-subtle)" }} className="hide-mobile" />

            <div>
              <div style={{ fontSize: 13, color: "var(--text-muted)" }}>کل بسته‌های عبوری</div>
              <div className="mono" style={{ fontSize: 20, fontWeight: 800 }}>
                {primaryTelemetry ? fa((primaryTelemetry.tx_packets || 0) + (primaryTelemetry.rx_packets || 0)) : "۰"}
              </div>
            </div>
          </div>

          <RecoveryNotice state={primary?.recovery} />
          {!!primaryTelemetry?.clock_skew_seconds && <div role="alert" className="banner error">زمان فریم احرازشدهٔ طرف مقابل خارج از بازه است (اختلاف تقریبی {fa(Math.round(primaryTelemetry.clock_skew_seconds))} ثانیه). همگامی ساعت دو سرور و تأخیر مسیر را بررسی کن.</div>}
          <p className="hint">فیلتر مبدأ: {fa(primaryTelemetry?.source_rejected ?? 0)} · رد فرمت: {fa(primaryTelemetry?.invalid_packets ?? 0)} · رد MTU: {fa(primaryTelemetry?.mtu_rejected ?? 0)} · رد زمان: {fa(primaryTelemetry?.clock_rejected ?? 0)}</p>
          <p className="hint">آخرین مبدأ ردشده: {primaryTelemetry?.last_rejected_source || "—"}. شمارندهٔ فیلتر مبدأ شامل ترافیک نامرتبط اتصال‌های دیگر هم می‌شود؛ معادل افت مسیر نیست.</p>
          <div style={{ fontSize: 12.5, color: "var(--text-muted)", borderTop: "1px solid var(--border-subtle)", paddingTop: 12, display: "flex", justifyContent: "space-between", flexWrap: "wrap", gap: 10 }}>
            <span>ضربان قلب احرازشده: <strong>هر ۳ ثانیه</strong></span>
            <span>پنجرهٔ ضدبازپخش: <strong>۱۰۲۴ بسته</strong></span>
            <span>حذف محلی بسته: <strong>{primaryTelemetry?.drops ? `${fa(primaryTelemetry.drops)} drop` : "صفر"}</strong></span>
          </div>
        </section>

        {/* Live Speed & Flow Bento */}
        <section className="panel" style={{ justifyContent: "space-between" }}>
          <div>
            <div className="eyebrow">THROUGHPUT VOLUME</div>
            <h2>ترافیک کل</h2>
            <p className="hint">مجموع حجم اتصال‌ها در اجرای فعلی؛ با شروع دوبارهٔ هر اتصال، شمارندهٔ آن صفر می‌شود.</p>
          </div>

          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16, padding: "8px 0" }}>
            <div style={{ background: "var(--card-2)", padding: 14, borderRadius: 12, border: "1px solid var(--border-subtle)" }}>
              <div style={{ fontSize: 12, color: "var(--text-muted)", marginBottom: 4 }}>↑ ارسال (TX)</div>
              <div className="mono" style={{ fontSize: 22, fontWeight: 800, color: "var(--text)" }}>
                {bytes(totalTraffic.tx)}
              </div>
            </div>
            <div style={{ background: "var(--card-2)", padding: 14, borderRadius: 12, border: "1px solid var(--border-subtle)" }}>
              <div style={{ fontSize: 12, color: "var(--text-muted)", marginBottom: 4 }}>↓ دریافت (RX)</div>
              <div className="mono" style={{ fontSize: 22, fontWeight: 800, color: "var(--accent-cyan)" }}>
                {bytes(totalTraffic.rx)}
              </div>
            </div>
          </div>

          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", fontSize: 12, color: "var(--text-muted)" }}>
            <span>وضعیت: {updated ? `به‌روزرسانی ${updated}` : "در حال دریافت…"}</span>
            <span style={{ color: "var(--accent-cyan)" }}>● زنده</span>
          </div>
        </section>
      </div>

      {/* 4 Stats Cards Bento */}
      <div className="stats">
        {[
          ["کل اتصال‌ها", dash?.total, "01"],
          ["پردازش‌های فعال", dash?.running_count, "02"],
          ["اتصال‌های متوقف", dash?.stopped_count, "03"],
          ["نیازمند بررسی", dash?.error_count, "04"],
        ].map(([label, value, idx]) => (
          <div className="stat" key={String(idx)}>
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
              <span className="stat-label">{label}</span>
              <span className="mono" style={{ fontSize: 11, color: "var(--accent-cyan)", opacity: 0.8 }}>#{idx}</span>
            </div>
            <strong className="stat-value">{value === undefined ? "—" : fa(Number(value))}</strong>
          </div>
        ))}
      </div>

      {/* Main Connection Table Bento */}
      <section className="panel">
        <div className="section-head">
          <div>
            <h2>اتصال‌های فعال و پیکربندی‌شده</h2>
            <p className="hint">فهرست تونل‌های مستقل تعریف‌شده روی این سرور.</p>
          </div>
          <span className="live-label">
            <span className={`status-dot ${error ? "stopped" : "running"}`} />
            {instances.length} تونل ثبت‌شده
          </span>
        </div>

        {!dash && !error && <p className="muted">در حال دریافت وضعیت سرور…</p>}

        {dash && instances.length === 0 && (
          <div className="empty">
            <div className="empty-icon"><IconTunnels size={42} /></div>
            <h3>هنوز اتصالی پیکربندی نشده است</h3>
            <p>مشخصات دو سرور را وارد کن تا تنظیمات مکمل و کلید رمزنگاری تولید شوند.</p>
            <div className="row center" style={{ marginTop: 8 }}>
              <Link className="btn btn-primary" href={base + "/dashboard/pair"}>ساخت اولین اتصال دوطرفه</Link>
              <Link className="btn btn-ghost" href={base + "/dashboard/tunnels"}>ورود فایل تنظیمات</Link>
            </div>
          </div>
        )}

        {instances.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>نام اتصال / نقش</th>
                  <th>وضعیت پروسه</th>
                  <th>ارتباط دوطرفه</th>
                  <th>ترافیک ارسالی / دریافتی</th>
                  <th>تأخیر واقعی (RTT)</th>
                  <th>زمان اجرا</th>
                  <th>کنترل سریع</th>
                </tr>
              </thead>
              <tbody>
                {instances.map((i) => {
                  const t = i.telemetry;
                  const peer = i.engine === "tun" ? !!t?.peer_connected : false;
                  return (
                    <tr key={i.id}>
                      <td>
                        <Link className="row-link" href={base + `/dashboard/tunnels/edit?id=${i.id}`}><bdi dir="auto">{i.name}</bdi></Link>
                        <div className="tunnel-tags">
                          <span className={`mode-badge ${i.mode}`}><bdi>{i.mode === "local" ? "سرور ایران" : "سرور خارج"}</bdi><span className="mode-sub mono" dir="ltr">({i.mode})</span></span>
                          <span className="engine-badge mono" dir="ltr">{i.engine === "tun" ? "TUN L3" : "UDP Relay"}</span>
                        </div>
                      </td>
                      <td>
                        <span className={`pill ${i.status}`}>{STATUS_LABEL[i.status] || i.status}</span>
                        {i.error && <div className="inline-error" title={i.error}>{i.error}</div>}
                        <RecoveryNotice state={i.recovery} />
                      </td>
                      <td>
                        {i.engine !== "tun" ? (
                          <span className="pill muted">رله قدیمی</span>
                        ) : peer ? (
                          <span className="pill ok">پاسخ معتبر ✓</span>
                        ) : (
                          <span className={`pill ${i.status === "running" ? "warn" : "muted"}`}>
                            {i.status === "running" ? "در انتظار پاسخ…" : "—"}
                          </span>
                        )}
                      </td>
                      <td className="mono" dir="ltr">
                        ↑ {bytes(t?.tx_bytes)}<br />↓ {bytes(t?.rx_bytes)}
                      </td>
                      <td className="mono">{peer && t?.rtt_ms ? `${fa(Math.round(t.rtt_ms))} ms${t.jitter_ms ? ` (±${fa(Math.round(t.jitter_ms))})` : ""}` : "—"}</td>
                      <td className="mono">{uptime(i.uptime)}</td>
                      <td>
                        <div className="row">
                          <button
                            className={`btn btn-sm ${i.status === "running" ? "btn-ghost" : "btn-primary"}`}
                            disabled={busy === i.id || i.status === "stopping" || i.status === "starting"}
                            onClick={() => toggle(i)}
                          >
                            {busy === i.id ? (
                              "…"
                            ) : i.status === "running" ? (
                              <>
                                <IconStop size={12} />
                                <span>توقف</span>
                              </>
                            ) : (
                              <>
                                <IconPlay size={12} />
                                <span>راه‌اندازی</span>
                              </>
                            )}
                          </button>
                          <Link className="btn btn-ghost btn-sm" href={base + `/dashboard/tunnels/edit?id=${i.id}`}>
                            <IconSettings size={13} />
                            <span>تنظیمات</span>
                          </Link>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Bottom Bento Row: Route Flow & System Specs */}
      <div className="grid-2-1">
        <section className="route-card">
          <div className="eyebrow">DYNAMIC SPOOF ROUTING</div>
          <h2>جریان فریم‌های رمزشده در شبکه</h2>
          <div className="path-flow" dir="ltr">
            <div className="path-node"><span>TCP / UDP / ICMP</span></div>
            <div className="path-arrow">➔</div>
            <div className="path-node" style={{ borderColor: "rgba(99, 102, 241, 0.4)", color: "var(--accent-hover)" }}>
              <span>Local TUN</span>
            </div>
            <div className="path-arrow">⇄</div>
            <div className="path-node" style={{ borderColor: "rgba(88, 217, 194, 0.4)", color: "var(--accent-cyan)" }}>
              <span>Remote TUN</span>
            </div>
            <div className="path-arrow">➔</div>
            <div className="path-node"><span>سرویس مقصد</span></div>
          </div>
          <p style={{ color: "var(--text-muted)", fontSize: 13, lineHeight: 1.9 }}>
            آدرس مبدأ در سطح لایهٔ شبکه با IPهای جعلی در حال گردش جایگزین می‌شود، در حالی که مقصد همیشه IP واقعی سرور است.
            تمام بسته‌های داخلی به صورت سرتاسری با استاندارد <strong>AES-256-GCM</strong> رمزگذاری می‌شوند. عبور بسته‌ها به سیاست و فیلترهای شبکهٔ مسیر بستگی دارد.
          </p>
        </section>

        <section className="panel">
          <div className="eyebrow">NODE TELEMETRY</div>
          <h2>مشخصات سرور میزبان</h2>
          <dl className="host-info" style={{ marginTop: 8 }}>
            <dt>نسخهٔ پنل</dt><dd className="mono" dir="ltr">{sys?.panel_version || "—"}</dd>
            <dt>نام هاست</dt><dd className="mono" dir="ltr">{sys?.hostname || "—"}</dd>
            <dt>سیستم‌عامل</dt><dd className="mono" dir="ltr">{sys ? `${sys.os} / ${sys.arch}` : "—"}</dd>
            <dt>پردازنده</dt><dd>{sys?.cpus ? `${fa(sys.cpus)} هسته` : "—"}</dd>
            <dt>حافظهٔ پنل</dt><dd className="mono" dir="ltr">{sys ? `${sys.memory_mb} MB` : "—"}</dd>
            <dt>ران‌تایم</dt><dd className="mono" dir="ltr">{sys?.go_version || "Go"}</dd>
          </dl>
        </section>
      </div>
    </div>
  );
}
