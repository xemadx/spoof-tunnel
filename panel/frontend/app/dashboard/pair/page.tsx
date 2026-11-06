"use client";
import { useEffect, useMemo, useState } from "react";
import Link from "@/lib/link";
import { api } from "@/lib/api";
import { useBasePath } from "@/lib/basepath";
import { useToast } from "@/lib/toast";
import { copyText } from "@/lib/clipboard";
import { IconDownload, IconCopy, IconCheck } from "@/lib/icons";

type End = Record<string, string | number | boolean>;

const CARRIERS = [
  { value: "icmp", label: "ICMP Echo", desc: "نیازمند آزمون مسیر" },
  { value: "tcp", label: "TCP خام", desc: "بدون هندشیک، همراه با PSH/ACK" },
  { value: "udp", label: "UDP Datagram", desc: "مناسب سرویس‌های بی‌درنگ" },
  { value: "ipip", label: "پروتکل ۴ (IPIP)", desc: "فریم رمزشده داخل هدر IP" },
  { value: "experimental", label: "پروتکل ۲۵۳", desc: "آزمایشی؛ عبور وابسته به شبکه" },
];

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

function suggestSubnet(used: string[]): string {
  const occupied = used.flatMap(value => {
    const [address, prefix] = value.split("/"); const bits = Number(prefix);
    if (!IPV4.test(address) || !Number.isInteger(bits) || bits < 0 || bits > 32) return [];
    const number = address.split(".").reduce((sum, part) => sum * 256 + Number(part), 0);
    const size = 2 ** (32 - bits), first = Math.floor(number / size) * size;
    return [{ first, last: first + size - 1 }];
  });
  for (const second of [77, 88, 99]) {
    for (let third = 0; third < 256; third++) {
      for (let fourth = 0; fourth < 256; fourth += 4) {
        const first = ((10 * 256 + second) * 256 + third) * 256 + fourth;
        if (!occupied.some(range => first <= range.last && first + 3 >= range.first)) return `10.${second}.${third}.${fourth}/30`;
      }
    }
  }
  return "";
}

