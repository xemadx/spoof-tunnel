const reasons: Record<string, string> = {
  ready: "مبدأ و طرف مقابل آماده‌اند",
  no_verified_source: "هنوز مبدأ تأییدشده نداریم",
  awaiting_peer_manifest: "در انتظار پذیرش فهرست مبدأها",
  awaiting_peer_readiness: "در انتظار آمادگی موتور مقابل",
  reverse_reference_stale:
    "مرجع رفت‌وبرگشت تازه نیست؛ شکست به همهٔ مبدأها نسبت داده نمی‌شود",
};
const modes: Record<string, string> = {
  adaptive: "انتخاب کیفیت",
  round_robin: "چرخش مبدأهای سالم",
  failover: "جایگزینی هنگام خرابی",
  legacy: "گردش ساده",
};
const peer: Record<string, string> = {
  configured: "ذخیره‌شده در تنظیمات",
  waiting: "در انتظار",
  receiving: "در حال دریافت",
  synced: "همگام",
  error: "خطا",
};
export function formatTunnelLog(line: string): string {
  const index = line.indexOf("{");
  if (index < 0) return line;
  try {
    const event = JSON.parse(line.slice(index)) as Record<string, unknown>;
    if (event.event !== "spoof_pool") return line;
    const text = (key: string) =>
      typeof event[key] === "string" ? (event[key] as string) : "";
    const count = (key: string) =>
      typeof event[key] === "number" && Number.isFinite(event[key])
        ? (event[key] as number).toLocaleString("fa-IR")
        : "۰";
    const active = text("active") || "ندارد";
    return `${line.slice(0, index)}استخر | ${modes[text("mode")] || "روش نامشخص"} | ${reasons[text("reason")] || "بررسی وضعیت"} | قابل استفاده ${count("healthy")} از ${count("total")} | در آزمون ${count("testing")} | قرنطینه ${count("quarantined")} | ارزیابی نشده ${count("unknown")} | مبدأ فعال ${active} | فهرست مقابل ${peer[text("peer_status")] || "نامشخص"}، ${count("peer_count")} مبدأ${text("peer_status") === "receiving" ? "، " + count("peer_progress") + "٪" : ""}${text("detail") ? " | " + text("detail") : ""}`;
  } catch {
    return line;
  }
}
