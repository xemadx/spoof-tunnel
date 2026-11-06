"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type LogEvent } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { explainBenchmarkError } from "@/lib/explain";

const LEVEL_LABEL: Record<LogEvent["level"], string> = {
  error: "خطا",
  warn: "هشدار",
  info: "اطلاعات",
};

function formatTime(iso: string) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString("fa-IR", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export default function LogsPage() {
  const [events, setEvents] = useState<LogEvent[]>([]);
  const [error, setError] = useState("");
  const [live, setLive] = useState(true);
  const [autoScroll, setAutoScroll] = useState(true);
  const [level, setLevel] = useState<"all" | LogEvent["level"]>("all");
  const [instance, setInstance] = useState("all");
  const [query, setQuery] = useState("");
  const [clearedAt, setClearedAt] = useState(0);
  const [copied, setCopied] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    try {
      const res = await api.logs(500);
      if (!mounted.current) return;
      setEvents(res.events);
      setError("");
    } catch (e) {
      if (mounted.current)
        setError(e instanceof Error ? e.message : "دریافت لاگ انجام نشد.");
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    api
      .logs(500)
      .then((res) => {
        if (!mounted.current) return;
        setEvents(res.events);
        setError("");
      })
      .catch((e) => {
        if (mounted.current)
          setError(e instanceof Error ? e.message : "دریافت لاگ انجام نشد.");
      });
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    if (!live) return;
    const timer = setInterval(() => void load(), 3000);
    return () => clearInterval(timer);
  }, [live, load]);

  useEffect(() => {
    if (autoScroll && listRef.current) {
      listRef.current.scrollTop = listRef.current.scrollHeight;
    }
  }, [events, autoScroll]);

  const instances = Array.from(
    new Set(events.map((e) => e.instance).filter(Boolean)),
  ).sort();
  const visible = events.filter((e) => {
    if (new Date(e.time).getTime() < clearedAt) return false;
    if (level !== "all" && e.level !== level) return false;
    if (instance !== "all" && e.instance !== instance) return false;
    if (
      query.trim() &&
      !e.message.toLowerCase().includes(query.trim().toLowerCase())
    )
      return false;
    return true;
  });

  const copyAll = async () => {
    const text = visible
      .map(
        (e) =>
          `${formatTime(e.time)} [${LEVEL_LABEL[e.level]}]${e.instance ? ` ${e.instance}` : ""}: ${e.message}`,
      )
      .join("\n");
    if (await copyText(text)) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>لاگ‌ها</h1>
          <p className="page-subtitle">
            همهٔ خطاها، قطعی‌ها و رویدادهای پنل در یک کنسول؛ هر قطعی با دلیلش
            اینجا می‌آید.
          </p>
        </div>
      </div>

      {error && (
        <div className="alert alert-error" role="alert">
          {error}
        </div>
      )}

      <section className="panel">
        <div
          style={{ display: "flex", gap: 12, flexWrap: "wrap", alignItems: "end" }}
        >
          <label style={{ display: "grid", gap: 4 }}>
            <span className="hint">سطح</span>
            <select
              className="input"
              value={level}
              onChange={(e) =>
                setLevel(e.target.value as "all" | LogEvent["level"])
              }
              aria-label="فیلتر سطح"
            >
              <option value="all">همه</option>
              <option value="error">خطا</option>
              <option value="warn">هشدار</option>
              <option value="info">اطلاعات</option>
            </select>
          </label>
          <label style={{ display: "grid", gap: 4 }}>
            <span className="hint">اتصال</span>
            <select
              className="input"
              value={instance}
              onChange={(e) => setInstance(e.target.value)}
              aria-label="فیلتر اتصال"
            >
              <option value="all">همه</option>
              {instances.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </label>
          <label style={{ display: "grid", gap: 4, flex: "1 1 160px" }}>
            <span className="hint">جستجو در متن</span>
            <input
              className="input"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="مثلاً peer lost یا نام اتصال"
              aria-label="جستجو در لاگ"
            />
          </label>
          <label className="hint" style={{ display: "flex", gap: 6, alignItems: "center" }}>
            <input
              type="checkbox"
              checked={live}
              onChange={(e) => setLive(e.target.checked)}
            />
            زنده
          </label>
          <label className="hint" style={{ display: "flex", gap: 6, alignItems: "center" }}>
            <input
              type="checkbox"
              checked={autoScroll}
              onChange={(e) => setAutoScroll(e.target.checked)}
            />
            اسکرول خودکار
          </label>
          <button className="btn btn-ghost btn-sm" onClick={() => void copyAll()}>
            {copied ? "کپی شد" : "کپی همهٔ رویدادها"}
          </button>
          <button
            className="btn btn-ghost btn-sm"
            onClick={() => setClearedAt(Date.now())}
          >
            پاک کردن نمایش
          </button>
        </div>
        <p className="hint" style={{ marginTop: 12 }}>
          لاگ از زمان شروع پنل جمع می‌شود؛ با ری‌استارت پنل پاک می‌شود. «پاک
          کردن نمایش» فقط همین صفحه را خالی می‌کند.
        </p>

        <div className="log-viewer" ref={listRef} role="log" aria-label="کنسول لاگ">
          {visible.length === 0 && (
            <div className="log-info">
              هنوز رویدادی ثبت نشده است — یا فیلترها همه را پنهان کرده‌اند.
            </div>
          )}
          {visible.map((e, i) => {
            const explained = explainBenchmarkError(new Error(e.message));
            const key = `${e.time}-${e.instance_id}-${i}`;
            return (
              <div key={key} className={`log-${e.level}`}>
                <span className="mono">
                  [{formatTime(e.time)}] [{LEVEL_LABEL[e.level]}]
                  {e.instance ? ` [${e.instance}]` : ""}
                </span>{" "}
                {explained !== e.message ? (
                  <>
                    <span dir="auto">{explained}</span>
                    <br />
                    <small style={{ opacity: 0.65 }}>{e.message}</small>
                  </>
                ) : (
                  <span dir="auto">{e.message}</span>
                )}
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}
