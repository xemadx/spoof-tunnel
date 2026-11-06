# Panel TLS certificates / سرتیفیکیت پنل

## فارسی

از v0.4.7 پنل می‌تواند با سرتیفیکیت Let’s Encrypt روی HTTPS کار کند. سرتیفیکیت
به‌ازای هر سرور جداست: هر سرور دامنهٔ خودش را می‌خواهد که به آی‌پی عمومی همان
سرور اشاره کند. پنل همیشه روی همان پورت قبلی می‌ماند؛ فقط `http://` به
`https://` تبدیل می‌شود و پیشوند تصادفی آدرس پنل عوض نمی‌شود.

**گرفتن سرتیفیکیت (نصب یا به‌روزرسانی با اسکریپت):** بعد از تأیید رمز، اگر
سرتیفیکیت معتبری تنظیم نشده باشد، نصب‌کننده می‌پرسد «سرتیفیکیت نداری —
نمی‌خوای بگیری؟». اگر بگویی بله، دامنه را می‌پرسد (تا سه بار؛ دامنه باید کامل
باشد، مثل `panel.example.com`؛ وایلدکارد قبول نمی‌شود چون HTTP-01 نمی‌تواند
آن را اثبات کند). بعد، پیش از certbot، سه چیز را بررسی می‌کند: دامنه به آی‌پی‌ای
حل شود، آن آی‌پی یکی از آی‌پی‌های همین سرور باشد، و پورت ۸۰ خالی باشد.
اگر چیزی درست نباشد، دقیق می‌گوید چه را درست کن و روی HTTP می‌ماند. certbot
(اگر نصب نباشد، با apt/dnf/yum نصب می‌شود) سرتیفیکیت را با چالش HTTP-01 روی
پورت ۸۰ می‌گیرد؛ پنل با `--set-tls` آن را بررسی می‌کند (جفت کلید با هم بخوانند،
در بازهٔ تاریخ باشند، SAN دامنه را پوشش دهد)، سرویس را راه‌اندازی مجدد می‌کند و
با `curl` روی `https://<دامنه>:<پورت>/<پیشوند>/api/health` صحت آن را می‌سنجد.
اگر هر مرحله‌ای شکست بخورد، پنل روی HTTP می‌ماند و چیزی نیمه‌کاره رها
نمی‌شود. در اجرای بعدی نصب‌کننده، اگر سرتیفیکیت داشته باشد، دیگر سؤال
نمی‌پرسد؛ فقط یک خط وضعیت نشان می‌دهد.

**اگر سرور پشت NAT است:** آی‌پی‌ای که سرویس‌های بیرونی مثل ipify می‌بینند فقط آی‌پی خروجی سرور است و ممکن است با آی‌پی ورودی (آی‌پی‌ای که دامنه باید به آن اشاره کند) فرق کند. نصب‌کننده به همین دلیل اول آی‌پی‌های روی اینترفیس‌های سرور و آی‌پی آدرس پنل را بررسی می‌کند و فقط در آخر سراغ آی‌پی خروجی می‌رود؛ اگر دامنه با هیچ‌کدام نخواند، می‌پرسد با این حال ادامه دهد. اگر آدرس چاپ‌شدهٔ پنل هم آی‌پی خروجی نادرست را نشان می‌دهد، پیش از نصب `SPOOF_PUBLIC_IP=<آی‌پی ورودی سرور>` را تنظیم کن.

**تمدید:** certbot تمدید را خودش انجام می‌دهد (`certbot renew`) و پنل
سرتیفیکیت تازه را بدون راه‌اندازی مجدد می‌خواند (بارگذاری پویا در هر
handshake). اگر سرتیفیکیت تا کمتر از ۳۰ روز دیگر منقضی شود، نصب‌کننده در
اجرای بعدی تمدید را امتحان می‌کند؛ اگر تمدید نشد، تاریخ انقضا را می‌گوید و پنل
روی همان سرتیفیکیت معتبر می‌ماند. در پنل، کارت سرتیفیکیت در صفحهٔ تنظیمات
تاریخ انقضا را نشان می‌دهد و از ۳۰ روز مانده، هشدار می‌دهد.