function download(name: string, config: End) {
  const blob = new Blob([JSON.stringify(config, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

export default function PairPage() {
  const base = useBasePath();
  const { toast } = useToast();
  const [form, setForm] = useState({
    engine: "tun",
    auto_start: true,
    tun_subnet: "10.77.0.0/30",
    name: "تونل اصلی",
    local_ip: "",
    remote_ip: "",
    local_spoof: "",
    remote_spoof: "",
    listen: "127.0.0.1:5000",
    forward: "127.0.0.1:51820",
    upload: "icmp",
    download: "icmp",
    local_port: 5001,
    remote_port: 8090,
    save_role: "local" as "local" | "remote" | "",
  });
  const [result, setResult] = useState<{ local: End; remote: End; saved: End | null } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState<"local" | "remote" | "">("");

  useEffect(() => {
    api.listInstances()
      .then((list: Array<{ tun_address?: string }>) => {
        const used = (list || []).map((i) => i.tun_address || "").filter(Boolean);
        setForm((f) => ({ ...f, tun_subnet: suggestSubnet(used) }));
      })
      .catch(() => {});
  }, []);

  const set = (key: keyof typeof form, value: string | number | boolean) => {
    setResult(null);
    setError("");
    setForm((f) => ({ ...f, [key]: value }));
  };

  const problems = useMemo(() => {
    const list: string[] = [];
    if (!IPV4.test(form.local_ip)) list.push("IP واقعی سرور ایران معتبر نیست.");
    if (!IPV4.test(form.remote_ip)) list.push("IP واقعی سرور خارج معتبر نیست.");
    if (form.local_ip === form.remote_ip && form.local_ip !== "") list.push("آدرس‌های واقعی دو سرور باید متمایز باشند.");
    if (!IPV4.test(form.local_spoof)) list.push("مبدأ جعلی ایران معتبر نیست.");
    if (!IPV4.test(form.remote_spoof)) list.push("مبدأ جعلی خارج معتبر نیست.");
    if (form.local_spoof === form.remote_spoof && form.local_spoof !== "") list.push("مبدأهای جعلی دو طرف باید متفاوت باشند.");
    if (!/^\d{1,3}(\.\d{1,3}){3}\/30$/.test(form.tun_subnet)) list.push("شبکهٔ داخلی باید IPv4 با prefix یعنی /30 باشد.");
    if (!form.name.trim()) list.push("نام اتصال الزامی است.");
    return list;
  }, [form]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (problems.length) {
      setError(problems[0]);
      toast.error(problems[0]);
      return;
    }
    setBusy(true);
    setError("");
    try {
      const res = await api.pair(form);
      setResult(res);
      toast.success("تنظیمات هماهنگ دوطرفه با موفقیت تولید شد.");
    } catch (err) {
      const msg = err instanceof Error ? err.message : "ساخت تنظیمات ناموفق بود";
      setError(msg);
      toast.error(msg);
    } finally {
      setBusy(false);
    }
  };

  const copy = async (role: "local" | "remote") => {
    if (!result) return;
    if (await copyText(JSON.stringify(result[role], null, 2))) {
      setCopied(role);
      toast.success(`تنظیمات سمت ${role === "local" ? "ایران" : "خارج"} در کلیپ‌بورد کپی شد.`);
      setTimeout(() => setCopied(""), 1800);
    } else {
      setError("کپی در مرورگر ممکن نشد؛ از دکمهٔ دانلود استفاده کن.");
    }
  };

  const preview = (c: End) => JSON.stringify({ ...c, psk: "•••••••• (کلید اختصاصی ۳۲ بایتی در فایل دانلودی)" }, null, 2);

  return (
    <div className="page">
      {/* Header */}
      <div className="page-head">
        <div>
          <div className="eyebrow">CONNECTION BUILDER / 02</div>
          <h1>دو سمت، یک اتصال.</h1>
          <p className="page-intro">
            تنظیمات مکمل ایران و خارج را یک‌جا تولید کن. کلید رمزنگاری مشترک، جهت پروتکل‌ها و آدرس‌های ساب‌نت
            به صورت آینه‌ای و تضمین‌شده هماهنگ می‌شوند.
          </p>
        </div>
      </div>

      <div className="builder">
        {/* Form Panel */}
        <form className="panel" onSubmit={submit}>
          <div className="section-head">
            <h2>مشخصات اتصال دوطرفه</h2>
            <span className="pill ok">موتور پیشرفتهٔ L3 TUN</span>
          </div>

          {/* Endpoints Grid */}
          <div className="grid-2">
            <label className="field">
              <span>IP واقعی سرور ایران</span>
              <input className="input mono" dir="ltr" inputMode="decimal" placeholder="203.0.113.10"
                value={form.local_ip} onChange={(e) => set("local_ip", e.target.value.trim())} required />
              <small className="hint">مقصد بسته‌های ارسالی از خارج.</small>
            </label>
            <label className="field">
              <span>IP واقعی سرور خارج</span>
              <input className="input mono" dir="ltr" inputMode="decimal" placeholder="198.51.100.20"
                value={form.remote_ip} onChange={(e) => set("remote_ip", e.target.value.trim())} required />
              <small className="hint">مقصد بسته‌های ارسالی از ایران.</small>
            </label>
            <label className="field">
              <span>مبدأ جعلی سمت ایران</span>
              <input className="input mono" dir="ltr" inputMode="decimal" placeholder="192.0.2.11"
                value={form.local_spoof} onChange={(e) => set("local_spoof", e.target.value.trim())} required />
              <small className="hint">IP که در هدر بسته به عنوان فرستنده جعل می‌شود.</small>
            </label>
            <label className="field">
              <span>مبدأ جعلی سمت خارج</span>
              <input className="input mono" dir="ltr" inputMode="decimal" placeholder="192.0.2.22"
                value={form.remote_spoof} onChange={(e) => set("remote_spoof", e.target.value.trim())} required />
              <small className="hint">سرور ایران انتظار دارد بسته‌ها از این IP برسند.</small>
            </label>
          </div>

          {/* Subnet and Name */}
          <div className="grid-2">
            <label className="field">
              <span>نام اتصال</span>
              <input className="input" maxLength={90} value={form.name} onChange={(e) => set("name", e.target.value)} required />
            </label>
            <label className="field">
              <span>شبکهٔ داخلی TUN (/30)</span>
              <input className="input mono" dir="ltr" value={form.tun_subnet} onChange={(e) => set("tun_subnet", e.target.value)} required />
              <small className="hint">پیشنهاد خودکار ساب‌نت خالی برای جلوگیری از تداخل رابط‌ها.</small>
            </label>
          </div>

          {/* Carriers Selection */}
          <div className="grid-2">
            <div className="field">
              <span>حامل رفت (ایران → خارج)</span>
              <select className="input" value={form.upload} onChange={(e) => set("upload", e.target.value)}>
                {CARRIERS.map((c) => (
                  <option key={c.value} value={c.value}>{c.label} — {c.desc}</option>
                ))}
              </select>
            </div>
            <div className="field">
              <span>حامل برگشت (خارج → ایران)</span>
              <select className="input" value={form.download} onChange={(e) => set("download", e.target.value)}>
                {CARRIERS.map((c) => (
                  <option key={c.value} value={c.value}>{c.label} — {c.desc}</option>
                ))}
              </select>
            </div>

            <label className="field">
              <span>پورت دریافت در ایران</span>
              <input className="input mono" dir="ltr" type="number" min={1} max={65535}
                value={form.local_port} onChange={(e) => set("local_port", Number(e.target.value))} />
            </label>
            <label className="field">
              <span>پورت دریافت در خارج</span>
              <input className="input mono" dir="ltr" type="number" min={1} max={65535}
                value={form.remote_port} onChange={(e) => set("remote_port", Number(e.target.value))} />
            </label>
          </div>

          {/* Role Choice */}
          <div className="field">
            <span>این پنل روی کدام سرور در حال اجراست؟</span>
            <select className="input" value={form.save_role} onChange={(e) => set("save_role", e.target.value)}>
              <option value="local">سرور ایران · local (سمت ایران در همین سرور ذخیره شود)</option>
              <option value="remote">سرور خارج · remote (سمت خارج در همین سرور ذخیره شود)</option>
              <option value="">فقط تولید فایل‌ها، بدون ذخیره روی این سرور</option>
            </select>
          </div>

          <label className="switch-row" style={{ padding: "8px 0" }}>
            <input type="checkbox" checked={form.auto_start} onChange={(e) => set("auto_start", e.target.checked)} />
            <span>شروع خودکار این اتصال پس از بالا آمدن پنل</span>
          </label>

          {error && <div role="alert" className="banner error">{error}</div>}

          <button className="btn btn-primary btn-block" disabled={busy || problems.length > 0} style={{ padding: "14px", fontSize: "15px" }}>
            {busy ? "در حال ایجاد کلید و تنظیمات…" : "ساخت تنظیمات هماهنگ دوطرفه ←"}
          </button>
          {problems.length > 0 && <p className="hint" style={{ color: "var(--accent-amber)" }}>{problems[0]}</p>}
        </form>

        {/* Visual Route Sidebar */}
        <aside className="stack">
          <div className="route-card">
            <span className="eyebrow">TOPOLOGY VISUALIZER</span>
            <div className="route-node">
              <span>سرور ایران</span>
              <small dir="ltr">{form.local_ip || "LOCAL IPv4"}</small>
            </div>
            <div className="route-wire">
              <span className="mono" style={{ fontWeight: 700 }}>
                {form.upload.toUpperCase()} ↓ ↑ {form.download.toUpperCase()}
              </span>
            </div>
            <div className="route-node">
              <span>سرور خارج</span>
              <small dir="ltr">{form.remote_ip || "REMOTE IPv4"}</small>
            </div>
            <p>
              مقصد بسته‌ها همیشه IP واقعی است؛ آدرس‌های مبدأ با IP جعلی جایگزین می‌شوند.
              فیلترهای ضداسپوف در سمت دریافت‌کننده بسته‌های غیرمجاز را بی‌صدا حذف می‌کنند.
            </p>
          </div>

          <div className="panel inner">
            <h3>امنیت و کلیدهای جهت‌دار</h3>
            <p className="hint">
              کلید PSK تولیدشده به صورت خودکار با برچسب‌های جهتی HMAC مشتق می‌شود
              (local-to-remote و remote-to-local) برای جلوگیری از پذیرش فریم بازتاب‌شده در جهت مخالف.
            </p>
          </div>
        </aside>
      </div>

      {/* Generated Result Display */}
      {result && (
        <section className="panel result-panel" role="status">
          <div className="section-head">
            <div>
              <span className="eyebrow" style={{ color: "var(--accent-cyan)" }}>OUTPUT READY</span>
              <h2>تنظیمات با موفقیت تولید شد</h2>
            </div>
            {result.saved ? (
              <Link className="btn btn-ghost btn-sm" href={base + "/dashboard/tunnels"}>مشاهده در اتصال‌ها ←</Link>
            ) : null}
          </div>
          <p className="page-intro">
            {result.saved
              ? "سمت انتخاب‌شده در لیست اتصال‌های همین سرور ذخیره شد. فایل سمت دیگر را دانلود کرده و در پنل سرور مقابل وارد (Import) کن."
              : "هر دو فایل ساخته شد. فایل‌ها را دانلود و روی هر سرور وارد کن."}
          </p>

          <div className="grid-2">
            {(["local", "remote"] as const).map((role) => (
              <div key={role} className="panel inner">
                <div className="section-head">
                  <h3>{role === "local" ? "سرور ایران (Local)" : "سرور خارج (Remote)"}</h3>
                  <span className="mode-badge">{role}</span>
                </div>
                <pre className="code" dir="ltr">{preview(result[role])}</pre>
                <div className="row">
                  <button className="btn btn-primary btn-sm" onClick={() => { download(`spoof-${role}.json`, result[role]); toast.success(`فایل spoof-${role}.json دانلود شد.`); }}>
                    <IconDownload size={14} />
                    <span>دانلود فایل JSON</span>
                  </button>
                  <button className="btn btn-ghost btn-sm" onClick={() => copy(role)}>
                    {copied === role ? (
                      <>
                        <IconCheck size={14} />
                        <span>کپی شد</span>
                      </>
                    ) : (
                      <>
                        <IconCopy size={14} />
                        <span>کپی تنظیمات</span>
                      </>
                    )}
                  </button>
                </div>
              </div>
            ))}
          </div>

          <div className="banner warn">
            <span>⚠ فایل دانلودی شامل کلید خصوصی رمزنگاری (PSK) است. آن را در پیام‌رسان‌ها یا شبکه‌های عمومی به اشتراک نگذار.</span>
          </div>
        </section>
      )}
    </div>
  );
}
