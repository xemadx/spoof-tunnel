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
حل شود، آن آی‌پی با آی‌پی عمومی همین سرور برابر باشد، و پورت ۸۰ خالی باشد.
اگر چیزی درست نباشد، دقیق می‌گوید چه را درست کن و روی HTTP می‌ماند. certbot
(اگر نصب نباشد، با apt/dnf/yum نصب می‌شود) سرتیفیکیت را با چالش HTTP-01 روی
پورت ۸۰ می‌گیرد؛ پنل با `--set-tls` آن را بررسی می‌کند (جفت کلید با هم بخوانند،
در بازهٔ تاریخ باشند، SAN دامنه را پوشش دهد)، سرویس را راه‌اندازی مجدد می‌کند و
با `curl` روی `https://<دامنه>:<پورت>/<پیشوند>/api/health` صحت آن را می‌سنجد.
اگر هر مرحله‌ای شکست بخورد، پنل روی HTTP می‌ماند و چیزی نیمه‌کاره رها
نمی‌شود. در اجرای بعدی نصب‌کننده، اگر سرتیفیکیت داشته باشد، دیگر سؤال
نمی‌پرسد؛ فقط یک خط وضعیت نشان می‌دهد.

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
pre-checks three things: the domain resolves, it resolves to this server's
public IP, and port 80 is free. If anything is wrong it says exactly what to
fix and stays on HTTP. certbot (installed via apt/dnf/yum when missing)
obtains the certificate with the HTTP-01 challenge on port 80; the panel
validates it with `--set-tls` (pair loads, key matches, in date, SAN covers
the domain), restarts the service, and verifies the live endpoint with `curl`
against `https://<domain>:<port>/<prefix>/api/health`. Any failure leaves the
panel on HTTP; nothing is left half configured. On later installer runs, a
server that already has a certificate is never asked again — only one status
line is shown.

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
