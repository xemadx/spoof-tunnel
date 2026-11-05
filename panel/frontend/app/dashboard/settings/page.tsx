"use client";
import { useState } from "react";
import { api, setToken } from "@/lib/api";
import { PanelUpdate } from "@/lib/panel-update";
import { useToast } from "@/lib/toast";

const MIN = 12;
const MAX = 72;

export default function SettingsPage() {
  const [updating,setUpdating] = useState(false);
  const [oldPass, setOldPass] = useState("");
  const [newPass, setNewPass] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [loading, setLoading] = useState(false);
  const { toast } = useToast();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    setSuccess("");
    const byteLength = new TextEncoder().encode(newPass).length;
    if (byteLength < MIN) {
      setError(`رمز جدید باید حداقل ${MIN} بایت باشد.`);
      toast.error(`رمز جدید باید حداقل ${MIN} بایت باشد.`);
      return;
    }
    if (byteLength > MAX) {
      setError(`رمز جدید حداکثر ${MAX} بایت است.`);
      toast.error(`رمز جدید حداکثر ${MAX} بایت است.`);
      return;
    }
    if (newPass !== confirm) {
      setError("تکرار رمز جدید با رمز وارد شده مطابقت ندارد.");
      toast.error("تکرار رمز جدید با رمز وارد شده مطابقت ندارد.");
      return;
    }
    setLoading(true);
    try {
      const data = await api.changePassword(oldPass, newPass);
      if (data.token) setToken(data.token);
      const msg = "رمز عبور با موفقیت تغییر کرد و نشست‌های قبلی باطل شدند.";
      setSuccess(msg);
      toast.success(msg);
      setOldPass("");
      setNewPass("");
      setConfirm("");
    } catch (err) {
      const msg = err instanceof Error ? err.message : "تغییر رمز ناموفق بود";
      const userMsg = msg === "wrong password" ? "رمز عبور فعلی نادرست است." : msg;
      setError(userMsg);
      toast.error(userMsg);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="page narrow">
      <div className="page-head">
        <div>
          <div className="eyebrow">SECURITY / 04</div>
          <h1>تنظیمات پنل و حساب</h1>
          <p className="page-intro">مدیریت رمز عبور مدیر سیستم؛ با تغییر رمز، توکن‌های قبلی منقضی می‌شوند.</p>
        </div>
      </div>

      <PanelUpdate onBusy={setUpdating} />
      <section className="panel">
        <div className="section-head">
          <h2>تغییر رمز عبور مدیر</h2>
          <span className="pill muted">الگوریتم Bcrypt + Salt</span>
        </div>

        {error && <div role="alert" className="banner error">{error}</div>}
        {success && <div role="status" className="banner ok">{success}</div>}

        <form onSubmit={submit}><fieldset className="stack" disabled={updating} style={{border:0,padding:0,margin:0,minWidth:0}}>
          <label className="field">
            <span>رمز عبور فعلی</span>
            <input
              className="input mono"
              type="password"
              value={oldPass}
              onChange={(e) => setOldPass(e.target.value)}
              autoComplete="current-password"
              required
              dir="ltr"
            />
          </label>
          <label className="field">
            <span>رمز عبور جدید <small className="hint">({MIN} تا {MAX} بایت)</small></span>
            <input
              className="input mono"
              type="password"
              value={newPass}
              onChange={(e) => setNewPass(e.target.value)}
              autoComplete="new-password"
              required
              dir="ltr"
            />
          </label>
          <label className="field">
            <span>تکرار رمز عبور جدید</span>
            <input
              className="input mono"
              type="password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              autoComplete="new-password"
              required
              dir="ltr"
            />
          </label>
          <button className="btn btn-primary" type="submit" disabled={loading} style={{ marginTop: 8 }}>
            {loading ? "در حال ذخیره‌سازی…" : "ثبت و به‌روزرسانی رمز عبور"}
          </button>
        </fieldset></form>
      </section>

      <section className="panel inner">
        <h3>توصیه‌های امنیتی پنل</h3>
        <ul className="list">
          <li>پنل روی پروتکل HTTP داخلی اجرا می‌شود؛ حتماً از طریق SSH Port Forwarding یا ریورس‌پراکسی با گواهی TLS/SSL ارتباط برقرار کنید.</li>
          <li>فایل‌های تنظیمات دانلودی شامل کلید رمزنگاری محرمانه (PSK) هستند؛ آن‌ها را فقط در محیط‌های امن نگهداری کنید.</li>
          <li>پیشوند تصادفی URL پنل صرفاً یک سد در برابر اسکنرهای عمومی است و جایگزین رمز عبور قوی نمی‌شود.</li>
        </ul>
      </section>
    </div>
  );
}
