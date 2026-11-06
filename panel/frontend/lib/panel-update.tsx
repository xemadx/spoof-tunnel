"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, type UpdateStatus } from "./api";
import { Dialog } from "./dialog";

const KEY = "spoof-panel-pending-update";
const phases: Record<string, string> = {
  queued: "در صف شروع",
  checking: "بررسی نسخه و بسته‌ها",
  downloading: "دریافت از گیت‌هاب",
  verifying: "بررسی checksum و نسخهٔ فایل‌ها",
  backing_up: "پشتیبان‌گیری از دیتابیس",
  installing: "نصب پنل و موتور",
  restarting: "راه‌اندازی دوبارهٔ پنل",
  rolling_back: "بازگرداندن نسخهٔ قبلی",
  succeeded: "به‌روزرسانی با موفقیت انجام شد",
  rolled_back: "آپدیت کامل نشد؛ نسخهٔ قبلی بازیابی شد",
  failed: "به‌روزرسانی انجام نشد",
};
const terminal = new Set(["succeeded", "rolled_back", "failed"]);
function message(error: unknown) {
  const value = error instanceof Error ? error.message : "";
  const messages: Record<string, string> = {
    github_unreachable: "ارتباط سرور با گیت‌هاب برقرار نشد؛ دوباره بررسی کن.",
    github_http_403:
      "گیت‌هاب فعلاً درخواست را محدود کرده است؛ کمی بعد دوباره بررسی کن.",
    github_http_429:
      "تعداد درخواست‌ها به گیت‌هاب زیاد شده؛ کمی بعد دوباره بررسی کن.",
    update_already_running:
      "یک به‌روزرسانی در حال اجراست؛ وضعیت آن را دنبال کن.",
    release_changed_or_not_newer:
      "نسخهٔ گیت‌هاب تغییر کرده یا جدیدتر نیست؛ دوباره بررسی کن.",
    missing_release_assets:
      "بستهٔ متناسب با این سرور یا checksum هنوز منتشر نشده است.",
  };
  return (
    messages[value] ||
    "عملیات انجام نشد؛ اتصال سرور به گیت‌هاب و وضعیت سرویس را بررسی کن."
  );
}
export function PanelUpdate({ onBusy }: { onBusy: (value: boolean) => void }) {
  const [status, setStatus] = useState<UpdateStatus>();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [offline, setOffline] = useState(false);
  const [watch, setWatch] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const mounted = useRef(false),
    serial = useRef(0),
    busyRef = useRef(false);
  const phase = status?.job?.phase;
  const active = !!phase && !terminal.has(phase);
  const close = useCallback(() => {
    if (!busyRef.current) setConfirm(false);
  }, []);
  const receive = useCallback((next: UpdateStatus) => {
    setStatus(next);
    setOffline(false);
    const job = next.job,
      pending = sessionStorage.getItem(KEY);
    if (job && !terminal.has(job.phase)) {
      sessionStorage.setItem(KEY, job.id);
      setWatch(true);
    } else if (
      pending &&
      job &&
      (pending === job.id || pending === `target:${job.target}`)
    ) {
      sessionStorage.removeItem(KEY);
      setWatch(false);
      if (job.phase === "succeeded" && next.current === job.target)
        window.location.reload();
    } else if (pending && (!job || terminal.has(job.phase))) {
      sessionStorage.removeItem(KEY);
      setWatch(false);
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    setWatch(!!sessionStorage.getItem(KEY));
    const ticket = ++serial.current;
    api
      .updateStatus()
      .then((result) => {
        if (mounted.current && ticket === serial.current) receive(result);
      })
      .catch(() => {
        if (mounted.current)
          setError("دریافت وضعیت آپدیت ناموفق بود؛ دوباره بررسی کن.");
      });
    return () => {
      mounted.current = false;
    };
  }, [receive]);
  useEffect(() => {
    onBusy(active || watch || busy === "install");
  }, [active, watch, busy, onBusy]);
  useEffect(() => {
    if (!active && !watch) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    const poll = async () => {
      if (!busyRef.current && document.visibilityState !== "hidden") {
        const ticket = ++serial.current;
        try {
          const result = await api.updateStatus(controller.signal);
          if (!cancelled && ticket === serial.current) receive(result);
        } catch {
          if (!cancelled) setOffline(true);
        }
      }
      if (!cancelled) timer = setTimeout(poll, 2000);
    };
    timer = setTimeout(poll, 1000);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [active, watch, receive]);
  const check = async () => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy("check");
    setError("");
    const ticket = ++serial.current;
    try {
      const result = await api.updateCheck();
      if (mounted.current && ticket === serial.current) receive(result);
    } catch (error) {
      if (mounted.current) setError(message(error));
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy("");
    }
  };
  const install = async () => {
    if (!status?.latest || busyRef.current) return;
    const target = status.latest;
    setConfirm(false);
    busyRef.current = true;
    setBusy("install");
    setError("");
    sessionStorage.setItem(KEY, `target:${target}`);
    setWatch(true);
    ++serial.current;
    try {
      const result = await api.updateInstall(target);
      if (mounted.current) receive(result);
    } catch (error) {
      if (mounted.current) {
        setError(message(error));
        try {
          receive(await api.updateStatus());
        } catch {
          setOffline(true);
        }
      }
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy("");
    }
  };
  const unsupported: Record<string, string> = {
    requires_linux_root:
      "آپدیت داخلی برای نصب Linux با دسترسی root و معماری amd64 یا arm64 فعال است.",
    requires_standard_service:
      "پنل باید از طریق سرویس spoof-panel اجرا شود؛ اجرای دستی برای آپدیت داخلی پشتیبانی نمی‌شود.",
    requires_standard_install:
      "برای آپدیت داخلی، پنل باید با نصب‌کنندهٔ رسمی و سرویس systemd نصب شده باشد.",
    missing_update_service:
      "سرویس آپدیت نصب نیست؛ یک بار نصب‌کنندهٔ جدید را روی این سرور اجرا کن.",
    unversioned_build:
      "این بیلد برچسب انتشار ندارد؛ نسخهٔ رسمی را با نصب‌کننده نصب کن.",
  };
  return (
    <section className="panel panel-update" aria-label="به‌روزرسانی پنل">
      <div className="section-head">
        <div>
          <h2>به‌روزرسانی پنل</h2>
          <p className="hint">
            دریافت نسخهٔ رسمی از گیت‌هاب؛ پنل و موتور با هم به‌روز می‌شوند.
          </p>
        </div>
        <span className="pill muted">GitHub Releases</span>
      </div>
      <div className="update-versions">
        <div>
          <span>نسخهٔ نصب‌شده</span>
          <strong dir="ltr">{status?.current || "در حال دریافت…"}</strong>
        </div>
        <div>
          <span>آخرین نسخهٔ بررسی‌شده</span>
          <strong dir="ltr">{status?.latest || "هنوز بررسی نشده"}</strong>
          {status?.latest &&
            (status.signed ? (
              <span className="pill success">امضاشده</span>
            ) : (
              <span className="pill muted">بدون امضا</span>
            ))}
        </div>
      </div>
      {status && !status.supported && (
        <p className="pool-note">
          {unsupported[status.reason || ""] ||
            "آپدیت داخلی در این نصب در دسترس نیست."}
        </p>
      )}
      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}
      {offline && (
        <p className="pool-note" role="status">
          ارتباط پنل موقتاً قطع است؛ وضعیت را دوباره دریافت می‌کنیم. هنگام
          راه‌اندازی مجدد، این حالت طبیعی است. نصب دوباره‌ای شروع نمی‌شود.
        </p>
      )}
      {status?.job && (
        <div className={`update-job ${status.job.phase}`} role="status">
          <div>
            <strong>{phases[status.job.phase] || "دریافت وضعیت"}</strong>
            <bdi dir="ltr">
              {status.job.from} / {status.job.target}
            </bdi>
          </div>
          {(!terminal.has(status.job.phase) ||
            status.job.phase === "succeeded") && (
            <>
              <div className="update-progress-label">
                <span>پیشرفت به‌روزرسانی</span>
                <strong>{status.job.progress.toLocaleString("fa-IR")}٪</strong>
              </div>
              <progress
                max={100}
                value={status.job.progress}
                aria-label="پیشرفت به‌روزرسانی"
              />
              {status.job.phase === "downloading" &&
                !!status.job.bytes_total && (
                  <p className="hint">
                    {((status.job.bytes_done || 0) / 1048576).toLocaleString(
                      "fa-IR",
                      { maximumFractionDigits: 1 },
                    )}{" "}
                    از{" "}
                    {(status.job.bytes_total / 1048576).toLocaleString(
                      "fa-IR",
                      { maximumFractionDigits: 1 },
                    )}{" "}
                    مگابایت دریافت شد
                  </p>
                )}
            </>
          )}
          {status.job.error && (
            <>
              <p>جزئیات عملیات:</p>
              <code dir="ltr">{status.job.error}</code>
              <p className="hint">
                لاگ سرویس:{" "}
                <bdi dir="ltr">
                  journalctl -u spoof-panel-update -n 80 --no-pager
                </bdi>
              </p>
            </>
          )}
        </div>
      )}
      <p className="hint">
        نصب، پنل و اتصال‌های فعال این سرور را برای مدتی راه‌اندازی مجدد می‌کند.
        تنظیمات و حساب مدیر حفظ می‌شوند؛ پیش از جایگزینی، checksum و نسخهٔ هر دو
        باینری بررسی و از دیتابیس پشتیبان تهیه می‌شود.
      </p>
      <div className="update-actions">
        <button
          className="btn btn-ghost"
          disabled={!!busy || active || watch}
          onClick={() => void check()}
        >
          {busy === "check" ? "در حال بررسی…" : "بررسی نسخهٔ جدید"}
        </button>
        {status?.available && (
          <button
            className="btn btn-primary"
            disabled={!status.supported || !!busy || active || watch}
            onClick={() => setConfirm(true)}
          >
            نصب نسخهٔ <bdi dir="ltr">{status.latest}</bdi>
          </button>
        )}
        {status?.latest && !status.available && (
          <span className="pill success">نسخهٔ جدیدتری موجود نیست</span>
        )}
        {status?.release_url && (
          <a
            className="btn btn-ghost"
            href={status.release_url}
            target="_blank"
            rel="noopener noreferrer"
          >
            توضیحات انتشار
          </a>
        )}
      </div>
      {confirm && status?.latest && (
        <Dialog title="نصب به‌روزرسانی پنل" onClose={close}>
          <p>
            نسخهٔ <bdi dir="ltr">{status.latest}</bdi> از مخزن رسمی دریافت و روی
            همین سرور نصب می‌شود. پنل و اتصال‌ها راه‌اندازی مجدد می‌شوند و پس از
            موفقیت، این صفحه تازه می‌شود.
          </p>
          <div className="update-actions">
            <button className="btn btn-ghost" onClick={close}>
              انصراف
            </button>
            <button className="btn btn-primary" onClick={() => void install()}>
              شروع نصب <bdi dir="ltr">{status.latest}</bdi>
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
