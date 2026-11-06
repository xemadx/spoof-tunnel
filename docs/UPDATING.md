# In-panel updates / آپدیت از داخل پنل

## فارسی

برای اضافه‌شدن این قابلیت به v0.4.0، **یک بار نصب‌کننده را اجرا و به v0.4.1 ارتقا بده**. نصب‌کننده، سرویس مستقل `spoof-panel-update.service` را نیز نصب می‌کند. آپدیت‌های بعدی در «تنظیمات، به‌روزرسانی پنل» انجام می‌شوند:

1. «بررسی نسخهٔ جدید» را بزن. نسخهٔ نصب‌شده و آخرین انتشار پایدار مخزن `xemadx/spoof-tunnel` نمایش داده می‌شوند.
2. نسخهٔ جدید را انتخاب و نصب همان نسخه را تأیید کن. پنل و موتور این سرور با هم به‌روز می‌شوند؛ سرور دیگر را جداگانه به‌روز کن.
3. مرحله، درصد پیشرفت و حجم واقعی دریافت‌شده نمایش داده می‌شود. درصد کلی بر اساس مراحل تکمیل‌شده است؛ درصد دانلود از بایت‌های واقعی دریافت‌شده محاسبه می‌شود و تخمین زمان نیست.
4. پس از پاسخ سالم نسخهٔ جدید، صفحه فقط یک بار تازه می‌شود و تگ بالای پنل نسخهٔ در حال اجرای جدید را نشان می‌دهد. حساب، رمز، توکن معتبر و تنظیمات حفظ می‌شوند.

نصب، پنل و اتصال‌های فعال همان سرور را راه‌اندازی مجدد می‌کند. هنگام قطع موقت HTTP، صفحه وضعیت را دوباره دریافت می‌کند و درخواست نصب جدیدی نمی‌فرستد. با بازکردن دوبارهٔ تنظیمات نیز وضعیت عملیات قبلی بازیابی می‌شود.

آپدیت داخلی برای نصب استاندارد Linux با root، systemd و معماری amd64 یا arm64 است. اجرای دستی، مسیر نصب سفارشی یا نبود سرویس آپدیت، علت مشخص در رابط دارد. فقط نسخهٔ پایدار جدیدتر پذیرفته می‌شود؛ آدرس دانلود یا دستور دلخواه از مرورگر دریافت نمی‌شود.

پیش از توقف پنل، checksum آرشیو، checksum داخلی هر دو باینری، معماری ELF و خروجی نسخهٔ هر دو فایل بررسی می‌شوند. اسکریپت داخل آرشیو اجرا نمی‌شود. پس از توقف سرویس، پشتیبان سازگار SQLite تهیه می‌شود؛ فایل WAL نادیده گرفته نمی‌شود. باینری‌ها با rename اتمی جایگزین می‌شوند. اگر راه‌اندازی نسخهٔ جدید شکست بخورد، باینری‌ها و دیتابیس قبلی بازگردانده و سرویس دوباره اجرا می‌شود.

وضعیت در `/etc/spoof-panel/updates/job.json`، و پشتیبان هر عملیات در `/etc/spoof-panel/updates/<ID>/` نگهداری می‌شود؛ پوشه خصوصی و پشتیبان دیتابیس با دسترسی 0600 است. پشتیبان‌های قبلی برای بازیابی دستی حفظ می‌شوند و تا حذف دستی فضا مصرف می‌کنند. قطع برق یا خرابی دیسک خارج از تضمین برگشت خودکار معمول است؛ برای بازیابی دستی، ابتدا وضعیت تکمیل پشتیبان و لاگ را بررسی کن.

از نسخهٔ v0.4.4 به بعد، انتشار باید امضای Ed25519 فایل `SHA256SUMS` را هم داشته باشد (`SHA256SUMS.sig`)؛ بدون امضا یا با امضای نامعتبر، آپدیت رد می‌شود. کلید خصوصی آفلاین نگه داشته می‌شود و فقط در workflow انتشار با secret گیت‌هاب استفاده می‌شود؛ کلید عمومی داخل پنل است. انتشارهای قدیمی‌تر بدون امضا هم پذیرفته می‌شوند، ولی در تنظیمات «بدون امضا» نشان داده می‌شوند.

