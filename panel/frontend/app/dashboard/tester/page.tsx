"use client";
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useToast } from "@/lib/toast";
import {
  IconTester,
  IconPlay,
  IconStop,
  IconUpload,
  IconCopy,
  IconCheck,
  IconDownload,
} from "@/lib/icons";

type TesterResult = {
  ip: string;
  received: number;
  sent: number;
  loss_pct: number;
  passed: boolean;
};

type TesterState = {
  status: string;
  mode: string;
  error?: string;
  progress: number;
  total_ips: number;
  processed_ips?: number;
  sent_packets?: number;
  failed_packets?: number;
  passed_count: number;
  recv_count: number;
  results: TesterResult[];
 preview_limit?: number;
};

export default function TesterPage() {
  const { toast } = useToast();
  const [tab, setTab] = useState<"receiver" | "sender">("receiver");
  const [state, setState] = useState<TesterState>({
    status: "idle",
    mode: "",
    progress: 0,
    total_ips: 0,
    passed_count: 0,
    recv_count: 0,
    results: [],
  });
  const [inputType, setInputType] = useState<"text" | "file_path">("text");
  const [ipList, setIpList] = useState("");
  const [serverFilePath, setServerFilePath] = useState("");
  const [copied, setCopied] = useState(false);
  const [protocol, setProtocol] = useState("icmp");
  const [dstIP, setDstIP] = useState("");
  const [dstPort, setDstPort] = useState(80);
  const [timeout, setTimeout_] = useState(30);
  const [packetCount, setPacketCount] = useState(10);
  const [maxLoss, setMaxLoss] = useState(20);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestId = useRef(0);
  const actionInFlight = useRef(false);
  const pollRef = useRef<NodeJS.Timeout | null>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let cancelled = false;
    let fetching = false;
    const refresh = async () => {
      if (fetching || actionInFlight.current || document.visibilityState !== "visible") return;
      fetching = true;
      const ticket = ++requestId.current;
      try {
        const next = await api.testerStatus();
        if (!cancelled && ticket === requestId.current) { setState({ ...next, results: next.results || [] }); setError(""); }
      } catch (e) {
        if (!cancelled && ticket === requestId.current) setError(e instanceof Error ? e.message : "دریافت وضعیت آزمون ناموفق بود.");
      } finally { fetching = false; }
    };
    void refresh();
    pollRef.current = setInterval(refresh, 2000);
    return () => { cancelled = true; if (pollRef.current) clearInterval(pollRef.current); };
  }, []);

  const handleStart = async () => {
 if (actionInFlight.current) return;
    if (inputType === "text" && !ipList.trim()) {
      setError("فهرست IPهای مبدأ را وارد کن.");
      return;
    }
    if (inputType === "file_path" && !serverFilePath.trim()) {
      setError("مسیر فایل روی سرور را وارد کن.");
      return;
    }
    if (tab === "sender" && !dstIP.trim()) {
      setError("آدرس IP مقصد (سرور گیرنده) را وارد کن.");
      return;
    }
    setError("");
    actionInFlight.current = true; ++requestId.current;
    setLoading(true);
    try {
      await api.testerStart({
        mode: tab,
        protocol,
        ip_list: inputType === "text" ? ipList : "",
        file_path: inputType === "file_path" ? serverFilePath : "",
        dst_ip: dstIP,
        dst_port: dstPort,
        timeout: timeout,
        packet_count: packetCount,
        max_packet_loss: maxLoss,
      });
      const next = await api.testerStatus();
      setState({ ...next, results: next.results || [] });
      toast.info(`آزمون ${tab === "receiver" ? "گیرنده" : "فرستنده"} آغاز شد.`);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : "خطا در شروع آزمون";
      setError(msg);
      toast.error(msg);
    } finally {
      actionInFlight.current = false;
      setLoading(false);
    }
  };

  const handleStop = async () => {
 if (actionInFlight.current) return;
 actionInFlight.current = true; ++requestId.current; setLoading(true);
    try {
      await api.testerStop();
const next = await api.testerStatus();
 setState({ ...next, results: next.results || [] });
      toast.info("آزمون متوقف گردید.");
    } catch (e) { setError(e instanceof Error ? e.message : "توقف آزمون ناموفق بود."); }
 finally { actionInFlight.current = false; setLoading(false); }
  };

  const handleFileUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    try {
      const result = await api.testerUpload(file);
      setIpList(result.content);
      toast.success("فایل با موفقیت بارگذاری شد.");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "بارگذاری فایل ناموفق بود";
      setError(msg);
      toast.error(msg);
    }
    e.target.value = "";
  };

  const handleCopyPassedIPs = async () => {
    try {
      const all = await api.testerResults();
      await navigator.clipboard.writeText((all.results || []).filter(result => result.passed).map(result => result.ip).join("\n"));
      setCopied(true); toast.success("همهٔ آدرس‌های تأییدشده کپی شدند."); setTimeout(() => setCopied(false), 2000);
    } catch (e) { setError(e instanceof Error ? e.message : "کپی ممکن نشد؛ از دانلود استفاده کنید."); }
  };

  const handleDownloadPassedIPs = async () => {
    try {
      const blob = await api.testerDownload(); const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a"); anchor.href = url; anchor.download = "spoof-ips.txt"; anchor.click(); URL.revokeObjectURL(url);
      toast.success("خروجی کامل آدرس‌های تأییدشده دانلود شد.");
    } catch (e) { setError(e instanceof Error ? e.message : "دانلود ناموفق بود."); }
  };

  const results = state.results || [];
  const passedCount = state.passed_count || results.filter((r) => r.passed).length;
  const totalIPs = state.total_ips || results.length;
  const receivedCount = state.recv_count || results.length;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <div className="eyebrow">DIAGNOSTICS / 05</div>
          <h1>آزمون اسپوف</h1>
          <p className="page-intro">
            بررسی اینکه کدام IPهای مبدأ جعلی از فیلتر دیتاسنتر عبور کرده و به مقصد می‌رسند.
            ابتدا <strong>گیرنده (Receiver)</strong> را روی سرور مقصد روشن کن، سپس <strong>فرستنده (Sender)</strong> را روی سرور مبدأ اجرا کن.
          </p>
        </div>
      </div>

      <div className="tabs" role="tablist">
        <button
          role="tab"
          aria-selected={tab === "receiver"}
          className={`tab ${tab === "receiver" ? "active" : ""}`}
          onClick={() => { setTab("receiver"); setError(""); }}
        >
          گیرندهٔ آزمون (Receiver)
        </button>
        <button
          role="tab"
          aria-selected={tab === "sender"}
          className={`tab ${tab === "sender" ? "active" : ""}`}
          onClick={() => { setTab("sender"); setError(""); }}
        >
          فرستندهٔ آزمون (Sender)
        </button>
      </div>

      <div className="grid-2">
        <div className="panel">
          <h2>{tab === "receiver" ? "تنظیمات گیرنده" : "تنظیمات فرستنده"}</h2>

          <div className="stack">
            <label className="field">
              <span>پروتکل آزمون</span>
              <select className="input" value={protocol} onChange={(e) => setProtocol(e.target.value)}>
                <option value="icmp">ICMP Echo</option>
                <option value="tcp">TCP SYN</option>
              </select>
            </label>

            {tab === "sender" && (
              <>
                <label className="field">
                  <span>IP مقصد (سرور گیرنده)</span>
                  <input
                    className="input mono"
                    dir="ltr"
                    value={dstIP}
                    onChange={(e) => setDstIP(e.target.value.trim())}
                    placeholder="مثال: 198.51.100.20"
                    required
                  />
                </label>
              </>
            )}
            <p className="hint">آزمون IP به IP و به‌صورت ترتیبی اجرا می‌شود تا شمارش هر IP دقیق بماند؛ برای گستره‌های بزرگ، زمان اجرا متناسب با تعداد IPها افزایش می‌یابد.</p>

            {protocol === "tcp" && (
              <label className="field">
                <span>{tab === "sender" ? "پورت مقصد" : "فیلتر پورت (۰ = همه)"}</span>
                <input
                  className="input mono"
                  dir="ltr"
                  type="number"
                  value={dstPort}
                  onChange={(e) => setDstPort(+e.target.value)}
                  min={0}
                  max={65535}
                />
              </label>
            )}

            {tab === "receiver" && (
              <label className="field">
                <span>مهلت زمان انتظار (ثانیه)</span>
                <input
                  className="input mono"
                  dir="ltr"
                  type="number"
                  value={timeout}
                  onChange={(e) => setTimeout_(+e.target.value)}
                  min={5}
                  max={3600}
                />
              </label>
            )}

            <label className="field">
              <span>تعداد بسته به ازای هر IP</span>
              <input
                className="input mono"
                dir="ltr"
                type="number"
                value={packetCount}
                onChange={(e) => setPacketCount(+e.target.value)}
                min={1}
                max={1000}
              />
            </label>

            {tab === "receiver" && (
              <label className="field">
                <span>سقف افت مجاز بسته: <strong className="mono">{maxLoss}%</strong></span>
                <input
                  type="range"
                  min={0}
                  max={100}
                  value={maxLoss}
                  onChange={(e) => setMaxLoss(+e.target.value)}
                  style={{ accentColor: "var(--accent)" }}
                />
              </label>
            )}

            <div className="field">
              <span>ورودی فهرست IP</span>
              <div className="row">
                <label className="switch-row">
                  <input
                    type="radio"
                    checked={inputType === "text"}
                    onChange={() => setInputType("text")}
                  />
                  <span>ورود دستی / بارگذاری فایل</span>
                </label>
                <label className="switch-row">
                  <input
                    type="radio"
                    checked={inputType === "file_path"}
                    onChange={() => setInputType("file_path")}
                  />
                  <span>مسیر فایل روی سرور</span>
                </label>
              </div>
            </div>

            {inputType === "text" ? (
              <div className="stack">
                <div className="section-head">
                  <span className="hint">هر خط یک IP، CIDR یا بازه (مانند 1.1.1.1 یا 10.0.0.0/24)</span>
                  <button
                    type="button"
                    className="btn btn-ghost btn-sm"
                    onClick={() => fileInputRef.current?.click()}
                  >
                    <IconUpload size={13} />
                    <span>بارگذاری فایل متنی</span>
                  </button>
                  <input
                    type="file"
                    ref={fileInputRef}
                    onChange={handleFileUpload}
                    style={{ display: "none" }}
                    accept=".txt,.csv,.list"
                  />
                </div>
                <textarea
                  className="input mono"
                  dir="ltr"
                  value={ipList}
                  onChange={(e) => setIpList(e.target.value)}
                  placeholder={"1.1.1.1\n10.0.0.0/24\n192.168.1.1-192.168.1.50"}
                  rows={8}
                />
                <span className="hint">
                  {ipList.trim()
                    ? `${ipList.trim().split("\n").filter((l) => l.trim()).length} خط وارد شده`
                    : "هیچ آدرسی وارد نشده است"}
                </span>
              </div>
            ) : (
              <label className="field">
                <span>مسیر فایل روی سرور (مانند /opt/ips.txt)</span>
                <input
                  className="input mono"
                  dir="ltr"
                  value={serverFilePath}
                  onChange={(e) => setServerFilePath(e.target.value)}
                  placeholder="/etc/spoof-panel/sources.txt"
                />
              </label>
            )}

            {error && <div role="alert" className="banner error">{error}</div>}

            <div className="row">
              {state.status !== "running" ? (
                <button
                  className="btn btn-primary btn-block"
                  onClick={handleStart}
                  disabled={loading}
                >
                  <IconPlay size={13} />
                  <span>{loading ? "در حال آماده‌سازی…" : `شروع آزمون ${tab === "receiver" ? "گیرنده" : "فرستنده"}`}</span>
                </button>
              ) : (
                <button className="btn btn-danger btn-block" onClick={handleStop}>
                  <IconStop size={13} />
                  <span>توقف آزمون</span>
                </button>
              )}
            </div>
          </div>
        </div>

        <div className="panel">
          <div className="section-head">
            <h2>وضعیت و نتایج</h2>
            <div className="row">
              <span className={`status-dot ${state.status === "running" ? "running" : state.status === "done" ? "ok" : "stopped"}`} />
              <span className="mono">{state.status.toUpperCase()} {state.mode ? `(${state.mode})` : ""}</span>
            </div>
          </div>

          {state.status === "running" && (
            <div className="stack">
              <div className="section-head">
                <span className="hint">پیشرفت</span>
                <span className="mono">{state.progress}%</span>
              </div>
              <div style={{ height: 6, background: "var(--bg-secondary)", borderRadius: 3, overflow: "hidden" }}>
                <div
                  style={{
                    width: `${state.progress}%`,
                    height: "100%",
                    background: "linear-gradient(90deg, var(--accent), var(--accent-2))",
                    transition: "width 0.4s",
                  }}
                />
              </div>
            </div>
          )}

          {state.error && <div role="alert" className="banner error">{state.error}</div>}

          {state.mode === "sender" && (
            <div className="stack" aria-label="شمارنده‌های واقعی فرستنده">
              <p>IPهای پردازش‌شده: <strong className="mono">{(state.processed_ips ?? 0).toLocaleString("fa-IR")} / {state.total_ips.toLocaleString("fa-IR")}</strong></p>
              <p>بسته‌های پذیرفته‌شده برای ارسال: <strong className="mono">{(state.sent_packets ?? 0).toLocaleString("fa-IR")}</strong></p>
              <p>خطاهای ارسال: <strong className="mono">{(state.failed_packets ?? 0).toLocaleString("fa-IR")}</strong></p>
              <p className="hint">ارسال در فرستنده، دریافت در مقصد را ثابت نمی‌کند؛ نتیجهٔ عبور هر IP را در گیرنده بررسی کن.</p>
            </div>
          )}

          {(results.length > 0 || state.recv_count > 0) && (
            <div className="stack">
              {state.status === "running" && state.mode === "receiver" && (
                <div className="banner ok">
                  <span>در حال دریافت بسته‌ها… {state.recv_count} آدرس تا این لحظه شناسایی شد.</span>
                </div>
              )}

              <div className="stats" style={{ gridTemplateColumns: "repeat(3, 1fr)" }}>
                <div className="stat">
                  <span className="stat-label">کل آدرس‌ها</span>
                  <strong className="stat-value">{totalIPs.toLocaleString("fa-IR")}</strong>
                </div>
                <div className="stat">
                  <span className="stat-label">پذیرفته‌شده</span>
                  <strong className="stat-value" style={{ color: "var(--success)" }}>
                    {passedCount.toLocaleString("fa-IR")}
                  </strong>
                </div>
                <div className="stat">
                  <span className="stat-label">دریافت‌شده</span>
                  <strong className="stat-value" style={{ color: "var(--accent-2)" }}>
                    {receivedCount.toLocaleString("fa-IR")}
                  </strong>
                </div>
              </div>

              <div className="row">
                <button className="btn btn-ghost btn-sm" onClick={handleCopyPassedIPs}>
                  {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
                  <span>{copied ? "کپی شد" : "کپی IPهای تأییدشده"}</span>
                </button>
                <button className="btn btn-ghost btn-sm" onClick={handleDownloadPassedIPs}>
                  <IconDownload size={13} />
                  <span>دانلود spoof-ips.txt</span>
                </button>
              </div>

              {receivedCount > results.length && <p className="hint">نمایش {results.length} نتیجه از {receivedCount} آدرس دریافت‌شده؛ کپی و دانلود شامل خروجی کامل‌اند.</p>}
              <div className="table-wrap" style={{ maxHeight: 380, overflowY: "auto" }}>
                <table className="table">
                  <thead>
                    <tr>
                      <th>آدرس IP</th>
                      <th>دریافتی / ارسالی</th>
                      <th>افت بسته</th>
                      <th>وضعیت</th>
                    </tr>
                  </thead>
                  <tbody>
                    {results.map((r, i) => (
                      <tr key={i}>
                        <td className="mono" dir="ltr">{r.ip}</td>
                        <td className="mono">{r.received} / {r.sent}</td>
                        <td className="mono" style={{ color: r.loss_pct <= 10 ? "var(--success)" : r.loss_pct <= 30 ? "var(--warning)" : "var(--danger)" }}>
                          {r.loss_pct.toFixed(1)}%
                        </td>
                        <td>
                          <span className={`pill ${r.passed ? "ok" : "err"}`}>
                            {r.passed ? "موفق" : "رد"}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {results.length === 0 && state.status === "idle" && (
            <div className="empty">
              <div className="empty-icon"><IconTester size={44} /></div>
              <h3>آزمونی در جریان نیست</h3>
              <p>
                برای شروع، ابتدا گیرنده را در سرور خارج روشن کن و سپس فرستنده را در ایران اجرا کن.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
