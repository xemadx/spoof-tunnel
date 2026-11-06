"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  type BenchPreview,
  type BenchResult,
  type BenchState,
  type BenchPreset,
  type TunnelInstance,
} from "@/lib/api";
import { Dialog } from "@/lib/dialog";
import { copyText } from "@/lib/clipboard";
import { explainBenchmarkError as explain } from "@/lib/explain";
import Link from "@/lib/link";

const CARRIERS = ["icmp", "udp", "tcp", "ipip", "experimental"];
const LABEL: Record<string, string> = {
  icmp: "ICMP",
  udp: "UDP",
  tcp: "TCP خام",
  ipip: "IPIP · ۴",
  experimental: "IP · ۲۵۳",
};
const PHASE: Record<string, string> = {
  handshake: "بررسی پاسخ دوطرفه",
  finishing: "هماهنگ‌سازی نتیجهٔ دو سرور",
  latency: "سنجش تأخیر",
  upload: "سنجش ارسال از این سرور",
  download: "سنجش دریافت در این سرور",
  prepare: "بررسی و راه‌اندازی دو سمت",
  testing: "دریافت آزمون‌ها",
};
const number = (n: number, digits = 1) =>
  n.toLocaleString("fa-IR", { maximumFractionDigits: digits });
function failure(s?: string) {
  if (!s) return "این ترکیب هنوز آزموده نشده است.";
  if (s.includes("no authenticated response"))
    return "در این ترکیب، پاسخ احرازشدهٔ دوطرفه دریافت نشد.";
  if (s.includes("fewer than 8"))
    return "پاسخ کافی برای اندازه‌گیری قابل اتکا دریافت نشد؛ اگر هیچ پاسخی نیامده، این حامل در این جهت توسط مسیر فیلتر می‌شود و خطای کد نیست.";
  if (s.includes("no delivered"))
    return "دادهٔ آزمون در یکی از جهت‌ها دریافت نشد.";
  return s;
}
function applyError(error: string) {
  const marker = "new connection did not establish an authenticated peer";
  if (!error.includes(marker)) return null;
  const detail = error.includes("(") ? error.slice(error.indexOf("(")) : "";
  return (
    <>
      اتصال جدید در زمان مقرر همتای احرازشده را پیدا نکرد و تنظیمات قبلی
      بازیابی شد.
      {detail && (
        <>
          <br />
          <small dir="ltr">{detail}</small>
        </>
      )}
    </>
  );
}
function speed(r: BenchResult) {
  return Math.min(r.upload.mbps, r.download.mbps);
}