**تغییر دامنه یا قطع سرتیفیکیت:** برای دامنهٔ دیگر، اول با certbot برای دامنهٔ
جدید سرتیفیکیت بگیر، بعد `spoof-panel --set-tls --tls-domain <دامنه> --tls-cert
/etc/letsencrypt/live/<دامنه>/fullchain.pem --tls-key
/etc/letsencrypt/live/<دامنه>/privkey.pem` را اجرا کن و سرویس را راه‌اندازی
مجدد کن. برای برگشت به HTTP: `spoof-panel --disable-tls` و راه‌اندازی مجدد.
وضعیت را با `spoof-panel --tls-status` ببین.

**نکته‌های امنیتی:** سرتیفیکیت فقط برای دامنهٔ خودت صادر می‌شود؛ دامنه باید
واقعاً به همین سرور اشاره کند. پنل با سرتیفیکیت خراب یا گمشده بالا نمی‌آید روی
HTTPS قفل نمی‌شود: اگر `tls.json` فعال باشد ولی فایل‌ها خوانده نشوند، پنل با
هشدار بلند روی HTTP می‌آید. فایل `tls.json` در `/etc/spoof-panel` با دسترسی
0600 ذخیره می‌شود و شامل کلید خصوصی نیست — فقط مسیر فایل‌های certbot.

## English

From v0.4.7 the panel can serve HTTPS with a Let's Encrypt certificate. A
certificate is per server: each server needs its own domain pointing at its
own public IP. The panel keeps its configured port; only the scheme changes
from `http://` to `https://`, and the random web-path prefix is unchanged.

**Getting a certificate (install or update with the script):** after the
password is confirmed, if no valid certificate is configured, the installer
asks whether to get a free one. On yes it asks for the domain (up to three
tries; it must be a fully-qualified name such as `panel.example.com`;
wildcards are rejected because HTTP-01 cannot prove one). Before certbot it
pre-checks three things: the domain resolves, it resolves to one of this server's
addresses (interface addresses, the panel URL's IP, or the outbound address),
and port 80 is free. If the domain matches none of them, the installer shows
every address it found and asks whether to continue anyway and let certbot
decide; if the domain really points elsewhere, certbot fails and the panel
stays on HTTP. If anything is wrong it says exactly what to fix and stays on
HTTP. certbot (installed via apt/dnf/yum when missing)
obtains the certificate with the HTTP-01 challenge on port 80; the panel
validates it with `--set-tls` (pair loads, key matches, in date, SAN covers
the domain), restarts the service, and verifies the live endpoint with `curl`
against `https://<domain>:<port>/<prefix>/api/health`. Any failure leaves the
panel on HTTP; nothing is left half configured. On later installer runs, a
server that already has a certificate is never asked again — only one status
line is shown.

**Behind NAT:** the address outside services like ipify see is only the
server's egress address, and it can differ from the inbound address (the one
the domain must point at). The installer therefore checks the interface
addresses and the panel URL's IP first and only falls back to the egress
address; the panel binary does the same when printing its URL. If the printed
panel URL shows the wrong egress address, set
`SPOOF_PUBLIC_IP=<the server's inbound IP>` before running the installer.

**Renewal:** certbot renews (`certbot renew`) and the panel picks up renewed
files without a restart (dynamic reload on every handshake). If the
certificate expires within 30 days, the installer attempts renewal on its
next run; if renewal fails it reports the expiry date and the panel keeps
serving the still-valid certificate. The Settings page shows the expiry date
and warns from 30 days out.

**Changing domain or turning TLS off:** for another domain, obtain its
certificate with certbot first, then run `spoof-panel --set-tls --tls-domain
<domain> --tls-cert /etc/letsencrypt/live/<domain>/fullchain.pem --tls-key
/etc/letsencrypt/live/<domain>/privkey.pem` and restart the service. To go
back to HTTP: `spoof-panel --disable-tls` and restart. Check status with
`spoof-panel --tls-status`.

