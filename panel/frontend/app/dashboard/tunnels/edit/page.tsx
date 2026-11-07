"use client";
import { formatTunnelLog } from "@/lib/tunnel-log";
import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { api, type RecoveryState } from "@/lib/api";
import { PoolManager } from "@/lib/pool-manager";
import { RecoveryNotice } from "@/lib/recovery";
import { getBasePath } from "@/lib/basepath";
import { useToast } from "@/lib/toast";
import { IconPlay, IconStop, IconRestart, IconDownload } from "@/lib/icons";

const CARRIERS = [
  { value: "icmp", label: "ICMP Echo (پیشنهادی)" },
  { value: "tcp", label: "TCP خام (PSH/ACK)" },
  { value: "udp", label: "UDP Datagram" },
  { value: "ipip", label: "پروتکل ۴ (IPIP)" },
  { value: "experimental", label: "پروتکل ۲۵۳ (آزمایشی)" },
];

type Instance = Record<string, string | number | boolean | null | undefined>;

type ForwardStatus = {
  rule: { id: number; name: string; protocol: string; listen_port: number; target_port: number; enabled: boolean };
  target: string;
  listening: boolean;
  stats: { listening: boolean; connections: number; bytes_up: number; bytes_down: number; last_error: string };
};

const STATUS: Record<string, string> = {
  running: "در حال اجرا",
  stopped: "متوقف",
  starting: "در حال شروع",
  stopping: "در حال توقف",
  error: "خطا",
};

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small className="hint">{hint}</small>}
    </label>
  );
}

