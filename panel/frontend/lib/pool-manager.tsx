"use client";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { api, type PoolFiles, type PoolState, type PoolFile } from "./api";
import { Dialog } from "./dialog";
const MODES = [
  {
    id: "adaptive",
    title: "انتخاب بر اساس کیفیت",
    detail: "مبدأ سالم با پاسخ بهتر؛ تغییر هنگام افت کیفیت.",
  },
  {
    id: "round_robin",
    title: "چرخش میان مبدأهای قابل استفاده",
    detail:
      "هر بسته با مبدأ سالم بعدی؛ مبدأهای در حال آزمون و قرنطینه وارد گردش نمی‌شوند.",
  },
  {
    id: "failover",
    title: "جابه‌جایی هنگام خرابی",
    detail: "حفظ مبدأ سالم فعلی؛ پس از خرابی، انتخاب طبق اولویت فایل‌ها.",
  },
];
const STATES: Record<string, string> = {
  healthy: "سالم",
  suspect: "نیازمند بررسی",
  testing: "در حال آزمون",
  quarantined: "قرنطینه",
  unknown: "ارزیابی نشده",
};
const REASONS: Record<string, string> = {
  awaiting_confirmation: "در انتظار تأیید بعدی",
  cooldown_retest: "بازآزمایی پس از قرنطینه",
  verification_expired: "نیازمند تأیید تازه",
  reverse_unconfirmed: "مسیر برگشت هنوز تأیید نشده",
  probe_timeout: "عدم پاسخ به آزمون",
  large_probe_timeout: "عدم پاسخ به آزمون بستهٔ بزرگ",
  recent_probe_loss: "عدم پاسخ در آزمون‌های اخیر",
  restored_quarantine: "قرنطینهٔ حفظ‌شده پس از ری‌استارت",
};
const num = (n: number) => n.toLocaleString("fa-IR");
function explain(e: unknown) {
  const s =
    e instanceof Error ? e.message : "عملیات انجام نشد؛ دوباره تلاش کن.";
  const map: Record<string, string> = {
    "keep at least one non-empty group selected before clearing the default group":
      "پیش از خالی‌کردن گروه پیش‌فرض، یک گروه دارای آی‌پی دیگر را فعال کن.",
    "stop this connection before editing the default group":
      "برای ویرایش گروه پیش‌فرض، ابتدا همین اتصال را متوقف کن.",
    "select between 1 and 65 non-empty groups":
      "حداقل یک گروه دارای آی‌پی را انتخاب کن.",
    "an empty default group cannot be selected":
      "گروه پیش‌فرض خالی است؛ ابتدا آی‌پی وارد کن.",
    "peer engine does not support smart pools; update both servers":
      "موتور طرف مقابل از استخر هوشمند پشتیبانی نمی‌کند؛ هر دو سرور را به‌روز کن.",
    "peer source synchronization timed out":
      "دریافت فهرست به پایان نرسید؛ ارتباط و نسخهٔ طرف مقابل را بررسی کن.",
    "peer source pool overlaps this server's source pool":
      "استخر طرف مقابل با مبدأهای این سرور هم‌پوشانی دارد؛ فهرست‌های دو جهت را جدا کن.",
    "this address file is already imported": "این فایل قبلاً وارد شده است.",
    "stop this connection before applying its pool selection":
      "ابتدا همین اتصال را متوقف کن.",
    "connection changed; reload its pool settings":
      "تنظیمات تغییر کرده؛ فهرست را دوباره دریافت کن.",
    "unselect and apply this file before deleting it":
      "ابتدا انتخاب این فایل را بردار و اعمال کن.",
    "start the connection with an updated engine before requesting peer sources":
      "ابتدا اتصال را با موتور به‌روز اجرا کن.",
  };
  return s.includes("expected one unicast IPv4")
    ? `هر خط باید یک IPv4 باشد؛ خط نامعتبر: ${s.match(/line (\d+)/)?.[1] ?? "—"}`
    : (map[s] ?? s);
}
export function PoolManager({
  id,
  local,
  running,
  active,
  version,
  onApplied,
}: {
  id: number;
  local: boolean;
  running: boolean;
  active: boolean;
  version: string;
  onApplied: (
    version: string,
    patch?: Record<string, string | number | boolean>,
  ) => void;
}) {
  const inputId = useId();
  const mounted = useRef(false);
  const busyRef = useRef(false);
  const request = useRef(0);
  const knownFiles = useRef(new Set<number>());
  const versionRef = useRef(version);
  const draftDirtyRef = useRef(false);
  const [deleting, setDeleting] = useState<PoolFile | null>(null);
  const closeDelete = useCallback(() => {
    if (!busyRef.current) setDeleting(null);
  }, []);
  const [conflict, setConflict] = useState(false);
  const [manual, setManual] = useState("");
  const [manualBase, setManualBase] = useState("");
  const [manualVersion, setManualVersion] = useState("");
  const [manualLoaded, setManualLoaded] = useState(false);
  const [data, setData] = useState<PoolFiles>();
  const [ids, setIDs] = useState<number[]>([]);
  const [mode, setMode] = useState("legacy");
  const [pps, setPPS] = useState(25);
  const [auto, setAuto] = useState(false);
  const [bounded, setBounded] = useState(false);
  const [capacity, setCapacity] = useState(100);
  const [minutes, setMinutes] = useState(30);
  const [state, setState] = useState<PoolState>();
  const [offset, setOffset] = useState(0);
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [pollError, setPollError] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    versionRef.current = version;
  }, [version]);
  const refresh = useCallback(
    async (preserve = false) => {
      const ticket = ++request.current;
      const result = await api.poolFiles(id);
      if (!mounted.current || ticket !== request.current) return;
      const newEnabled = result.files
        .filter((f) => f.enabled && !knownFiles.current.has(f.id))
        .map((f) => f.id);
      knownFiles.current = new Set(result.files.map((f) => f.id));
      setConflict(
        !!versionRef.current && result.updated_at !== versionRef.current,
      );
      setData(result);
      if (preserve && draftDirtyRef.current) {
        setIDs((current) =>
          Array.from(
            new Set([
              ...current.filter((value) =>
                result.files.some((f) => f.id === value && f.count > 0),
              ),
              ...newEnabled,
            ]),
          ),
        );
      } else {
        setIDs(result.files.filter((f) => f.enabled).map((f) => f.id));
        setMode(result.mode);
        setPPS(result.probe_pps);
        setBounded(!!result.active_size);
        setCapacity(result.active_size || 100);
        setMinutes((result.refresh_seconds || 1800) / 60);
        setAuto(result.auto_peer_sync);
      }
    },
    [id],
  );
  useEffect(() => {
    mounted.current = true;
    void refresh(knownFiles.current.size > 0).catch((e) => {
      if (mounted.current) setError(explain(e));
    });
    return () => {
      mounted.current = false;
    };
  }, [refresh, running, version]);
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const controller = new AbortController();
    async function poll() {
      if (active && running && document.visibilityState !== "hidden") {
        try {
          const result = await api.poolState(
            id,
            offset,
            filter,
            controller.signal,
          );
          if (!cancelled) {
            setState(result);
            setPollError(false);
          }
        } catch {
          if (!cancelled) setPollError(true);
        }
      }
      if (!cancelled) timer = setTimeout(poll, 3000);
    }
    if (active && running) void poll();
    else if (!running) setState(undefined);
    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [id, running, active, offset, filter]);
  useEffect(() => {
    if (!state?.runtime?.pool.quarantined) return;
    const tick = () => setNow(Date.now());
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, [state?.runtime?.pool.quarantined]);
  const action = async (key: string, work: () => Promise<void>) => {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(key);
    setError("");
    setNotice("");
    try {
      await work();
    } catch (e) {
      if (mounted.current) setError(explain(e));
    } finally {
      busyRef.current = false;
      if (mounted.current) setBusy("");
    }
  };
  const upload = (files: FileList | null) => {
    if (!files?.length) return;
    const chosen = Array.from(files);
    void action("upload", async () => {
      let count = 0;
      try {
        for (const file of chosen) {
          if (!file.name.toLowerCase().endsWith(".txt"))
            throw Error("فقط فایل TXT انتخاب کن.");
          if (file.size > 10 * 1024 * 1024)
            throw Error("حداکثر حجم هر فایل ۱۰ مگابایت است.");
          await api.poolUpload(id, file);
          count++;
        }
      } finally {
        await refresh(true);
        if (count)
          setNotice(
            `${num(count)} فایل اضافه شد. فایل‌های موردنظر را انتخاب و اعمال کن.`,
          );
      }
    });
  };
  const readManual = async () => {
    const result = await api.poolDefault(id);
    setManual(result.content);
    setManualBase(result.content);
    setManualVersion(result.updated_at);
    setManualLoaded(true);
    if (versionRef.current && versionRef.current !== result.updated_at)
      setConflict(true);
  };
  const saveManual = () =>
    void action("default-save", async () => {
      await saveAll();
    });
  const manualDirty = manualLoaded && manual !== manualBase;
  const validPPS =
    Number.isInteger(pps) &&
    pps >= 1 &&
    pps <= 100 &&
    (!bounded ||
      (mode !== "legacy" &&
        Number.isInteger(capacity) &&
        capacity >= 1 &&
        capacity <= Math.min(400, 4 * pps) &&
        Number.isInteger(minutes * 60) &&
        minutes >= 1 &&
        minutes <= 1440));
  const validSelection = !!data?.files.some(
    (file) =>
      ids.includes(file.id) &&
      (file.kind === "default" && manualDirty
        ? manual.trim() !== ""
        : file.count > 0),
  );
  const move = (id: number, delta: number) => {
    draftDirtyRef.current = true;
    setIDs((current) => {
      const list = [...current],
        i = list.indexOf(id),
        j = i + delta;
      if (i < 0 || j < 0 || j >= list.length) return current;
      [list[i], list[j]] = [list[j], list[i]];
      return list;
    });
  };
  const saveAll = async () => {
    if (!data) return;
    const settings = {
      ids,
      mode,
      probe_pps: pps,
      active_size: bounded ? capacity : 0,
      refresh_seconds: Math.round(minutes * 60),
      auto_peer_sync: auto,
    };
    const result = manualDirty
      ? await api.poolDefaultSave(
          id,
          manual,
          versionRef.current || manualVersion,
          settings,
        )
      : await api.poolSettings(id, {
          ...settings,
          updated_at: versionRef.current,
        });
    versionRef.current = result.updated_at;
    onApplied(result.updated_at, {
      pool_mode: mode,
      pool_pps: pps,
      pool_active_size: bounded ? capacity : 0,
      pool_refresh_seconds: Math.round(minutes * 60),
      auto_peer_sync: auto,
    });
    draftDirtyRef.current = false;
    await refresh();
    if (manualLoaded) await readManual();
    setManualVersion(result.updated_at);
    setOffset(0);
    setNotice(
      `${num(result.total)} مبدأ یکتا ذخیره شد؛ محتوای پیش‌فرض، انتخاب گروه‌ها و روش ارسال هماهنگ اعمال شدند.`,
    );
  };
  const apply = () => void action("apply", saveAll);
  const rt = state?.runtime,
    peer = rt?.peer;
  const dirty =
    !!data &&
    (mode !== data.mode ||
      pps !== data.probe_pps ||
      (bounded ? capacity : 0) !== (data.active_size || 0) ||
      Math.round(minutes * 60) !== (data.refresh_seconds || 1800) ||
      auto !== data.auto_peer_sync ||
      ids.join() !==
        data.files
          .filter((f) => f.enabled)
          .map((f) => f.id)
          .join());
  useEffect(() => {
    draftDirtyRef.current = dirty;
  }, [dirty]);
  const totalRows =
    data?.files
      .filter((f) => ids.includes(f.id))
      .reduce((n, f) => n + f.count, 0) ?? 0;
  const files = [...(data?.files ?? [])].sort((a, b) => {
    const ai = ids.indexOf(a.id),
      bi = ids.indexOf(b.id);
    return (
      (ai < 0 ? 10000 + a.position : ai) - (bi < 0 ? 10000 + b.position : bi)
    );
  });
  const retry = (until?: number) => {
    if (!until) return "—";
    const left = Math.max(0, until - Math.floor(now / 1000));
    return left
      ? `${num(Math.floor(left / 60))}:${String(left % 60).padStart(2, "0")}`
      : "در صف بازآزمایی";
  };
  return (
    <div className="pool-manager stack">
      <section className="panel pool-hero">
        <div className="section-head">
          <div>
            <span className="pool-eyebrow">SOURCE POOLS</span>
            <h2>استخر مبدأهای اسپوف</h2>
            <p className="hint">
              {local
                ? "ارسال از این سرور ایران به سرور خارج"
                : "ارسال از این سرور خارج به سرور ایران"}
            </p>
          </div>
          <span className="pill">قرنطینهٔ موقت: ۱۰ دقیقه</span>
        </div>
        <p className="page-intro">
          فایل‌ها را انتخاب کن؛ مبدأهای تکراری یک بار حساب می‌شوند. در روش‌های
          هوشمند، مبدأ پیش از ارسال داده باید آزمون معتبر را بگذراند.
        </p>
        <div className="pool-metrics">
          {[
            ["مبدأ ذخیره‌شده", rt?.pool.total ?? data?.total ?? 0],
            ["قابل استفاده", rt?.pool.healthy ?? 0],
            ["در حال آزمون", rt?.pool.testing ?? 0],
            ["قرنطینه", rt?.pool.quarantined ?? 0],
            ["ارزیابی نشده", rt?.pool.unknown ?? data?.total ?? 0],
          ].map(([label, value]) => (
            <div key={label}>
              <span>{label}</span>
              <strong>{num(Number(value))}</strong>
            </div>
          ))}
        </div>
        {!data && !error && (
          <p className="hint" aria-live="polite">
            در حال دریافت فایل‌ها…
          </p>
        )}
        {!running && (
          <p className="hint">
            برای سنجش کیفیت، اتصال را اجرا کن. آمار سلامت پس از شروع نمایش داده
            می‌شود.
          </p>
        )}
        {rt?.persistence_error && (
          <p className="pool-note" role="alert">
            ذخیرهٔ وضعیت قرنطینه انجام نشد؛ فضای دیسک و دسترسی فایل‌های پنل را
            بررسی کن.
          </p>
        )}
        {pollError && (
          <p className="pool-note" role="status">
            دریافت وضعیت قطع شده؛ آمار نمایش‌داده‌شده آخرین نتیجه است.
          </p>
        )}
        {running && rt && rt.pool.mode !== "legacy" && !rt.control_fresh && (
          <p className="pool-note">
            مسیر رفت‌وبرگشت هنوز تأیید نشده؛ نبود پاسخ در این وضعیت، باعث
            قرنطینهٔ گروهی مبدأها نمی‌شود.
          </p>
        )}
        {running &&
          rt &&
          auto &&
          mode !== "legacy" &&
          !rt.our_sources_accepted && (
            <p className="pool-note">
              در انتظار تأیید فهرست ارسال توسط طرف مقابل؛ داده پس از پذیرش فهرست
              و تأیید سلامت ارسال می‌شود.
            </p>
          )}
        {running && state && !state.available && (
          <p className="pool-note">
            وضعیت استخر در دسترس نیست؛ اجرا بودن موتور و به‌روز بودن هر دو سرور
            را بررسی کن.
          </p>
        )}
      </section>
      {running && rt && rt.pool.mode !== "legacy" && !rt.peer_ready && (
        <div className="pool-note" role="status">
          طرف مقابل هنوز آمادهٔ ارسال داده نیست؛ منتظر تکمیل تأیید مبدأ و
          همگام‌سازی آن سمت هستیم.
        </div>
      )}
      {conflict && (
        <div className="pool-feedback error" role="alert">
          تنظیمات اتصال در جای دیگری تغییر کرده است. بازخوانی صفحه، تغییرات
          ذخیره‌نشده را کنار می‌گذارد.
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => window.location.reload()}
          >
            بازخوانی صفحه
          </button>
        </div>
      )}
      {error && (
        <div className="pool-feedback error" role="alert">
          {error}
          <button
            className="btn btn-ghost btn-sm"
            disabled={!!busy}
            onClick={() => void action("refresh", () => refresh(true))}
          >
            دریافت دوباره
          </button>
        </div>
      )}
      {notice && (
        <div className="pool-feedback" role="status">
          {notice}
        </div>
      )}
      <section className="panel">
        <div className="section-head">
          <div>
            <h2>گروه‌های استخر این سرور</h2>
            <p className="hint">
              TXT · هر خط یک IPv4 · تا ۲۶۲٬۱۴۴ مبدأ یکتا در هر اتصال
            </p>
          </div>
          <label
            className={`btn btn-primary btn-sm ${busy ? "disabled" : ""}`}
            htmlFor={inputId}
          >
            {busy === "upload" ? "در حال بارگذاری…" : "+ افزودن فایل TXT"}
          </label>
          <input
            id={inputId}
            className="pool-file-input"
            type="file"
            accept=".txt,text/plain"
            multiple
            disabled={!!busy}
            onChange={(e) => {
              upload(e.target.files);
              e.target.value = "";
            }}
          />
        </div>
        {data?.legacy_content && !files.length && (
          <div className="pool-note">
            <p>
              فهرست قبلی محفوظ است؛ برای مدیریت آن با فایل‌ها، یک بار آن را
              تبدیل کن.
            </p>
            <button
              className="btn btn-ghost btn-sm"
              disabled={!!busy}
              onClick={() =>
                void action("migrate", async () => {
                  await api.poolMigrate(id);
                  await refresh(true);
                  setNotice(
                    "استخر قبلی به فایل تبدیل شد؛ روش ارسال تغییر نکرد.",
                  );
                })
              }
            >
              تبدیل استخر فعلی به فایل
            </button>
          </div>
        )}
        {!!files.length && (
          <div className="pool-selection">
            <label>
              <input
                type="checkbox"
                checked={
                  files.some((f) => f.count > 0) &&
                  ids.length === files.filter((f) => f.count > 0).length
                }
                disabled={running || !!busy}
                onChange={(e) => {
                  draftDirtyRef.current = true;
                  setIDs(
                    e.target.checked
                      ? files.filter((f) => f.count > 0).map((f) => f.id)
                      : [],
                  );
                }}
              />
              انتخاب همهٔ گروه‌ها
            </label>
            <span>
              {num(ids.length)} گروه انتخاب‌شده · {num(totalRows)} ردیف پیش از
              حذف هم‌پوشانی
            </span>
          </div>
        )}
        {data && ids.length === 0 && (
          <p className="hint">
            حداقل یک گروه دارای آی‌پی را برای استفاده انتخاب کن.
          </p>
        )}
        <div className="pool-files">
          {!files.length && data && (
            <div className="pool-empty">
              <strong>فایل‌هایت را اینجا نگه دار</strong>
              <p>
                خروجی آی‌پی‌های موفق همین جهت را اضافه کن، سپس فایل‌های موردنظر
                را انتخاب کن.
              </p>
            </div>
          )}
          {files.map((file) => (
            <div
              key={file.id}
              className={`pool-file-row ${ids.includes(file.id) ? "selected" : ""}`}
            >
              <label>
                <input
                  type="checkbox"
                  checked={ids.includes(file.id)}
                  disabled={running || !!busy || file.count === 0}
                  onChange={() => {
                    draftDirtyRef.current = true;
                    setIDs((v) =>
                      v.includes(file.id)
                        ? v.filter((x) => x !== file.id)
                        : [...v, file.id],
                    );
                  }}
                />
                <span>
                  <bdi dir="auto">{file.name}</bdi>
                  <small>
                    {file.kind === "default"
                      ? "ورود مستقیم آی‌پی · "
                      : "فایل TXT · "}
                    {num(file.count)} مبدأ یکتا
                    {file.enabled ? " · در تنظیمات ذخیره شده" : ""}
                  </small>
                </span>
              </label>
              <div className="pool-file-actions">
                {ids.includes(file.id) && (
                  <>
                    <span>اولویت {num(ids.indexOf(file.id) + 1)}</span>
                    <button
                      className="btn btn-ghost btn-sm"
                      aria-label={`اولویت بالاتر ${file.name}`}
                      disabled={running || !!busy || ids.indexOf(file.id) === 0}
                      onClick={() => move(file.id, -1)}
                    >
                      بالاتر
                    </button>
                    <button
                      className="btn btn-ghost btn-sm"
                      aria-label={`اولویت پایین‌تر ${file.name}`}
                      disabled={
                        running ||
                        !!busy ||
                        ids.indexOf(file.id) === ids.length - 1
                      }
                      onClick={() => move(file.id, 1)}
                    >
                      پایین‌تر
                    </button>
                  </>
                )}
                {file.kind !== "default" && !file.enabled && (
                  <button
                    className="btn btn-ghost btn-sm danger-text"
                    disabled={!!busy}
                    onClick={() => setDeleting(file)}
                  >
                    حذف
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      </section>
      <section className="panel">
        <details
          className="pool-manual"
          onToggle={(e) => {
            if (e.currentTarget.open && !manualLoaded)
              void action("default-load", readManual);
          }}
        >
          <summary>ویرایش آی‌پی‌های دستیِ گروه پیش‌فرض</summary>
          <p className="hint">
            ذخیره، محتوای پیش‌فرض و انتخاب فعلی گروه‌ها و روش ارسال را یکجا
            اعمال می‌کند. آی‌پی‌های دستی فقط در گروه «پیش‌فرض» ذخیره می‌شوند. هر
            فایل TXT گروه مستقل خودش را دارد.
          </p>
          {manualLoaded ? (
            <>
              <label className="field">
                <span>آی‌پی‌های گروه پیش‌فرض</span>
                <textarea
                  className="input mono"
                  dir="ltr"
                  rows={7}
                  value={manual}
                  disabled={running || !!busy}
                  spellCheck={false}
                  onChange={(e) => setManual(e.target.value)}
                  placeholder="هر خط یک IPv4"
                />
              </label>
              {bounded && !validPPS && (
                <p className="pool-note" role="alert">
                  بودجهٔ آزمون باید ۱ تا ۱۰۰ بسته در ثانیه، تعداد منتخب ۱ تا{" "}
                  {num(Math.max(1, Math.min(400, 4 * pps)))} و بازسازی ۱ تا ۱۴۴۰
                  دقیقه باشد.
                </p>
              )}
              <div className="pool-apply">
                <p className="hint">
                  {manualDirty && !validSelection
                    ? "برای ذخیرهٔ پیش‌فرض خالی، یک گروه TXT دارای آی‌پی را انتخاب کن."
                    : running
                      ? "برای ویرایش این گروه، ابتدا اتصال را متوقف کن."
                      : "برای فهرست‌های بزرگ، فایل TXT مستقل اضافه کن."}
                </p>
                <div className="pool-file-actions">
                  <button
                    className="btn btn-ghost"
                    disabled={!!busy || !manualDirty}
                    onClick={() => setManual(manualBase)}
                  >
                    کنار گذاشتن تغییرات
                  </button>
                  <button
                    className="btn btn-primary"
                    disabled={
                      running ||
                      !!busy ||
                      conflict ||
                      !manualDirty ||
                      !validPPS ||
                      !validSelection
                    }
                    onClick={saveManual}
                  >
                    {busy === "default-save"
                      ? "در حال ذخیره…"
                      : "ذخیرهٔ گروه پیش‌فرض"}
                  </button>
                </div>
              </div>
            </>
          ) : (
            <p className="hint">
              {busy === "default-load"
                ? "در حال دریافت آی‌پی‌های دستی…"
                : "برای دریافت دوباره، این بخش را ببند و باز کن."}
            </p>
          )}
        </details>
      </section>
      <section className="panel">
        <h2>روش استفاده از مبدأها</h2>
        <div className="pool-note">
          <div>
            <label className="pool-check">
              <input
                type="checkbox"
                checked={bounded}
                disabled={running || !!busy}
                onChange={(e) => {
                  draftDirtyRef.current = true;
                  setBounded(e.target.checked);
                  if (e.target.checked && mode === "legacy") {
                    setMode("adaptive");
                    setAuto(true);
                  }
                }}
              />
              <strong>استفاده از مجموعهٔ منتخب</strong>
            </label>
            <p className="hint">
              داده فقط با مبدأهای تأییدشدهٔ منتخب ارسال می‌شود. آزمون کشف از
              بقیهٔ آی‌پی‌های گروه‌های فعال هم استفاده می‌کند؛ بهترین‌ها از میان
              مبدأهای ارزیابی‌شده انتخاب می‌شوند.
            </p>
            {bounded && (
              <div className="grid-2">
                <label className="field">
                  <span>سقف مبدأهای در حال استفاده</span>
                  <input
                    className="input mono"
                    type="number"
                    min={1}
                    aria-label="سقف مبدأهای در حال استفاده"
                    max={Math.min(400, 4 * pps)}
                    value={capacity}
                    disabled={running || !!busy}
                    onChange={(e) => {
                      draftDirtyRef.current = true;
                      setCapacity(Number(e.target.value));
                    }}
                  />
                  <small className="hint">
                    تا {num(Math.min(400, 4 * pps))} مبدأ با بودجهٔ آزمون فعلی؛
                    جای خالی با مبدأ تأییدنشده پر نمی‌شود.
                  </small>
                </label>
                <label className="field">
                  <span>بازسازی مجموعه هر چند دقیقه</span>
                  <input
                    className="input mono"
                    type="number"
                    min={1}
                    max={1440}
                    step={1}
                    aria-label="بازسازی مجموعه هر چند دقیقه"
                    value={minutes}
                    disabled={running || !!busy}
                    onChange={(e) => {
                      draftDirtyRef.current = true;
                      setMinutes(Number(e.target.value));
                    }}
                  />
                  <small className="hint">
                    خرابی زودتر جبران می‌شود؛ پیش از بازسازی، نامزدهای برتر
                    دوباره آزموده می‌شوند.
                  </small>
                </label>
              </div>
            )}
          </div>
        </div>
        <div
          className="pool-modes"
          role="radiogroup"
          aria-label="روش انتخاب مبدأ"
        >
          {MODES.map((item) => (
            <label
              key={item.id}
              className={`pool-mode ${mode === item.id ? "selected" : ""}`}
            >
              <input
                type="radio"
                name={`pool-mode-${id}`}
                checked={mode === item.id}
                disabled={running || !!busy}
                onChange={() => {
                  draftDirtyRef.current = true;
                  setMode(item.id);
                  setAuto(true);
                }}
              />
              <span>
                <strong>{item.title}</strong>
                <small>{item.detail}</small>
              </span>
            </label>
          ))}
        </div>
        {mode === "legacy" && (
          <p className="pool-note">
            این اتصال در حالت قبلیِ گردش ساده است؛ کیفیت‌سنجی فعال نیست. برای
            فعال‌سازی یکی از سه روش بالا را انتخاب کن. موتور هر دو سرور باید
            به‌روز باشد.
          </p>
        )}
        <details className="pool-advanced">
          <summary>تنظیمات آزمون و هماهنگی</summary>
          <div className="grid-2">
            <label className="field">
              <span>سقف بسته‌های کنترل در ثانیه</span>
              <input
                className="input mono"
                type="number"
                min={1}
                max={100}
                value={pps}
                disabled={running || !!busy}
                onChange={(e) => {
                  draftDirtyRef.current = true;
                  setPPS(Number(e.target.value));
                }}
              />
              <small className="hint">
                فهرست‌های بزرگ تدریجی و با این بودجه ارزیابی می‌شوند.
              </small>
            </label>
            <label className="pool-check">
              <input
                type="checkbox"
                checked={auto}
                disabled={running || !!busy}
                onChange={(e) => {
                  draftDirtyRef.current = true;
                  setAuto(e.target.checked);
                }}
              />
              <span>دریافت خودکار مبدأهای مجاز طرف مقابل هنگام شروع</span>
            </label>
          </div>
        </details>
        {bounded && !validPPS && (
          <p className="pool-note" role="alert">
            بودجهٔ آزمون باید ۱ تا ۱۰۰ بسته در ثانیه، تعداد منتخب ۱ تا{" "}
            {num(Math.max(1, Math.min(400, 4 * pps)))} و بازسازی ۱ تا ۱۴۴۰ دقیقه
            باشد.
          </p>
        )}
        <div className="pool-apply">
          <span className="hint">
            {manualDirty
              ? "محتوای پیش‌فرض و انتخاب گروه‌ها با هم اعمال می‌شوند؛ برای خالی‌کردن پیش‌فرض، گروه دیگری را انتخاب کن."
              : running && dirty
                ? "اتصال با نسخهٔ ذخیره‌شده اجراست؛ تغییرات این صفحه هنوز اعمال نشده‌اند."
                : running
                  ? "برای تغییر گروه‌های فعال و روش ارسال، ابتدا همین اتصال را متوقف کن."
                  : dirty
                    ? "تغییرات هنوز اعمال نشده‌اند."
                    : "تنظیمات با نسخهٔ ذخیره‌شده هماهنگ است."}
          </span>
          <button
            className="btn btn-primary"
            disabled={
              running ||
              !!busy ||
              conflict ||
              (!dirty && !manualDirty) ||
              !data ||
              !validSelection ||
              !validPPS
            }
            onClick={apply}
          >
            {busy === "apply" ? "در حال اعمال…" : "اعمال انتخاب و روش ارسال"}
          </button>
        </div>
      </section>
      <section className="panel">
        <div className="section-head">
          <div>
            <h2>مبدأهای مجازِ طرف مقابل</h2>
            <p className="hint">
              فهرست ارسال سرور مقابل، برای دریافت در همین سرور ثبت می‌شود.
            </p>
          </div>
          <button
            className="btn btn-ghost"
            disabled={
              !running ||
              !!busy ||
              (peer?.in_flight ?? peer?.status === "receiving")
            }
            onClick={() =>
              void action("sync", async () => {
                await api.poolSync(id);
                setNotice(
                  "درخواست دریافت فهرست ارسال شد؛ پیشرفت در همین بخش نمایش داده می‌شود.",
                );
              })
            }
          >
            دریافت از طرف مقابل
          </button>
        </div>
        {peer?.in_flight && (
          <button
            className="btn btn-ghost btn-sm"
            disabled={!!busy}
            onClick={() =>
              void action("cancel-sync", async () => {
                await api.poolCancelSync(id);
                setNotice("دریافت لغو شد؛ فهرست معتبر قبلی محفوظ است.");
              })
            }
          >
            لغو دریافت
          </button>
        )}
        <div className="pool-peer">
          <strong>{num(peer?.count ?? data?.peer_count ?? 0)} مبدأ</strong>
          <span className="pill">
            {peer?.status === "configured"
              ? "فهرست ذخیره‌شده در تنظیمات"
              : peer?.status === "synced"
                ? "فهرست معتبر دریافت شد"
                : peer?.status === "receiving"
                  ? `دریافت ${num(peer.progress)}٪`
                  : peer?.status === "error"
                    ? "دریافت ناموفق"
                    : running
                      ? "در انتظار هماهنگی"
                      : "اتصال متوقف است"}
          </span>
        </div>
        {peer?.status === "receiving" && (
          <progress
            aria-label="پیشرفت دریافت مبدأها"
            max={100}
            value={peer.progress}
          />
        )}{" "}
        {peer?.error && (
          <p className="pool-note">{explain(new Error(peer.error))}</p>
        )}
        <p className="hint">
          فهرست کامل و احرازشده جایگزین می‌شود؛ دریافت ناقص، فهرست فعلی را تغییر
          نمی‌دهد. موتور به‌روز باید در هر دو سمت فعال باشد.
        </p>
      </section>
      <section className="panel">
        <div className="section-head">
          <div>
            <h2>کیفیت مبدأهای این سرور</h2>
            <p className="hint">
              تأخیر، زمان رفت‌وبرگشت آزمون است؛ شمارنده‌ها مربوط به همین اجرای
              اتصال‌اند.
            </p>
          </div>
          <label className="field">
            <span>فیلتر وضعیت</span>
            <select
              className="input"
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value);
                setOffset(0);
              }}
            >
              <option value="">همهٔ وضعیت‌ها</option>
              <option value="selected">فقط مبدأهای منتخب</option>
              {Object.entries(STATES).map(([key, label]) => (
                <option key={key} value={key}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>
        {!!rt?.pool.active_size && (
          <div className="pool-note">
            <div>
              <strong>
                {num(rt.pool.selected || 0)} از {num(rt.pool.active_size)} مبدأ
                منتخب
              </strong>
              <p className="hint">
                نسل {num(rt.pool.generation || 1)} ·{" "}
                {rt.pool.rebuilding
                  ? "بازآزمایی نامزدهای بازسازی در حال انجام است"
                  : `بازسازی بعدی: ${rt.pool.refresh_at ? new Date(rt.pool.refresh_at * 1000).toLocaleTimeString("fa-IR") : "—"}`}
              </p>
            </div>
          </div>
        )}
        {rt?.pool.active && (
          <p className="hint">
            آخرین مبدأ انتخاب‌شده برای داده:{" "}
            <bdi className="mono" dir="ltr">
              {rt.pool.active}
            </bdi>
          </p>
        )}
        <div className="pool-table-wrap">
          <table className="pool-table">
            <thead>
              <tr>
                <th>مبدأ</th>
                <th>وضعیت</th>
                <th>تأخیر</th>
                <th>پاسخ / آزمون</th>
                <th>عدم پاسخ تخمینی</th>
                <th>بازآزمایی</th>
              </tr>
            </thead>
            <tbody>
              {(rt?.pool.items ?? []).map((row) => (
                <tr key={row.ip}>
                  <td>
                    <bdi className="mono" dir="ltr">
                      {row.ip}
                    </bdi>
                  </td>
                  <td>
                    <span className={`pool-state ${row.state}`}>
                      {STATES[row.state] ?? row.state}
                      {row.selected ? " · منتخب" : ""}
                    </span>
                    {row.reason && REASONS[row.reason] && (
                      <small className="pool-reason">
                        {REASONS[row.reason]}
                      </small>
                    )}
                  </td>
                  <td>
                    {row.replies ? `${num(Math.round(row.rtt_ms))} ms` : "—"}
                  </td>
                  <td>
                    <bdi dir="ltr">
                      {num(row.replies)} / {num(row.probes)}
                    </bdi>
                  </td>
                  <td>
                    {row.replies > 0 || row.failures > 0
                      ? `${num(Math.round(row.loss_pct))}٪`
                      : "—"}
                  </td>
                  <td>{retry(row.retry_at)}</td>
                </tr>
              ))}
              {!rt?.pool.items.length && (
                <tr>
                  <td colSpan={6} className="pool-empty">
                    {running
                      ? "هنوز نتیجه‌ای برای نمایش وجود ندارد."
                      : "با شروع اتصال، نتایج آزمون نمایش داده می‌شود."}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="pool-pagination">
          <button
            className="btn btn-ghost btn-sm"
            disabled={offset === 0}
            onClick={() => setOffset((v) => Math.max(0, v - 50))}
          >
            صفحهٔ قبل
          </button>
          <span className="hint">
            صفحهٔ {num(Math.floor(offset / 50) + 1)} · حداکثر ۵۰ مبدأ در هر صفحه
          </span>
          <button
            className="btn btn-ghost btn-sm"
            disabled={!rt || rt.pool.items.length < 50}
            onClick={() => setOffset((v) => v + 50)}
          >
            صفحهٔ بعد
          </button>
        </div>
        <p className="hint">
          مبدأ ناموفق پس از چند آزمون، ۱۰ دقیقه کنار گذاشته می‌شود؛ سپس وارد صف
          بازآزمایی خواهد شد. پایان شمارش معکوس به‌تنهایی مبدأ را سالم نمی‌کند.
        </p>
      </section>
      {deleting && (
        <Dialog title="حذف گروه TXT" onClose={closeDelete}>
          <p>
            گروه <bdi dir="auto">{deleting.name}</bdi> با {num(deleting.count)}{" "}
            مبدأ حذف شود؟ فایل اصلی روی کامپیوترت و گروه‌های دیگر تغییری
            نمی‌کنند.
          </p>
          <div className="pool-file-actions">
            <button
              className="btn btn-ghost"
              disabled={!!busy}
              onClick={closeDelete}
            >
              انصراف
            </button>
            <button
              className="btn btn-danger"
              disabled={!!busy}
              onClick={() =>
                void action("delete", async () => {
                  const target = deleting;
                  try {
                    await api.poolDelete(id, target.id);
                    await refresh(true);
                    setNotice("گروه TXT حذف شد.");
                  } finally {
                    setDeleting(null);
                  }
                })
              }
            >
              {busy === "delete" ? "در حال حذف…" : "حذف گروه"}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