**Security notes:** a certificate is only issued for a domain that really
points at this server. A broken TLS configuration never bricks the panel: if
`tls.json` says enabled but the files cannot be read, the panel logs loudly
and falls back to plain HTTP. `tls.json` lives in `/etc/spoof-panel` with
mode 0600 and holds no private key — only the paths to certbot's files.

## رفع خطای Certbot در v0.4.10 / Certbot argument fix

در نسخه‌های v0.4.7 تا v0.4.9، تلاش برای صدور گواهی با خطای `unrecognized arguments: --register-unspecified` متوقف می‌شد. در v0.4.10 گزینهٔ معتبر `--register-unsafely-without-email` جایگزین شده است. برای تلاش دوباره، نصب‌کنندهٔ نسخهٔ اصلاح‌شده را اجرا کن، حفظ رمز فعلی را انتخاب و دامنه را دوباره وارد کن. خطای قبلی پیش از صدور گواهی رخ می‌داد؛ دریافت واقعی همچنان به DNS صحیح و دسترسی ورودی پورت ۸۰ نیاز دارد. دامنه، پورت و مسیر پنل برای هر سرور جدا باقی می‌مانند.

v0.4.10 fixes an invalid Certbot account-registration option in the installer. The supported no-email option is `--register-unsafely-without-email`; the earlier `--register-unspecified` fails before certificate issuance. Rerun the updated installer, retain the existing password, and enter the domain again. Regression tests now reject unsupported CLI options and run in CI; the corrected argument vector was also parsed by real Ubuntu 22.04 Certbot 1.21.0 without contacting an ACME server. This validates CLI compatibility, not public DNS/HTTP-01 reachability for any particular domain.

## اصلاح فعال‌سازی و آدرس HTTPS در v0.4.12 / HTTPS activation and URL fix

در v0.4.10 ممکن بود گواهی با موفقیت صادر شود، ولی بررسی فوریِ نصب‌کننده پیش از آماده‌شدن وب‌سرور شکست بخورد و TLS را غیرفعال کند. v0.4.12 پاسخ واقعی HTTPS را با تلاش مجدد تا حدود ۴۵ ثانیه بررسی می‌کند؛ درخواست محلی از پراکسی و تنظیمات curl کاربر عبور نمی‌کند، ولی اعتبار زنجیره و نام دامنه همچنان الزامی است. خطای دقیق curl نمایش داده می‌شود و پاسخ HTML به‌عنوان سلامت پنل پذیرفته نمی‌شود. وضعیت `true/false` خروجی پنل نیز درست شناخته می‌شود؛ اجرای دوباره برای گواهی فعال و معتبر درخواست صدور تازه نمی‌دهد.

پس از موفقیت، آدرس نمایش‌داده‌شده و `credentials.txt` شامل `https://<domain>:<port>/<prefix>/` هستند؛ حساب مدیر، پورت و مسیر قبلی حفظ می‌شوند. فرمان `--setup` و پیام شروع پنل نیز دامنهٔ گواهی را نشان می‌دهند. فعال‌کردن TLS با `--set-tls` به راه‌اندازی مجدد سرویس نیاز دارد. آزمون خودکار با گواهی یک CA آزمایشی و خودِ باینری پنل، تأخیر شروع، پراکسی، اعتبارسنجی نام دامنه و آدرس خروجی را بررسی می‌کند؛ این آزمون گواهی عمومی درخواست نمی‌کند.

v0.4.12 fixes false HTTPS failures after successful issuance. The installer retries the actual local HTTPS health endpoint for approximately 45 seconds, bypasses proxy settings and user curl configuration, and retains trusted-chain and hostname verification. It reports curl errors, rejects HTML masquerading as health JSON, and recognizes the panel's real `true/false` TLS status. Rerunning with an enabled, valid certificate does not request a new certificate.

On success, both the displayed URL and `credentials.txt` use `https://<domain>:<port>/<prefix>/`; the existing account, port and path are retained. Setup output and startup logs also use the certificate domain. The `--set-tls` command requires a service restart. CI tests the actual panel listener with a private fixture CA, delayed startup, proxy bypass, hostname rejection and URL preservation without contacting a public ACME service.