function EditContent() {
  const params = useSearchParams();
  const id = Number(params.get("id"));
    const { toast } = useToast();

  const [config, setConfig] = useState<Instance | null>(null);
  const [status, setStatus] = useState("stopped");
  const [recovery, setRecovery] = useState<RecoveryState>();
  const [statusError, setStatusError] = useState("");
  const [controlBusy, setControlBusy] = useState(false);
 const requestId = useRef(0);
 const fetching = useRef(false);
 const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [tab, setTab] = useState<"config" | "ips" | "logs" | "forwards">("config");
  const forwardRequest = useRef(0);
 const forwardFetching = useRef(false);
 const [forwards, setForwards] = useState<ForwardStatus[]>([]);
  const [newListen, setNewListen] = useState(443);
  const [newTarget, setNewTarget] = useState(443);
  const [newFwdName, setNewFwdName] = useState("");
 const [forwardBusy, setForwardBusy] = useState(false);
  const [logs, setLogs] = useState<string[]>([]);
  const [wsState, setWsState] = useState<"off" | "live" | "retry">("off");
  const followLog = useRef(true);
 const clearLogQueue=useRef<()=>void>(()=>{});
  const logEnd = useRef<HTMLDivElement>(null);

  const load = useCallback(async (full = true, force = false) => {
    if (!id || (fetching.current && !force)) return;
    const ticket = ++requestId.current; fetching.current = true;
    try {
      let data: { status: string; error?: string; recovery?: RecoveryState };
      if (full) {
        const snapshot = await api.getInstance(id);
        if (ticket !== requestId.current) return;
        setConfig(snapshot.instance);
        data = snapshot;
      } else {
        data = await api.instanceStatus(id);
        if (ticket !== requestId.current) return;
      }
      setRecovery(data.recovery); setStatus(data.status); setStatusError(data.error || "");
    } catch (e) {
      if (ticket === requestId.current) setError(e instanceof Error ? e.message : "دریافت وضعیت اتصال ناموفق بود.");
    } finally { if (ticket === requestId.current) fetching.current = false; }
  }, [id]);

  useEffect(() => {
    if (!id) {
      window.location.replace(getBasePath() + "/dashboard/tunnels/");
      return;
    }
    void load(true);
    const t = setInterval(() => {
      if (document.visibilityState === "visible") void load(false);
    }, 3000);
    return () => clearInterval(t);
  }, [id, load]);


  const loadForwards = useCallback(async (force = false) => {
    if (!id || (forwardFetching.current && !force)) return;
    const ticket = ++forwardRequest.current; forwardFetching.current = true;
    try {
      const items = await api.listForwards(id);
      if (ticket === forwardRequest.current) setForwards(items);
    } catch (e) {
      if (ticket === forwardRequest.current) setError(e instanceof Error ? e.message : "دریافت وضعیت فورواردها ناموفق بود.");
    } finally { if (ticket === forwardRequest.current) forwardFetching.current = false; }
  }, [id]);

  useEffect(() => {
    if (tab !== "forwards") return;
    void loadForwards();
    const t = setInterval(() => {
      if (document.visibilityState === "visible") void loadForwards();
    }, 3000);
    return () => clearInterval(t);
  }, [tab, loadForwards]);

  const addForward = async () => {
    if (forwardBusy) return;
 setForwardBusy(true); ++forwardRequest.current;
    setError("");
    try {
      await api.createForward(id, { name: newFwdName, protocol: "tcp", listen_port: newListen, target_port: newTarget, enabled: true });
      toast.success("فوروارد ذخیره شد؛ وضعیت گوش‌دادن در فهرست نمایش داده می‌شود.");
      await loadForwards(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "ساخت فوروارد ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally { setForwardBusy(false); }
  };

  const removeForward = async (ruleId: number) => {
 if (forwardBusy) return; setForwardBusy(true); ++forwardRequest.current;
    setError("");
    try {
      await api.deleteForward(id, ruleId);
      toast.success("فوروارد حذف شد.");
      await loadForwards(true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "حذف فوروارد ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally { setForwardBusy(false); }
  };

  // Live logs over WebSocket
  useEffect(() => {
    if (tab !== "logs" || !id) return;
    let ws: WebSocket | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let closed = false;
    let pending: string[]=[];
    let flushTimer: ReturnType<typeof setTimeout>|null=null;
    const flush=()=>{flushTimer=null;if(closed)return;const batch=pending;pending=[];setLogs(prev=>[...prev,...batch].slice(-800));};
    clearLogQueue.current=()=>{pending=[];setLogs([]);};
    const connect = () => {
      const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
      const token = localStorage.getItem("token") || "";
      if (!token) { window.location.replace(getBasePath() + "/login/"); return; }
 const url = `${proto}//${window.location.host}${getBasePath()}/api/instances/${id}/logs`;
      ws = new WebSocket(url, ["spoof-logs", token]);
      ws.onopen = () => {pending=[];if(flushTimer)clearTimeout(flushTimer);flushTimer=null;setLogs([]);setWsState("live");};
      ws.onmessage = (ev) => {pending.push(String(ev.data));if(pending.length>800)pending=pending.slice(-800);if(!flushTimer)flushTimer=setTimeout(flush,100);};
      ws.onclose = () => {
        if (closed) return;
        const curToken = localStorage.getItem("token") || "";
        if (!curToken) {
          window.location.replace(getBasePath() + "/login/");
          return;
        }
        try {
          const parts = curToken.split(".");
          if (parts.length === 3) {
            const payload = JSON.parse(atob(parts[1]));
            if (payload.exp && Date.now() >= payload.exp * 1000) {
              localStorage.removeItem("token");
              window.location.replace(getBasePath() + "/login/");
              return;
            }
          }
        } catch {
          // ignore parsing error
        }
        setWsState("retry");
        timer = setTimeout(connect, 2500);
      };
    };
    connect();
    return () => {
      closed = true;
      if (timer) clearTimeout(timer);
      if(flushTimer)clearTimeout(flushTimer);
      ws?.close();
      setWsState("off");
    };
  }, [tab, id]);

  useEffect(() => {
    const viewer = logEnd.current?.parentElement;
 if (viewer && followLog.current) viewer.scrollTop = viewer.scrollHeight;
  }, [logs]);

  const poolApplied = useCallback((version: string, patch: Record<string, string | number | boolean> = {}) => {
    setConfig(current => current ? { ...current, ...patch, updated_at: version } : current);
  }, []);

  const set = (key: string, value: string | number | boolean) =>
    setConfig((c) => (c ? { ...c, [key]: value } : c));

  const save = async () => {
    if (!config) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const compact = Object.fromEntries(Object.entries(config).filter(([key]) => !["spoof_ip_list", "peer_spoof_ip_list", "peer_pool_revision"].includes(key)));
      const saved = await api.updateInstance(id, compact);
 setConfig(saved);

      toast.success("تنظیمات اتصال با موفقیت ذخیره شد.");
      setNotice("تنظیمات ذخیره شد. برای اعمال، تونل را دوباره راه‌اندازی کن.");
    } catch (e) {
      const msg = e instanceof Error ? e.message : "ذخیره ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  };

  const control = async (kind: "start" | "stop" | "restart") => {
 if (controlBusy || saving) return; setControlBusy(true); ++requestId.current;
    setError("");
    try {
      if (kind === "start") {
        await api.instanceStart(id);
        toast.success("اتصال راه‌اندازی شد.");
      } else if (kind === "stop") {
        await api.instanceStop(id);
        toast.info("اتصال متوقف شد.");
      } else {
        await api.instanceRestart(id);
        toast.success("اتصال مجدداً راه‌اندازی شد.");
      }
      await load(false, true);
    } catch (e) {
      const msg = e instanceof Error ? e.message : "عملیات ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally { setControlBusy(false); }
  };

  const download = () => {
    if (!config) return;
    const payload = { ...config };
    delete payload.status;
    delete payload.uptime;
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${String(config.name || "tunnel").replace(/\s+/g, "-")}.json`;
    a.click();
    URL.revokeObjectURL(url);
    toast.success("فایل تنظیمات اتصال دانلود شد.");
  };

  if (!config) return <div className="page">{error ? <><div className="banner error" role="alert">{error}</div><button className="btn btn-primary" onClick={() => void load(true, true)}>تلاش دوباره</button><a href={getBasePath() + "/dashboard/tunnels/"}>بازگشت به اتصال‌ها</a></> : <p className="muted">در حال دریافت تنظیمات…</p>}</div>;

  const isTun = config.engine === "tun";
  const isLocal = config.mode === "local";
  const running = status === "running";
  const locked = running || status === "starting" || status === "stopping" || controlBusy;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">CONNECTION / SETTINGS</div>
          <div className="row">
            <h1><bdi dir="auto">{String(config.name || "اتصال")}</bdi></h1>
            <span className={`pill ${status}`}>{STATUS[status] || status}</span>
          </div>
          <p className="page-intro">
            {isTun ? "هستهٔ L3: بسته‌های کامل TCP، UDP و ICMP از طریق رابط TUN منتقل می‌شوند." : "موتور قدیمی UDP relay."}
          </p>
        </div>
        <div className="row">
          <button className="btn btn-ghost" onClick={() => window.location.assign(getBasePath() + "/dashboard/tunnels/")}>
            ← بازگشت به اتصال‌ها
          </button>
          {running ? (
            <>
              <button className="btn btn-ghost" disabled={controlBusy || saving} onClick={() => control("stop")}>
                <IconStop size={12} />
                <span>توقف</span>
              </button>
              <button className="btn btn-ghost" disabled={controlBusy || saving} onClick={() => control("restart")}>
                <IconRestart size={12} />
                <span>راه‌اندازی مجدد</span>
              </button>
            </>
          ) : (
            <button className="btn btn-primary" disabled={controlBusy || saving} onClick={() => control("start")}>
              <IconPlay size={12} />
              <span>راه‌اندازی اتصال</span>
            </button>
          )}
        </div>
      </div>

      {statusError && <div role="alert" className="banner error">{statusError}</div>}
      <RecoveryNotice state={recovery} />
      {error && <div role="alert" className="banner error">{error}</div>}
      {notice && <div role="status" className="banner ok">{notice}</div>}

      <div className="tabs" role="tablist">
        {([
          ["config", "تنظیمات اتصال"],
          ["ips", "استخر IPهای اسپوف"],
          ["forwards", "فوروارد معکوس (ورودی کلاینت)"],
          ["logs", "لاگ زندهٔ تونل"],
        ] as const).map(([k, label]) => (
          <button key={k} role="tab" aria-selected={tab === k} className={`tab ${tab === k ? "active" : ""}`} onClick={() => setTab(k)}>
            {label}
          </button>
        ))}
      </div>

      {tab === "config" && (
        <>
          {locked && <div className="banner warn">برای تغییر تنظیمات، ابتدا دکمهٔ «توقف» را بزنید.</div>}

          <div className="grid-2">
            <section className="panel">
              <h2>هویت و راه‌اندازی</h2>
              <div className="stack">
                <Field label="نام اتصال">
                  <input className="input" value={String(config.name ?? "")} disabled={locked || saving} onChange={(e) => set("name", e.target.value)} maxLength={100} />
                </Field>
                <Field label="شروع خودکار">
                  <label className="switch-row">
                    <input type="checkbox" checked={!!config.enabled} disabled={locked || saving} onChange={(e) => set("enabled", e.target.checked)} />
                    <span>اجرای خودکار هنگام بوت شدن پنل</span>
                  </label>
                </Field>
                <Field label="کلید رمزنگاری مشترک (PSK)" hint="کلید ۳۲ بایتی محرمانه داخل فایل دانلود است.">
                  <input className="input mono" dir="ltr" value="••••••••••••••••••••••••••••••••" readOnly />
                </Field>
              </div>
            </section>

            <section className="panel">
              <h2>پروتکل‌های حامل و پورت‌ها</h2>
              <div className="stack">
                <Field label="حامل ارسال (Upload Carrier)">
                  <select className="input" value={String(config.send_transport ?? "icmp")} disabled={locked || saving} onChange={(e) => set("send_transport", e.target.value)}>
                    {CARRIERS.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
                  </select>
                </Field>
                <Field label="حامل دریافت (Download Carrier)" hint="باید با حامل ارسال طرف مقابل هم‌خوان باشد.">
                  <select className="input" value={String(config.recv_transport ?? "icmp")} disabled={locked || saving} onChange={(e) => set("recv_transport", e.target.value)}>
                    {CARRIERS.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
                  </select>
                </Field>
                <div className="grid-2 tight">
                  <Field label={isLocal ? "پورت دریافت خارج" : "پورت دریافت این سرور"}>
                    <input className="input mono" dir="ltr" type="number" min={1} max={65535}
                      value={Number(isLocal ? config.remote_port : config.listen_port) || ""}
                      disabled={locked || saving}
                      onChange={(e) => set(isLocal ? "remote_port" : "listen_port", Number(e.target.value))} />
                  </Field>
                  <Field label={isLocal ? "پورت دریافت ایران" : "پورت دریافت طرف مقابل"}>
                    <input className="input mono" dir="ltr" type="number" min={1} max={65535}
                      value={Number(isLocal ? config.recv_port : config.client_port) || ""}
                      disabled={locked || saving}
                      onChange={(e) => set(isLocal ? "recv_port" : "client_port", Number(e.target.value))} />
                  </Field>
                </div>
              </div>
            </section>

            <section className="panel">
              <h2>آدرس‌های واقعی دو سرور</h2>
              <div className="stack">
                <Field label="IP واقعی سرور محلی" hint="مقصد بسته‌ها و بایند سوکت خام.">
                  <input className="input mono" dir="ltr" value={String(config.local_ip ?? "")} disabled={locked || saving} onChange={(e) => set("local_ip", e.target.value)} />
                </Field>
                <Field label="IP واقعی طرف مقابل">
                  <input className="input mono" dir="ltr"
                    value={String((isLocal ? config.remote_addr : config.client_ip) ?? "")}
                    disabled={locked || saving}
                    onChange={(e) => set(isLocal ? "remote_addr" : "client_ip", e.target.value)} />
                </Field>
              </div>
            </section>

            <section className="panel">
              <h2>آدرس‌های مبدأ جعلی (Spoof)</h2>
              <div className="stack">
                <Field label="مبدأ تکی جایگزین این سمت" hint="فقط اگر استخر ارسال خالی باشد استفاده می‌شود؛ مبدأهای فعال را در تب «استخر IPهای اسپوف» ویرایش کن.">
                  <input className="input mono" dir="ltr" value={String(config.spoof_ip ?? "")} disabled={locked || saving} onChange={(e) => set("spoof_ip", e.target.value)} />
                </Field>
                <Field label="مبدأ جعلی مورد انتظار طرف مقابل">
                  <input className="input mono" dir="ltr" value={String(config.peer_spoof_ip ?? "")} disabled={locked || saving} onChange={(e) => set("peer_spoof_ip", e.target.value)} />
                </Field>
                <p className="hint">فهرست چندمبدأییِ دریافت را در تب «استخر IPهای اسپوف» با طرف مقابل همگام کن. فهرست‌های بزرگ داخل این فرم بارگذاری نمی‌شوند.</p>
              </div>
            </section>

            {isTun && (
              <section className="panel">
                <h2>پیکربندی رابط TUN مجازی</h2>
                <div className="stack">
                  <Field label="نام رابط سیستم (Network Interface)" hint="۱ تا ۱۵ کاراکتر (مثلاً sptest0).">
                    <input className="input mono" dir="ltr" value={String(config.tun_interface ?? "")} disabled={locked || saving} onChange={(e) => set("tun_interface", e.target.value)} />
                  </Field>
                  <div className="grid-2 tight">
                    <Field label="IP داخلی این سمت">
                      <input className="input mono" dir="ltr" value={String(config.tun_address ?? "")} disabled={locked || saving} onChange={(e) => set("tun_address", e.target.value)} />
                    </Field>
                    <Field label="IP داخلی طرف مقابل">
                      <input className="input mono" dir="ltr" value={String(config.tun_peer ?? "")} disabled={locked || saving} onChange={(e) => set("tun_peer", e.target.value)} />
                    </Field>
                  </div>
                  <Field label="MTU رابط TUN" hint="محدوده ۵۷۶ تا ۱۳۰۰ بایت (پیشنهادی: ۱۲۸۰).">
                    <input className="input mono" dir="ltr" type="number" min={576} max={1300}
                      value={Number(config.tun_mtu) || ""}
                      disabled={locked || saving}
                      onChange={(e) => set("tun_mtu", Number(e.target.value))} />
                  </Field>
                </div>
              </section>
            )}
          </div>

          <div className="row end sticky-actions">
            <button className="btn btn-ghost" onClick={download}>
              <IconDownload size={14} />
              <span>دانلود فایل کانفیگ JSON</span>
            </button>
            <button className="btn btn-primary" disabled={saving || locked} onClick={save}>
              {saving ? "در حال ذخیره‌سازی…" : "ذخیرهٔ تنظیمات اتصال"}
            </button>
          </div>
        </>
      )}

      {isTun ? <div hidden={tab !== "ips"}><PoolManager id={id} local={isLocal} running={locked} version={String(config.updated_at ?? "")} active={tab === "ips"} onApplied={poolApplied} /></div> : tab === "ips" && <section className="panel"><h2>استخر هوشمند</h2><p className="hint">این بخش برای موتور TUN لایهٔ ۳ در دسترس است.</p></section>}

      {tab === "forwards" && (
        <section className="panel">
          <div className="section-head">
            <div>
              <h2>فوروارد معکوس: ورودی کلاینت روی همین سرور</h2>
              <span className="hint">فقط برای تونل TUN</span>
            </div>
            <button className="btn btn-ghost btn-sm" onClick={() => void loadForwards(true)}>به‌روزرسانی</button>
          </div>
          <p className="page-intro">
            کلاینت (گوشی/لپتاپ) به IP عمومی همین سرور روی «پورت ورودی» وصل می‌شود و پنل، اتصال را از داخل تونل به IP داخلی طرف مقابل روی «پورت مقصد» می‌رساند؛ جایی که سرویس واقعی (مثلاً 3x-ui) گوش می‌کند.
            مقصد همیشه IP داخلی طرف مقابل است و هرگز IP عمومی دیگری نیست. این فوروارد فقط TCP است؛ UDP در نسخهٔ ۱ پشتیبانی نمی‌شود.
            توجه: پورت ورودی روی اینترنت عمومی باز می‌شود و تنها محافظ آن، احراز هویت سرویس مقصد است (مثلاً UUID در 3x-ui)؛ پس پورت ورودی را با پورت‌های تونل (5001 و 8090) اشتباه نگیر.
          </p>
          {!isTun && <p className="hint" style={{ color: "var(--accent-amber)" }}>فوروارد فقط برای تونل‌های TUN فعال است؛ موتور قدیمی مسیر listen/forward خودش را دارد.</p>}
          {isTun && (
            <>
              <div className="grid-2">
                <Field label="نام (اختیاری)">
                  <input className="input" value={newFwdName} onChange={(e) => setNewFwdName(e.target.value)} maxLength={100} placeholder="مثلاً vless-خارج" />
                </Field>
                <Field label="پورت ورودی روی همین سرور (کلاینت به این پورت می‌زند)">
                  <input className="input mono" dir="ltr" type="number" min={1} max={65535} value={newListen} onChange={(e) => setNewListen(Number(e.target.value))} />
                </Field>
                <Field label="پورت مقصد روی سرور مقابل (پورت Inbound در 3x-ui)">
                  <input className="input mono" dir="ltr" type="number" min={1} max={65535} value={newTarget} onChange={(e) => setNewTarget(Number(e.target.value))} />
                </Field>
                <div className="field"><span>&nbsp;</span><button className="btn btn-primary" disabled={forwardBusy} onClick={() => void addForward()}>+ ساخت فوروارد</button></div>
              </div>
              <div className="stack" style={{ marginTop: 16 }}>
                {forwards.length === 0 && <p className="muted">هنوز فورواردی ثبت نشده است.</p>}
                {forwards.map((f) => (
                  <div key={f.rule.id} className="panel inner">
                    <div className="section-head">
                      <h3 dir="ltr">{f.rule.name} — {f.rule.listen_port} → {f.target}</h3>
                      <span className={`pill ${f.listening ? "ok" : "muted"}`}>{f.listening ? "در حال گوش دادن" : "متوقف"}</span>
                    </div>
                    <p className="hint" dir="ltr">
                      client → this server:{f.rule.listen_port} → tunnel → {f.target} · connections: {f.stats?.connections ?? 0} · ↑ {f.stats?.bytes_up ?? 0} B · ↓ {f.stats?.bytes_down ?? 0} B
                    </p>
                    {f.stats?.last_error && <p className="hint" style={{ color: "var(--accent-amber)" }}>{f.stats.last_error}</p>}
                    <button className="btn btn-ghost btn-sm danger-text" disabled={forwardBusy} onClick={() => void removeForward(f.rule.id)}>حذف فوروارد</button>
                  </div>
                ))}
              </div>
              <p className="hint">فوروارد با شروع تونل بالا می‌آید و با توقف تونل (یا از کار افتادن آن) متوقف می‌شود. اگر تونل در حال اجراست، فوروارد جدید بلافاصله گوش می‌کند و نیازی به ری‌استارت تونل نیست.</p>
            </>
          )}
        </section>
      )}

      {tab === "logs" && (
        <section className="panel">
          <div className="section-head">
            <div>
              <h2>لاگ زندهٔ پروسهٔ تانل</h2>
              <span className="hint">رویدادهای هسته و وضعیت استخر؛ نمایش آخرین ۸۰۰ خط. با اتصال مجدد، تاریخچهٔ تازه جایگزین می‌شود.</span>
            </div>
            <div className="row">
              <span className={`pill ${wsState === "live" ? "ok" : wsState === "retry" ? "warn" : "muted"}`}>
                {wsState === "live" ? "● سوکت فعال" : wsState === "retry" ? "اتصال مجدد…" : "غیرفعال"}
              </span>
              <button className="btn btn-ghost btn-sm" onClick={() => clearLogQueue.current()}>پاک کردن لاگ</button>
            </div>
          </div>
          <div className="log-viewer" dir="ltr" onScroll={e=>{const v=e.currentTarget;followLog.current=v.scrollHeight-v.scrollTop-v.clientHeight<160;}}>
            {logs.length === 0 && <div className="muted">در حال حاضر لاگی ثبت نشده است. با راه‌اندازی اتصال، لاگ‌های خروجی اینجا نمایش داده می‌شوند.</div>}
            {logs.map((line, i) => (
              <div dir="auto" key={`${i}-${line.slice(0, 14)}`} className={/error|fatal|panic/i.test(line) ? "log-error" : /warn/i.test(line) ? "log-warn" : ""}>
                {formatTunnelLog(line)}
              </div>
            ))}
            <div ref={logEnd} />
          </div>
        </section>
      )}
    </div>
  );
}

export default function EditPage() {
  return (
    <Suspense fallback={<div className="page"><p className="muted">در حال بارگذاری…</p></div>}>
      <EditContent />
    </Suspense>
  );
}
