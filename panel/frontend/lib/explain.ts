// Persian explanations for benchmark/apply backend errors. The backend
// returns English errors; this is the single place that turns them into
// plain Persian with the fix the user must do. Used by the connection
// test page and by the Logs console.
export function explainBenchmarkError(e: unknown): string {
  const value = e instanceof Error ? e.message : "";
  // Dynamic backend errors: the conflicting tunnel name and subnet are
  // embedded in the message, so they are parsed out and rendered in
  // Persian with the fix. With auto-adopt these only fire for a real
  // conflict with a different tunnel (or several identical ones).
  const overlap = value.match(/^TUN subnet overlaps (.+) \(([^)]+)\)$/);
  if (overlap)
    return `زیرشبکهٔ TUN این اتصال با اتصال «${overlap[1]}» تداخل دارد (${overlap[2]}). یا زیرشبکهٔ دیگری بده، یا اتصالِ متداخل را حذف کن، یا اتصال مقصد را دستی انتخاب کن.`;
  const iface = value.match(/^interface (\S+) is already used by (.+)$/);
  if (iface)
    return `رابط TUN «${iface[1]}» همین حالا دست اتصال «${iface[2]}» است؛ رابط دیگری انتخاب کن یا آن اتصال را حذف کن.`;
  const errors: Record<string, string> = {
    benchmark_multiple_matching_connections:
      "چند اتصال با همین آی‌پی‌ها و زیرشبکه پیدا شد؛ اتصال مقصد را دستی انتخاب کن تا معلوم شود کدام یک جایگزین شود.",
    benchmark_local_ip_mismatch:
      "آی‌پی واقعی همین سرور با اتصال انتخاب‌شده یکی نیست؛ تنظیمات اتصال را دوباره دریافت کن.",
    benchmark_peer_ip_mismatch:
      "آی‌پی واقعی طرف مقابل با اتصال انتخاب‌شده یکی نیست؛ اتصال و جهت دو سرور را بررسی کن.",
    benchmark_source_not_in_pool:
      "مبدأ جعلی همین سرور در استخر فعال این اتصال نیست؛ یکی از مبدأهای گروه‌های اعمال‌شده را وارد کن.",
    benchmark_peer_source_not_admitted:
      "مبدأ جعلی طرف مقابل در فهرست مجاز این اتصال نیست؛ مبدأ را اصلاح یا فهرست طرف مقابل را همگام کن.",
    "benchmark endpoints or fixed sources do not match the selected connection":
      "آی‌پی‌های واقعی یا مبدأهای ثابت با اتصال انتخاب‌شده یکی نیستند؛ هر چهار فیلد را با استخر فعال بررسی کن.",
    "benchmark ports must match the selected receiver connection":
      "پورت‌ها باید با همین اتصال برابر باشند؛ تنظیمات اتصال را دوباره دریافت کن.",
    "benchmark ports must match the selected controller connection":
      "پورت‌های کد طرف مقابل با این اتصال برابر نیستند؛ نیمهٔ درست اتصال را در دو پنل انتخاب کن.",
  };
  return errors[value] || value || "عملیات انجام نشد؛ دوباره تلاش کنید.";
}