اگر حین نصب برق برود یا ورکر kill شود، پنل در راه‌اندازی بعدی ناهماهنگی دو باینری را تشخیص می‌دهد: اگر هر دو جدید باشند آپدیت کامل شده؛ اگر هر دو قدیمی باشند هیچی عوض نشده؛ اگر فقط هسته جدید باشد، آپدیت از روی فایل‌های stage کامل و سرویس ری‌استارت می‌شود؛ در غیر این صورت باینری‌های قبلی بازیابی می‌شوند. اگر هیچ‌کدام ممکن نباشد، پنل خطا را لاگ می‌کند و بازیابی دستی لازم است.

```sh
journalctl -u spoof-panel-update -n 80 --no-pager
systemctl status spoof-panel-update
```

## English

Run the installer once to move from v0.4.0 to v0.4.1 and install the fixed updater unit. Later upgrades use **Settings → Panel update**. Only a newer stable release from the configured GitHub repository is accepted. A check does not install anything; installation names the checked version explicitly.

The worker runs in a separate systemd cgroup, survives the panel's stop, and writes bounded persistent progress. It verifies the outer archive checksum, both embedded binary checksums, ELF architecture and exact version output before stopping the panel. Extraction is bounded, rejects unsafe paths/symlinks/duplicate binaries, and never executes archive installation scripts. No panel token or password is forwarded to GitHub.

After service stop, `VACUUM INTO` produces a consistent database snapshot including committed WAL contents. Previous binaries are saved privately; replacement uses same-directory atomic renames. The new process must answer the local version health endpoint. Startup failure restores the prior database and binaries, then checks the restored process. Cancellation attempts rollback using a separate bounded context. Ordinary rollback does not promise recovery from hardware failure or sudden power loss.

The browser reports byte-derived download progress and completed installation stages, tolerates temporary HTTP loss without resubmitting installation, and reloads once after the target version responds. The header obtains its version from the new backend. Active links restart according to their saved enabled configuration.

The panel service retains its existing restricted filesystem. Only the fixed updater service is allowed to replace the standard binary pair. Custom/manual installs are reported as unsupported. Backups remain under the private updates directory until manually removed.

SQLite's [VACUUM INTO documentation](https://www.sqlite.org/lang_vacuum.html) describes snapshot consistency and power-loss limits. The systemd configuration follows the [systemd execution sandbox](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html).

From v0.4.4, a release must also carry `SHA256SUMS.sig`, an Ed25519 signature of the exact `SHA256SUMS` bytes made with an offline key. The private seed lives only as the GitHub Actions secret `SPOOF_RELEASE_SIGNING_KEY`; the public key is embedded in the panel (see `cmd/sign-release`). A missing or invalid signature rejects the update. Older unsigned releases are still accepted and are shown as unsigned in Settings.

If power fails or the worker is killed mid-install, the panel reconciles on its next start: both binaries new means the update completed; both old means nothing changed; a new core with an old panel is completed from the staged binaries and the service restarts; any other mix restores the previous binaries. If neither is possible the panel logs the failure and manual recovery from `updates/<id>/` is required.

## UI review

| Before | After | Why |
| --- | --- | --- |
| Default content and selected TXT groups saved separately | Both save controls commit content, selection and policy atomically | A checked replacement works when clearing the default |
| SSH installer required for every update | Checked stable release, explicit install and persistent progress | The operator can follow one update without repeating requests |
| HTTP restart looked like a failed action | Reconnect status, one final reload and new header tag | Distinguish service restart from a failed installation |
| Raw pool JSON and a history/live gap | Readable bounded summaries, atomic snapshot/subscription and batched rendering | Keep diagnostics useful without duplicate boundary records or DOM growth |