export default function ConnectionTestPage() {
  const [role, setRole] = useState<"local" | "remote">("local");
  const [state, setState] = useState<BenchState>();
  const [instances, setInstances] = useState<TunnelInstance[]>([]);
  const [target, setTarget] = useState(0);
  const [preset, setPreset] = useState<BenchPreset>();
  const [presetLoading, setPresetLoading] = useState(false);
  const [presetError, setPresetError] = useState("");
  const presetSerial = useRef(0);
  const presetController = useRef<AbortController | undefined>(undefined);
  const [code, setCode] = useState("");
  const [receiverCode, setReceiverCode] = useState("");
  const [name, setName] = useState("اتصال سنجیده‌شده");
  const [subnet, setSubnet] = useState("10.77.0.0/30");
  const [rate, setRate] = useState(20);
  const [seconds, setSeconds] = useState(3);
  const [escalate, setEscalate] = useState(true);
  const [allow, setAllow] = useState(false);
  const [form, setForm] = useState({
    local_ip: "",
    peer_ip: "",
    source: "",
    peer_source: "",
    port_a: 39090,
    port_b: 39091,
  });
  const [error, setError] = useState("");
  const [pollError, setPollError] = useState(false);
  const [busy, setBusy] = useState("");
  const [selected, setSelected] = useState<number | null>(null);
  const [preview, setPreview] = useState<BenchPreview>();
  const [setupOpen, setSetupOpen] = useState(true);
  useEffect(() => {
    if (
      state?.role === "local" &&
      ["running", "applying", "done"].includes(state.status)
    )
      setSetupOpen(false);
  }, [state?.role, state?.status]);
  const [copied, setCopied] = useState(false);
  const [copyHint, setCopyHint] = useState("");
  const codeField = useRef<HTMLTextAreaElement>(null);
  const busyRef = useRef(false),
    mounted = useRef(false);
  const active =
    !!state && ["waiting", "running", "applying"].includes(state.status);
  const locked = active || (state?.status === "done" && !state.applied);
  // Which of the four steps the page is on: 1 setup, 2 measuring,
  // 3 recommendation, 4 applying. One glance, no paragraphs.
  const step =
    !state || state.status === "idle" || state.status === "stopped"
      ? 1
      : state.status === "waiting" || state.status === "running"
        ? 2
        : state.status === "done"
          ? 3
          : 4;
  // Worst-case ladder cost: every cell climbs every rung to the 500 Mbps
  // measurement ceiling. In practice the ladder stops as soon as the link
  // saturates, so real usage is far below this.
  const ladderRungs = (() => {
    const rungs = [rate];
    while (rungs[rungs.length - 1] < 500)
      rungs.push(Math.min(rungs[rungs.length - 1] * 2, 500));
    return rungs;
  })();
  useEffect(() => {
    mounted.current = true;
    void api
      .listInstances()
      .then(setInstances)
      .catch(() => {});
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const next = await api.benchmarkStatus(controller.signal);
        if (mounted.current) {
          setState(next);
          setPollError(false);
          if (
            ["waiting", "running", "applying", "done"].includes(next.status) &&
            !next.applied &&
            (next.role === "local" || next.role === "remote")
          )
            setRole(next.role);
        }
      } catch {
        if (mounted.current) setPollError(true);
      }
      if (mounted.current) timer = setTimeout(poll, 1500);
    };
    void poll();
    return () => {
      mounted.current = false;
      controller.abort();
      presetController.current?.abort();
      clearTimeout(timer);
    };
  }, []);
  const action = async (key: string, work: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(key);
    setError("");
    try {
      await work();
    } catch (e) {
      if (mounted.current) setError(explain(e));
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy("");
    }
  };
  const clearPreset = () => {
    presetSerial.current++;
    presetController.current?.abort();
    setPreset(undefined);
    setPresetLoading(false);
    setPresetError("");
  };
  const chooseTarget = (id: number) => {
    setTarget(id);
    clearPreset();
    setError("");
    if (!id) return;
    const ticket = presetSerial.current,
      controller = new AbortController();
    presetController.current = controller;
    setPresetLoading(true);
    void api
      .benchmarkPreset(id, controller.signal)
      .then((next) => {
        if (
          !mounted.current ||
          controller.signal.aborted ||
          ticket !== presetSerial.current
        )
          return;
        if (next.id !== id || next.role !== role)
          throw Error("سمت اتصال انتخاب‌شده با نقش این سرور برابر نیست.");
        setPreset(next);
        if (role === "remote")
          setForm({
            local_ip: next.local_ip,
            peer_ip: next.peer_ip,
            source: next.source,
            peer_source: next.peer_source,
            port_a: next.send_port,
            port_b: next.receive_port,
          });
        else {
          setName(next.name);
          setSubnet(next.subnet);
        }
      })
      .catch((e) => {
        if (
          mounted.current &&
          !controller.signal.aborted &&
          ticket === presetSerial.current
        )
          setPresetError(explain(e));
      })
      .finally(() => {
        if (mounted.current && ticket === presetSerial.current)
          setPresetLoading(false);
      });
  };
  const results = state?.results || [];
  const best = results.find((r) => r.index === state?.recommended);
  const detail = results.find(
    (r) => r.index === (selected ?? state?.recommended),
  );
  const closePreview = useCallback(() => {
    if (!busyRef.current) setPreview(undefined);
  }, []);
  const start = () =>
    void action("start", async () => {
      setSelected(null);
      setPreview(undefined);
      setState(
        await api.benchmarkStart({
          code: code.trim(),
          target_id: target,
          name,
          subnet,
          options: {
            rate_mbps: rate,
            seconds,
            escalate,
          },
        }),
      );
    });
  const makeCode = () =>
    void action("receiver", async () => {
      const r = await api.benchmarkReceiver({
        ...form,
        target_id: target,
        allow_apply: allow,
      });
      setReceiverCode(r.code);
      setState(r.state);
      setCopied(false);
    });
  const stop = () =>
    void action("stop", async () => {
      setState(await api.benchmarkStop());
      setSetupOpen(true);
      setReceiverCode("");
      setPreview(undefined);
    });
  const showPreview = (r: BenchResult) =>
    void action("preview", async () =>
      setPreview(await api.benchmarkPreview(r.index)),
    );
  const validPort = (p: number) => Number.isInteger(p) && p >= 1 && p <= 65535;
  return (
    <div className="ct-page">
      <div className="page-header">
        <div>
          <h1>آزمون اتصال</h1>
          <p className="page-subtitle">
            ۲۵ ترکیب حامل را در هر دو جهت می‌سنجد و بهترین را پیشنهاد می‌دهد.
          </p>
        </div>
      </div>
      <ol className="ct-steps" aria-label="مراحل آزمون">
        {["آماده‌سازی", "سنجش", "پیشنهاد", "اعمال"].map((label, i) => (
          <li
            key={label}
            className={
              i + 1 === step ? "current" : i + 1 < step ? "done" : ""
            }
            aria-current={i + 1 === step ? "step" : undefined}
          >
            <span className="ct-step-num">
              {i + 1 < step ? "✓" : number(i + 1, 0)}
            </span>
            {label}
          </li>
        ))}
      </ol>
      {error && (
        <div className="alert alert-error" role="alert">
          {error}
          <br />
          <Link href="/dashboard/logs">جزئیات و علت در لاگ کنسول</Link>
        </div>
      )}
      {pollError && (
        <p className="pool-note" role="status">
          دریافت وضعیت قطع شده؛ نتیجهٔ قبلی نمایش داده می‌شود. اتصال به پنل
          دوباره بررسی می‌شود.
        </p>
      )}
      <section className="panel">
        <div className="section-head">
          <div>
            <h2>آماده‌سازی</h2>
            <p className="hint">
              {role === "local"
                ? "کد ساخته‌شده در پنل طرف مقابل را وارد کن."
                : "کد را بساز و به سرور کنترل‌کننده بده."}
            </p>
          </div>
        </div>
        <button
          className="btn btn-ghost btn-sm"
          aria-expanded={setupOpen}
          aria-controls="connection-test-setup"
          onClick={() => setSetupOpen((v) => !v)}
        >
          {setupOpen ? "جمع‌کردن تنظیمات" : "نمایش تنظیمات آزمون"}
        </button>
        <div id="connection-test-setup" hidden={!setupOpen}>
          <div className="ct-role" role="group" aria-label="نقش این سرور">
            <button
              className={`btn ${role === "local" ? "btn-primary" : "btn-ghost"}`}
              aria-pressed={role === "local"}
              disabled={!!locked || !!busy}
              onClick={() => {
                clearPreset();
                setRole("local");
                setTarget(0);
              }}
            >
              کنترل آزمون · معمولاً ایران
            </button>
            <button
              className={`btn ${role === "remote" ? "btn-primary" : "btn-ghost"}`}
              aria-pressed={role === "remote"}
              disabled={!!locked || !!busy}
              onClick={() => {
                clearPreset();
                setRole("remote");
                setTarget(0);
              }}
            >
              طرف مقابل · معمولاً خارج
            </button>
          </div>
          <label className="field">
            <span>اتصال مقصد برای اعمال نتیجه</span>
            <select
              className="input"
              value={target}
              disabled={!!locked || !!busy}
              onChange={(e) => chooseTarget(Number(e.target.value))}
            >
              <option value={0}>ساخت اتصال جدید پس از تأیید</option>
              {instances
                .filter((i) => i.engine === "tun" && i.mode === role)
                .map((i) => (
                  <option key={i.id} value={i.id}>
                    {i.name} · #{i.id}
                  </option>
                ))}
            </select>
          </label>
          {presetLoading && (
            <p className="hint" role="status">
              در حال دریافت تنظیمات و مبدأهای استخر فعال…
            </p>
          )}
          {presetError && (
            <div className="pool-note" role="alert">
              <span>{presetError}</span>
              <button
                className="btn btn-ghost btn-sm"
                disabled={presetLoading || !!busy}
                onClick={() => chooseTarget(target)}
              >
                دریافت دوبارهٔ مشخصات اتصال
              </button>
            </div>
          )}
          {role === "remote" ? (
            <>
              <div className="grid-2">
                {(
                  [
                    ["local_ip", "آی‌پی واقعی همین سرور"],
                    ["peer_ip", "آی‌پی واقعی سرور کنترل‌کننده"],
                    ["source", "مبدأ جعلی برای ارسال همین سرور"],
                    ["peer_source", "مبدأ جعلی برای ارسال سرور کنترل‌کننده"],
                  ] as const
                ).map(([key, label]) => (
                  <label className="field" key={key}>
                    <span>{label}</span>
                    <input
                      className="input mono"
                      dir="ltr"
                      inputMode="decimal"
                      autoComplete="off"
                      value={form[key]}
                      readOnly={
                        !!target && (key === "local_ip" || key === "peer_ip")
                      }
                      disabled={!!locked || !!busy || presetLoading}
                      onChange={(e) =>
                        setForm((f) => ({ ...f, [key]: e.target.value.trim() }))
                      }
                      placeholder="203.0.113.1"
                    />
                  </label>
                ))}
              </div>
              <details className="pool-advanced">
                <summary>شناسه / پورت آزمون</summary>
                <div className="grid-2">
                  {(
                    [
                      ["port_a", "پورت دریافت سرور کنترل‌کننده"],
                      ["port_b", "پورت دریافت همین سرور"],
                    ] as const
                  ).map(([key, label]) => (
                    <label className="field" key={key}>
                      <span>{label}</span>
                      <input
                        className="input mono"
                        type="number"
                        min={1}
                        max={65535}
                        value={form[key]}
                        disabled={!!locked || !!busy || !!target}
                        onChange={(e) =>
                          setForm((f) => ({
                            ...f,
                            [key]: Number(e.target.value),
                          }))
                        }
                      />
                    </label>
                  ))}
                </div>
                <p className="hint">
                  پورت یکسان در همهٔ ترکیب‌ها سنجیده می‌شود؛ برای ICMP شناسهٔ
                  Echo است. IPIP و پروتکل ۲۵۳ پورت ندارند. کد آزمون ترافیک آن‌ها
                  را از اتصال‌های موجود جدا می‌کند.
                </p>
              </details>
              <label className="pool-check">
                <input
                  type="checkbox"
                  checked={allow}
                  disabled={!!locked || !!busy}
                  onChange={(e) => setAllow(e.target.checked)}
                />
                <span>
                  اجازهٔ ساخت یا تغییر همین اتصال، پس از تأیید نتیجه در سرور
                  کنترل‌کننده
                </span>
              </label>
              <div className="ct-actions">
                <button
                  className="btn btn-primary"
                  disabled={
                    !!locked ||
                    !!busy ||
                    presetLoading ||
                    (!!target && !preset) ||
                    !!presetError ||
                    !Object.values(form).every(Boolean) ||
                    !validPort(form.port_a) ||
                    !validPort(form.port_b)
                  }
                  onClick={makeCode}
                >
                  {busy === "receiver"
                    ? "در حال آماده‌سازی…"
                    : "ساخت کد موقت و انتظار"}
                </button>
              </div>
              {receiverCode && (
                <div className="ct-code">
                  <label className="field">
                    <span>
                      کد آزمون · فقط برای سرور خودت بفرست · اعتبار ۲۰ دقیقه
                    </span>
                    <textarea
                      className="input mono"
                      dir="ltr"
                      readOnly
                      value={receiverCode}
                      rows={4}
                      ref={codeField}
                      aria-label="کد آزمون اتصال"
                    />
                  </label>
                  <button
                    className="btn btn-ghost btn-sm"
                    onClick={() =>
                      void action("copy", async () => {
                        if (await copyText(receiverCode)) {
                          setCopied(true);
                          setCopyHint("");
                          setTimeout(() => setCopied(false), 2000);
                        } else {
                          codeField.current?.focus();
                          codeField.current?.select();
                          setCopyHint(
                            "کد انتخاب شد؛ با گزینهٔ کپی مرورگر یا Ctrl+C آن را کپی کن.",
                          );
                        }
                      })
                    }
                  >
                    {copied ? "کپی شد" : "کپی کد"}
                  </button>
                  {copyHint && (
                    <p className="hint" role="status">
                      {copyHint}
                    </p>
                  )}
                </div>
              )}
              {state?.status === "waiting" && !receiverCode && (
                <p className="pool-note">
                  گیرنده از قبل فعال است. برای دریافت کد تازه، این نشست را پایان
                  بده و کد جدید بساز.
                </p>
              )}
            </>
          ) : (
            <>
              <label className="field">
                <span>کد موقت طرف مقابل</span>
                <textarea
                  className="input mono"
                  dir="ltr"
                  autoComplete="off"
                  spellCheck={false}
                  rows={3}
                  value={code}
                  disabled={!!locked || !!busy}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="کد ساخته‌شده در پنل طرف مقابل"
                />
              </label>
              {!target && (
                <div className="grid-2">
                  <label className="field">
                    <span>نام اتصال جدید</span>
                    <input
                      className="input"
                      maxLength={90}
                      value={name}
                      disabled={!!locked || !!busy}
                      onChange={(e) => setName(e.target.value)}
                    />
                  </label>
                  <label className="field">
                    <span>زیرشبکهٔ اختصاصی TUN</span>
                    <input
                      className="input mono"
                      dir="ltr"
                      value={subnet}
                      disabled={!!locked || !!busy}
                      onChange={(e) => setSubnet(e.target.value)}
                    />
                    <small className="hint">
                      یک /30 آزاد؛ پیش از تأیید، تداخل در هر دو سرور بررسی
                      می‌شود.
                    </small>
                  </label>
                </div>
              )}
              <div className="grid-2">
                <label className="field">
                  <span>سقف سرعت هر جهت در آزمون</span>
                  <select
                    className="input"
                    value={rate}
                    disabled={!!locked || !!busy}
                    onChange={(e) => setRate(Number(e.target.value))}
                  >
                    {[1, 5, 10, 20, 40, 60].map((n) => (
                      <option key={n} value={n}>
                        {number(n, 0)} Mbps
                      </option>
                    ))}
                  </select>
                </label>
                <label className="field">
                  <span>مدت سنجش سرعت هر جهت</span>
                  <select
                    className="input"
                    value={seconds}
                    disabled={!!locked || !!busy}
                    onChange={(e) => setSeconds(Number(e.target.value))}
                  >
                    {[2, 3, 5, 8].map((n) => (
                      <option key={n} value={n}>
                        {number(n, 0)} ثانیه
                      </option>
                    ))}
                  </select>
                </label>
              </div>
              <label className="pool-check">
                <input
                  type="checkbox"
                  checked={escalate}
                  disabled={!!locked || !!busy}
                  onChange={(e) => setEscalate(e.target.checked)}
                />
                <span>
                  آزمون خودش نرخ را دوبرابر می‌کند تا ماکسیمم واقعی معلوم
                  شود؛ سقف انتخابی ندارد
                </span>
              </label>
              <p className="hint">
                ۲۵ ترکیب · تا حدود{" "}
                {number(
                  ((escalate
                    ? ladderRungs.reduce((a, b) => a + b, 0)
                    : rate) *
                    seconds *
                    25 *
                    2) /
                    8,
                  0,
                )}{" "}
                MB دادهٔ آزمون
              </p>
              <div className="ct-actions">
                <button
                  className="btn btn-primary"
                  disabled={
                    !!locked ||
                    !!busy ||
                    presetLoading ||
                    (!!target && !preset) ||
                    !!presetError ||
                    !code.trim() ||
                    (!target && (!name.trim() || !subnet.trim()))
                  }
                  onClick={start}
                >
                  {busy === "start" ? "در حال شروع…" : "شروع آزمون ۲۵ ترکیب"}
                </button>
              </div>
            </>
          )}
        </div>
      </section>
      {state && state.status !== "idle" && (
        <section className="panel ct-results">
          <div className="section-head">
            <div>
              <h2>
                {state.applied
                  ? "اتصال جدید تأیید و راه‌اندازی شد"
                  : state.status === "applying"
                    ? "در حال اعمال نتیجه"
                    : state.status === "done"
                      ? "نتیجهٔ اندازه‌گیری"
                      : state.status === "stopped"
                        ? "آزمون پایان داده شد"
                        : "وضعیت آزمون"}
              </h2>
              <p className="hint" role="status">
                {state.status === "waiting"
                  ? "در انتظار شروع از سرور کنترل‌کننده"
                  : PHASE[state.phase] || ""}
                {state.current >= 0 && state.status === "running"
                  ? ` · ترکیب ${number(state.current + 1, 0)} از ۲۵`
                  : ""}
              </p>
            </div>
            {state.status !== "applying" && state.status !== "stopped" && (
              <button
                className="btn btn-ghost btn-sm"
                disabled={!!busy}
                onClick={stop}
              >
                {busy === "stop"
                  ? "در حال پایان…"
                  : active
                    ? "توقف آزمون"
                    : "پایان نشست"}
              </button>
            )}
          </div>
          <progress
            max={100}
            value={state.progress}
            aria-label="پیشرفت آزمون اتصال"
          />
          {state.status === "applying" && (
            <p className="pool-note" role="status">
              تونل جدید در دو سرور راه‌اندازی می‌شود؛ برای استخرهای بزرگ،
              همگام‌سازی لیست مبدأها چند دقیقه طول می‌کشد. اگر همتا تأیید
              نشود، تنظیمات قبلی بازیابی می‌شود.
            </p>
          )}
          {state.endpoints && (
            <details className="pool-advanced">
              <summary>مبدأها و پورت‌های ثابت این آزمون</summary>
              <div className="ct-metrics">
                <div>
                  <small>سرور کنترل‌کننده</small>
                  <strong>
                    <bdi dir="ltr">{state.endpoints.controller_ip}</bdi>
                  </strong>
                  <small>
                    مبدأ جعلی{" "}
                    <bdi dir="ltr">{state.endpoints.controller_source}</bdi> ·
                    پورت دریافت {number(state.endpoints.controller_port, 0)}
                  </small>
                </div>
                <div>
                  <small>طرف مقابل</small>
                  <strong>
                    <bdi dir="ltr">{state.endpoints.peer_ip}</bdi>
                  </strong>
                  <small>
                    مبدأ جعلی <bdi dir="ltr">{state.endpoints.peer_source}</bdi>{" "}
                    · پورت دریافت {number(state.endpoints.peer_port, 0)}
                  </small>
                </div>
              </div>
              <p className="hint">
                نتیجه به همین مبدأها و شرایط این زمان مربوط است.
              </p>
            </details>
          )}
          {state.error && (
            <p className="alert alert-error" role="alert">
              {applyError(state.error) ?? state.error}
              <br />
              <Link href="/dashboard/logs">جزئیات و علت در لاگ کنسول</Link>
            </p>
          )}
          {best && (
            <div className="ct-recommendation">
              <div>
                <span className="eyebrow">پیشنهاد آزمون</span>
                <h3>
                  ارسال این سرور: {LABEL[best.send]} · ارسال طرف مقابل:{" "}
                  {LABEL[best.receive]}
                </h3>
                <p>
                  حداقل سرعت دو جهت {number(speed(best))} Mbps · تأخیر میانه{" "}
                  {number(best.rtt_ms)} ms
                </p>
                {(best.upload.capped || best.download.capped) && (
                  <p className="ct-ceiling">
                    حداقل یک جهت هنوز به سقف اندازه‌گیری (
                    {number(best.rate_mbps || rate, 0)} Mbps) رسیده؛ ظرفیت
                    واقعی بالاتر از این است. این سقف، حد فرستندهٔ آزمون است نه
                    یک تنظیم.
                  </p>
                )}
                <p className="hint">
                  {target
                    ? `روی اتصال «${instances.find((i) => i.id === target)?.name ?? `#${target}`}» اعمال می‌شود.`
                    : "اتصال هم‌سان جایگزین می‌شود، وگرنه اتصال جدید ساخته می‌شود."}
                </p>
              </div>
              {role === "local" &&
                state.status === "done" &&
                !state.applied && (
                  <button
                    className="btn btn-primary"
                    disabled={!!busy || pollError}
                    onClick={() => showPreview(best)}
                  >
                    {busy === "preview"
                      ? "بررسی دو سرور…"
                      : "بررسی و تأیید پیشنهاد"}
                  </button>
                )}
            </div>
          )}
          {state.status === "done" && !best && (
            <p className="pool-note">
              هیچ ترکیبی شرایط پیشنهاد را نداشت. مبدأها و مسیر برگشت را بررسی
              کن.
            </p>
          )}
          <div className="pool-table-wrap">
            <table className="pool-table ct-matrix">
              <caption>
                سطر: حامل ارسال این سرور · ستون: حامل ارسال طرف مقابل
              </caption>
              <thead>
                <tr>
                  <th scope="col">حامل</th>
                  {CARRIERS.map((c) => (
                    <th scope="col" key={c}>
                      {LABEL[c]}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {CARRIERS.map((send, a) => (
                  <tr key={send}>
                    <th scope="row">{LABEL[send]}</th>
                    {CARRIERS.map((receive, b) => {
                      const index = a * 5 + b,
                        r = results.find((v) => v.index === index),
                        current =
                          state.current === index && state.status === "running";
                      return (
                        <td key={receive}>
                          <button
                            className={`ct-cell ${r?.status === "passed" ? "passed" : r ? "failed" : ""} ${index === state.recommended ? "recommended" : ""}`}
                            disabled={!r}
                            aria-pressed={detail?.index === index}
                            aria-label={`ارسال ${LABEL[send]}، برگشت ${LABEL[receive]}: ${r?.status === "passed" ? `${number(speed(r))} مگابیت، مشاهده جزئیات` : r ? "ناموفق، مشاهده علت" : current ? "در حال آزمون" : "آزموده نشده"}`}
                            onClick={() => setSelected(index)}
                          >
                            {r?.status === "passed" ? (
                              <>
                                <strong dir="ltr">
                                  {r.upload.capped || r.download.capped
                                    ? "≥ "
                                    : ""}
                                  {number(speed(r))}
                                </strong>
                                <small>
                                  Mbps
                                  {index === state.recommended
                                    ? " · پیشنهاد"
                                    : ""}
                                </small>
                              </>
                            ) : (
                              <span>
                                {r ? "بی‌نتیجه" : current ? "آزمون…" : "—"}
                              </span>
                            )}
                          </button>
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {detail && (
            <div className="ct-detail">
              <h3>
                ارسال {LABEL[detail.send]} · دریافت {LABEL[detail.receive]}
              </h3>
              {detail.status !== "passed" ? (
                <p className="hint">{failure(detail.error)}</p>
              ) : (
                <>
                  <div className="ct-metrics">
                    <div>
                      <small>نرخ سنجش نهایی</small>
                      <strong dir="ltr">
                        {number(
                          detail.rate_mbps || state.options.rate_mbps,
                          0,
                        )}{" "}
                        Mbps
                      </strong>
                      <small>
                        {detail.escalated
                          ? `با دوبرابر کردن نرخ در ${number(detail.rounds || 2, 0)} مرحله سنجیده شد`
                          : "در نرخ پایه سنجیده شد"}
                      </small>
                    </div>
                    <div>
                      <small>تأخیر میانه / صدک ۹۵</small>
                      <strong dir="ltr">
                        {number(detail.rtt_ms)} / {number(detail.p95_ms)} ms
                      </strong>
                    </div>
                    <div>
                      <small>پاسخ رفت‌وبرگشت</small>
                      <strong>
                        {number(detail.ping_replies, 0)} از{" "}
                        {number(detail.ping_sent, 0)}
                      </strong>
                    </div>
                    {(
                      [
                        ["ارسال همین سرور", detail.upload],
                        ["ارسال طرف مقابل", detail.download],
                      ] as const
                    ).map(([label, d]) => (
                      <div key={label}>
                        <small>{label}</small>
                        <strong dir="ltr">
                          {d.capped ? "≥ " : ""}
                          {number(d.mbps)} Mbps
                        </strong>
                        <small>
                          افت {number(d.loss_pct)}٪ · {number(d.received, 0)}{" "}
                          دریافت از {number(d.sent, 0)} · خطای ارسال{" "}
                          {number(d.send_errors, 0)}
                        </small>
                      </div>
                    ))}
                  </div>
                  {role === "local" &&
                    state.status === "done" &&
                    !state.applied &&
                    detail.index !== state.recommended && (
                      <button
                        className="btn btn-ghost"
                        disabled={!!busy || pollError}
                        onClick={() => showPreview(detail)}
                      >
                        بررسی و تأیید همین ترکیب
                      </button>
                    )}
                </>
              )}
            </div>
          )}
          {state.applied && (
            <Link className="btn btn-primary" href="/dashboard/tunnels">
              مشاهدهٔ اتصال‌ها
            </Link>
          )}
        </section>
      )}
      {preview && (
        <Dialog title="تأیید تغییر اتصال در دو سرور" onClose={closePreview}>
          <div className="ct-preview">
            <p>
              {preview.replace
                ? "اتصال فعلی در هر دو سرور متوقف می‌شود و با حامل‌های جدید دوباره راه‌اندازی می‌شود."
                : "یک اتصال مکمل روی هر دو سرور ساخته و راه‌اندازی می‌شود."}
            </p>
            <dl>
              <div>
                <dt>نام</dt>
                <dd>
                  <bdi>{preview.name}</bdi>
                </dd>
              </div>
              <div>
                <dt>ارسال همین سرور</dt>
                <dd>{LABEL[preview.send]}</dd>
              </div>
              <div>
                <dt>ارسال طرف مقابل</dt>
                <dd>{LABEL[preview.receive]}</dd>
              </div>
              <div>
                <dt>شبکهٔ TUN</dt>
                <dd>
                  <bdi dir="ltr">{preview.subnet}</bdi>
                </dd>
              </div>
              <div>
                <dt>اتصال همین سرور / طرف مقابل</dt>
                <dd>
                  {preview.target_id ? `#${preview.target_id}` : "جدید"} /{" "}
                  {preview.remote_target_id
                    ? `#${preview.remote_target_id}`
                    : "جدید"}
                </dd>
              </div>
            </dl>
            <p className="hint">
              در صورت شکست، تنظیمات قبلی بازیابی می‌شود.
            </p>
            <div className="ct-actions">
              <button
                className="btn btn-primary"
                disabled={!!busy}
                onClick={() =>
                  void action("apply", async () => {
                    setState(await api.benchmarkApply(preview.token));
                    setPreview(undefined);
                  })
                }
              >
                {busy === "apply"
                  ? "در حال اعمال…"
                  : "تأیید و راه‌اندازی دو سرور"}
              </button>
              <button
                className="btn btn-ghost"
                disabled={!!busy}
                onClick={closePreview}
              >
                انصراف
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </div>
  );
}
