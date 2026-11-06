import { test, expect, type Page } from '@playwright/test';
const prefix = '/reviewprefix';
const runtimeErrors = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => { const errors: string[] = []; runtimeErrors.set(page, errors); page.on('pageerror', error => errors.push(error.message)); });
test.afterEach(({ page }) => { expect(runtimeErrors.get(page)).toEqual([]); });
async function mock(page: Page, running = false) {
 let instance = { updated_at: '2026-11-05T12:00:00Z', id: 1, name: 'Review tunnel', mode: 'local', engine: 'tun', status: 'stopped', uptime: 30, send_transport: 'icmp', recv_transport: 'icmp', tun_interface: 'spcheck', tun_address: '10.77.0.1/30', tun_peer: '10.77.0.2', remote_addr: '192.0.2.2', spoof_ip: '198.51.100.1', peer_spoof_ip: '198.51.100.2', spoof_ip_list: '198.51.100.1', remote_port: 8090, recv_port: 5001, enabled: true };
 let poolFiles = [{ id: 1, kind: 'default', name: 'پیش‌فرض', count: 1, enabled: true, position: 0, hash: 'first' }, { id: 2, kind: 'file', name: 'فارس.txt', count: 1, enabled: false, position: 1, hash: 'second' }];
 let poolMode = running ? 'adaptive' : 'legacy';
 let manualContent = '198.51.100.1';
 let poolVersion = instance.updated_at;
 const writes: { path: string; data: Record<string, unknown> }[] = [];
 let statusCalls = 0;
 await page.addInitScript(() => { localStorage.setItem('token', 'review'); if (!localStorage.getItem('spoof-theme')) localStorage.setItem('spoof-theme', 'light'); });
 await page.route('**/api/**', async route => {
  const request = route.request(); const path = new URL(request.url()).pathname.split('/api')[1];
  let data: unknown = {}; let status = 200;
  if (request.method() === 'PUT') writes.push({ path, data: request.postDataJSON() });
  if (path === '/system') data = { panel_version: 'test', hostname: 'test', os: 'linux', arch: 'amd64', cpus: 2, memory_mb: 20 };
  else if (path === '/instances') data = [instance];
  else if (path === '/dashboard') data = { total: 2, running_count: 0, stopped_count: 2, error_count: 0, instances: [instance, { ...instance, id: 2 }].map((i, index) => ({ ...i, telemetry: { peer_connected: false, tx_bytes: index ? 8192 : 1024, rx_bytes: 2048, drops: 0, last_peer: 0 } })) };
  else if (path === '/instances/1') {
   if (request.method() === 'PUT') instance = { ...instance, ...request.postDataJSON() };
   data = request.method() === 'PUT' ? instance : { instance, status: running ? 'running' : 'stopped', uptime: 0 };
  } else if (path === '/instances/1/pools') {
   data = { files: poolFiles, mode: poolMode, probe_pps: 25, auto_peer_sync: poolMode !== 'legacy', total: 1, peer_count: 1, legacy_content: false, updated_at: poolVersion };
  } else if (path === '/instances/1/pools/default') {
   if (request.method() === 'POST') {
    const matches = request.postDataBuffer()?.toString('utf8').match(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g) ?? [];
    manualContent = Array.from(new Set(matches)).join('\n');
    poolFiles[0].count = manualContent ? manualContent.split('\n').length : 0;
    const selected=request.postDataBuffer()?.toString('utf8').match(/name="settings"\r\n\r\n([^\r\n]+)/);
    if(selected){const settings=JSON.parse(selected[1]);poolMode=settings.mode;poolFiles=poolFiles.map(f=>({...f,enabled:settings.ids.includes(f.id)&&f.count>0}));}
    if (poolFiles[0].enabled) instance.spoof_ip_list = manualContent;
    else if(poolFiles[1]?.enabled) instance.spoof_ip_list='198.51.100.22';
   }
   data = { content: manualContent, count: poolFiles[0].count, total: instance.spoof_ip_list ? 1 : 0, updated_at: poolVersion };
  } else if (path === '/instances/1/pools/settings') {
   const body = request.postDataJSON(); poolMode = body.mode;poolVersion='2026-11-05T12:00:01Z';instance.updated_at=poolVersion;
   poolFiles = poolFiles.map(f => ({ ...f, enabled: body.ids.includes(f.id) }));
   instance.spoof_ip_list = body.ids.includes(2) ? '198.51.100.22' : '198.51.100.1';
   data = { ok: true, total: 1, updated_at: '2026-11-05T12:00:01Z' };
  } else if (path === '/instances/1/pools/state') {
   data = { available: true, runtime: { peer_ready: true, control_fresh: true, our_sources_accepted: true, peer: { supported: true, status: 'synced', count: 12919, progress: 100, revision: 'review-peer' }, pool: { mode: 'adaptive', total: 122437, healthy: 512, testing: 3, quarantined: 2, unknown: 121920, active: '45.157.244.1', offset: 0, items: [{ ip: '45.157.244.1', state: 'healthy', rtt_ms: 24, loss_pct: 0, probes: 10, replies: 10 }, { ip: '45.157.244.2', state: 'quarantined', rtt_ms: 0, loss_pct: 48, probes: 3, replies: 0, retry_at: Math.floor(Date.now()/1000)+600 }] } } };
  } else if (path === '/instances/1/pools/upload') {
   data = { id: 3, kind: 'file', name: 'جدید.txt', count: 3, enabled: false, position: 2, hash: 'third' };
   poolFiles.push(data as typeof poolFiles[number]);
  } else if (path.startsWith('/instances/1/pools/') && request.method() === 'DELETE') { const id = Number(path.split('/').at(-1)); poolFiles = poolFiles.filter(f => f.id !== id); data = { ok: true };
  } else if (path === '/instances/1/pools/sync') { data = { ok: true };
  } else if (path === '/instances/1/spoof-ips') {
   if (request.method() === 'PUT') { instance.spoof_ip_list = request.postDataJSON().content; data = { ok: true, count: 1, instance }; }
   else data = { content: instance.spoof_ip_list, count: 1 };
  } else if (path.endsWith('/status') && path !== '/tester/status') data = { status: running ? 'running' : 'stopped', uptime: 0 };
  else if (path === '/tester/status') { statusCalls++; data = { status: 'running', mode: 'receiver', total_ips: 1, progress: 10, recv_count: 0, results: [] }; }
  else if (path === '/auth/login') { status = 401; data = { error: 'invalid credentials' }; }
  else if (path.endsWith('/forwards')) data = [];
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) });
 });
 return { writes, statusCalls: () => statusCalls, pool: () => instance.spoof_ip_list };
}
test('connection card exposes real endpoint and tunnel addresses', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/tunnels/');
 await expect(page.locator('.conn-grid .v').filter({ hasText: '192.0.2.2' })).toBeVisible();
 await expect(page.locator('.conn-grid .v').filter({ hasText: '10.77.0.1/30' })).toBeVisible();
});
test('incorrect login preserves input and shows the actual error', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/login/');
 await page.getByLabel('نام کاربری').fill('bad-user'); await page.getByLabel('رمز عبور', { exact: true }).fill('bad-password');
 await page.getByRole('button', { name: 'ورود', exact: true }).click();
 await expect(page.locator('.banner[role=alert]')).toContainText('نام کاربری یا رمز عبور نادرست است');
 await expect(page.getByLabel('نام کاربری')).toHaveValue('bad-user');
});
test('tester restores an active server-side job after reload', async ({ page }) => {
 const state = await mock(page); await page.goto(prefix + '/dashboard/tester/');
 await expect(page.getByRole('button', { name: 'توقف آزمون' })).toBeVisible();
 expect(state.statusCalls()).toBeGreaterThan(0);
 await page.reload(); await expect(page.getByRole('button', { name: 'توقف آزمون' })).toBeVisible();
});
test('general settings cannot erase a separately applied named pool', async ({ page }) => {
 const state = await mock(page); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.getByRole('checkbox', { name: /پیش‌فرض/ }).uncheck();
 await page.getByRole('checkbox', { name: /فارس.txt/ }).check();
 await page.getByRole('radio', { name: /انتخاب بر اساس کیفیت/ }).check();
 await page.getByRole('button', { name: 'اعمال انتخاب و روش ارسال' }).click();
 await expect(page.getByRole('status')).toContainText('مبدأ یکتا ذخیره شد');
 await page.getByRole('tab', { name: 'تنظیمات اتصال', exact: true }).click();
 await page.getByRole('button', { name: 'ذخیرهٔ تنظیمات اتصال' }).click();
 await expect.poll(() => state.writes.some(w => w.path === '/instances/1')).toBeTruthy();
 expect(state.writes.find(w => w.path === '/instances/1')?.data).not.toHaveProperty('spoof_ip_list');
 expect(state.pool()).toBe('198.51.100.22');
});

test('mobile navigation closes by its trigger, close button and Escape', async ({ page }) => {
 await mock(page); await page.setViewportSize({ width: 390, height: 844 }); await page.goto(prefix + '/dashboard/');
 const trigger = page.getByRole('button', { name: 'باز و بستن منو' });
 await trigger.click(); await expect(trigger).toHaveAttribute('aria-expanded', 'true');
 await trigger.click(); await expect(trigger).toHaveAttribute('aria-expanded', 'false');
 await trigger.click(); await page.getByRole('button', { name: 'بستن منو ×', exact: true }).click();
 await expect(trigger).toHaveAttribute('aria-expanded', 'false');
 await trigger.click(); await page.keyboard.press('Escape'); await expect(trigger).toHaveAttribute('aria-expanded', 'false');
});
test('light theme hydrates without runtime errors and toggles', async ({ page }) => {
 const errors: string[] = []; page.on('pageerror', error => errors.push(error.message));
 await mock(page); await page.goto(prefix + '/dashboard/');
 await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
 await page.getByRole('button', { name: 'تغییر به حالت تیره' }).click();
 await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
 await page.reload(); await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
 expect(errors).toEqual([]);
});
test('total traffic sums every tunnel', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/');
 const traffic = page.locator('section').filter({ has: page.getByRole('heading', { name: 'ترافیک کل', exact: true }) });
 await expect(traffic).toContainText('9.0 KB');
});

test('delete dialog supports Escape without deleting the tunnel', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/tunnels/');
 await page.getByRole('button', { name: 'حذف Review tunnel' }).click();
 await expect(page.getByRole('dialog', { name: 'حذف اتصال' })).toBeVisible();
 await page.keyboard.press('Escape');
 await expect(page.getByRole('dialog', { name: 'حذف اتصال' })).toBeHidden();
 await expect(page.getByText('Review tunnel', { exact: true })).toBeVisible();
});

test('slow polling never replaces a newer response after manual refresh', async ({ page }) => {
 await mock(page);
 let calls = 0; let releaseFirst: (() => void) | undefined;
 await page.route('**/api/dashboard', async route => {
  const first = ++calls === 1;
  if (first) await new Promise<void>(resolve => { releaseFirst = resolve; });
  await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ total: 1, running_count: 0, stopped_count: 1, error_count: 0, instances: [{ id: 1, name: first ? 'Stale state' : 'Latest valid state', mode: 'local', engine: 'tun', status: 'stopped', uptime: 0 }] }) });
 });
 await page.goto(prefix + '/dashboard/');
 await expect.poll(() => calls).toBe(1);
 await page.waitForTimeout(3800); expect(calls).toBe(1);
 await page.getByRole('button', { name: 'به‌روزرسانی', exact: true }).click();
 await expect(page.getByRole('heading', { name: 'Latest valid state' })).toBeVisible();
 releaseFirst?.(); await page.waitForTimeout(200);
 await expect(page.getByRole('heading', { name: 'Latest valid state' })).toBeVisible();
 await expect(page.getByRole('heading', { name: 'Stale state' })).toBeHidden();
});
test('password validation counts UTF-8 bytes', async ({ page }) => {
 const state = await mock(page); await page.goto(prefix + '/dashboard/settings/');
 await page.locator('input[autocomplete=current-password]').fill('old-password');
 const password = 'س'.repeat(40);
 await page.locator('input[autocomplete=new-password]').nth(0).fill(password);
 await page.locator('input[autocomplete=new-password]').nth(1).fill(password);
 await page.getByRole('button', { name: 'ثبت و به‌روزرسانی رمز عبور' }).click();
 await expect(page.locator('.banner[role=alert]')).toContainText('حداکثر 72 بایت');
 expect(state.writes.filter(write => write.path === '/settings/password')).toEqual([]);
});

test('subnet suggestion avoids a wider imported network', async ({ page }) => {
 await mock(page);
 await page.route('**/api/instances', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify([{ tun_address: '10.77.0.1/24' }]) }));
 await page.goto(prefix + '/dashboard/pair/');
 await expect(page.getByLabel('شبکهٔ داخلی TUN (/30)', { exact: false })).toHaveValue('10.77.1.0/30');
});

test('forward list ignores an older polling response', async ({ page }) => {
 await mock(page); let calls = 0; let releaseFirst: (() => void) | undefined;
 await page.route('**/api/instances/1/forwards', async route => {
  const first = ++calls === 1;
  if (first) await new Promise<void>(resolve => { releaseFirst = resolve; });
  const rule = { rule: { id: 1, name: 'Latest forward', protocol: 'tcp', listen_port: 15443, target_port: 443, enabled: true }, target: '10.77.0.2:443', listening: false, stats: { connections: 0, bytes_up: 0, bytes_down: 0, last_error: '' } };
  await route.fulfill({ contentType: 'application/json', body: JSON.stringify(first ? [] : [rule]) });
 });
 await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'فوروارد معکوس (ورودی کلاینت)' }).click();
 await expect.poll(() => calls).toBe(1);
 await page.waitForTimeout(3200); expect(calls).toBe(1);
 await page.getByRole('button', { name: 'به‌روزرسانی', exact: true }).click();
 await expect(page.getByRole('heading', { name: /Latest forward/ })).toBeVisible();
 releaseFirst?.(); await page.waitForTimeout(200);
 await expect(page.getByRole('heading', { name: /Latest forward/ })).toBeVisible();
});

test('Persian Vazirmatn loads locally and is inherited by form controls', async ({ page }) => {
 const externalFonts: string[] = [];
 await page.route(/^https:\/\/fonts\.(googleapis|gstatic)\.com\//, route => { externalFonts.push(route.request().url()); return route.abort(); });
 await page.goto(prefix + '/login/');
 const fonts = await page.evaluate(async () => {
  await document.fonts.load('16px Vazirmatn', 'سلام دنیا');
  await document.fonts.ready;
  return {
   rtl: document.documentElement.dir,
   loaded: Array.from(document.fonts).some(face => face.family.replaceAll('"', '') === 'Vazirmatn' && face.status === 'loaded'),
   elements: ['body', 'h1', 'label', 'input', 'button'].map(selector => getComputedStyle(document.querySelector(selector)!).fontFamily),
  };
 });
 expect(fonts.rtl).toBe('rtl');
 expect(fonts.loaded).toBe(true);
 for (const family of fonts.elements) expect(family).toContain('Vazirmatn');
 expect(externalFonts).toEqual([]);
});

test('dashboard cycles peer health by ID and retains selection through refresh and reorder', async ({ page }) => {
 await mock(page);
 let ids = [1, 2, 3];
 await page.route('**/api/dashboard', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ total: ids.length, running_count: ids.length, stopped_count: 0, error_count: 0, instances: ids.map(id => ({ id, name: 'Peer ' + id, mode: 'local', engine: 'tun', status: 'running', uptime: 1, telemetry: { peer_connected: true, rtt_ms: id * 10, tx_packets: id * 100, rx_packets: 0, drops: id } })) }) }));
 await page.goto(prefix + '/dashboard/');
 const card = page.getByRole('region', { name: 'Peer 1' });
 await expect(card).toContainText('۱۰ ms');
 await page.getByRole('button', { name: 'اتصال بعدی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'Peer 2' })).toContainText('۲۰ ms');
 ids = [3, 2, 1];
 await page.getByRole('button', { name: 'به‌روزرسانی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'Peer 2' })).toContainText('اتصال ۲ از ۳');
 await page.getByRole('button', { name: 'اتصال بعدی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'Peer 1' })).toBeVisible();
 await page.getByRole('button', { name: 'اتصال بعدی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'Peer 3' })).toBeVisible();
 ids = [1];
 await page.getByRole('button', { name: 'به‌روزرسانی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'Peer 1' })).toBeVisible();
 await expect(page.getByRole('button', { name: 'اتصال بعدی', exact: true })).toBeDisabled();
 await expect(page.getByRole('button', { name: 'اتصال قبلی', exact: true })).toBeDisabled();
 ids = [];
 await page.getByRole('button', { name: 'به‌روزرسانی', exact: true }).click();
 await expect(page.getByRole('region', { name: 'وضعیت پیوند شبکه' })).toBeVisible();
 await expect(page.getByRole('button', { name: 'اتصال بعدی', exact: true })).toHaveCount(0);
});

test('sender completion reports real processed sources and packet counters', async ({ page }) => {
 await mock(page);
 await page.route('**/api/tester/status', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ status: 'done', mode: 'sender', progress: 100, total_ips: 6, processed_ips: 6, sent_packets: 60, failed_packets: 0, results: [] }) }));
 await page.goto(prefix + '/dashboard/tester/');
 const counters = page.getByLabel('شمارنده‌های واقعی فرستنده');
 await expect(counters).toContainText('۶ / ۶');
 await expect(counters).toContainText('۶۰');
 await expect(counters).toContainText('خطاهای ارسال: ۰');
});

test('receiver exposes a test code and sender submits that shared code', async ({ page }) => {
 await mock(page);
 const code='0123456789abcdef0123456789abcdef';
 await page.route('**/api/tester/status',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({status:'idle',mode:'receiver',test_id:code,progress:0,total_ips:0,results:[]})}));
 let submitted: Record<string,unknown>|undefined;
 await page.route('**/api/tester/start',route=>{submitted=route.request().postDataJSON();return route.fulfill({contentType:'application/json',body:'{"status":"started"}'});});
 await page.goto(prefix+'/dashboard/tester/');
 await expect(page.getByText(code,{exact:true})).toBeVisible();
 await page.getByRole('tab',{name:'فرستندهٔ آزمون (Sender)'}).click();
 await page.getByLabel('کد مشترک آزمون',{exact:true}).fill(code);
 await page.getByLabel('IP مقصد (سرور گیرنده)',{exact:true}).fill('192.0.2.2');
 await page.locator('textarea').fill('198.51.100.1');
 await page.getByRole('button',{name:'شروع آزمون فرستنده',exact:true}).click();
 await expect.poll(()=>submitted?.test_id).toBe(code);
});

test('test-code copy remains usable when HTTP clipboard access is unavailable', async ({ page }) => {
 await mock(page);
 await page.addInitScript(()=>Object.defineProperty(navigator,'clipboard',{value:undefined,configurable:true}));
 await page.route('**/api/tester/status',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({status:'running',mode:'receiver',test_id:'0123456789abcdef0123456789abcdef',progress:1,total_ips:1,results:[]})}));
 await page.goto(prefix+'/dashboard/tester/');
 await page.getByRole('button',{name:'کپی کد آزمون',exact:true}).click();
 await expect(page.getByText('کد را دستی کپی کن.',{exact:true})).toBeVisible();
});


test('receiver explains rejected probes even when no source passed', async ({ page }) => {
 await mock(page);
 await page.route('**/api/tester/status',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({status:'done',mode:'receiver',progress:100,total_ips:6,recv_count:0,rejected_probes:12,duplicate_probes:9,results:[]})}));
 await page.goto(prefix+'/dashboard/tester/');
 const counters=page.getByLabel('شمارنده‌های اعتبارسنجی گیرنده');
 await expect(counters).toContainText('پروب‌های ردشده: ۱۲');
 await expect(counters).toContainText('بسته‌های تکراری: ۹');
 await expect(counters).toContainText('ترافیک نامرتبط یا خراب');
});

test('recovery budget and exhaustion are visible on the dashboard', async ({ page }) => {
 await mock(page);
 let exhausted=false;
 await page.route('**/api/dashboard',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({total:1,running_count:0,stopped_count:0,error_count:1,instances:[{id:1,name:'Recovery peer',mode:'local',engine:'tun',status:'error',uptime:0,recovery:{active:!exhausted,attempts:exhausted?8:2,max_attempts:8,next_retry_at:0,exhausted},telemetry:{peer_connected:false,tx_bytes:0,rx_bytes:0,drops:0,last_peer:0}}]})}));
 await page.goto(prefix+'/dashboard/');
 const card=page.getByRole('region',{name:'Recovery peer'});
 await expect(card).toContainText('بازیابی خودکار فعال است؛ ۲ از ۸');
 exhausted=true;
 await page.getByRole('button',{name:'به‌روزرسانی',exact:true}).click();
 await expect(card).toContainText('بازیابی پس از ۸ تلاش متوقف شد');
 await expect(card).toContainText('دوباره راه‌اندازی کن');
});


for (const scenario of [
 {name:'desktop dark local',width:1132,theme:'dark',mode:'local',send:'icmp',receive:'udp',sendLabel:'ICMP',receiveLabel:'UDP',role:'ایران',title:'تونل ماهان - ترکیه · local',iface:'sp105607'},
 {name:'mobile light remote',width:320,theme:'light',mode:'remote',send:'experimental',receive:'ipip',sendLabel:'IP 253',receiveLabel:'IPIP',role:'خارج',title:'تونل ماهان - ترکیه · remote با نام طولانی برای بررسی نمایش صحیح',iface:'sp1234567890123'},
]) {
 test(`connection metadata stays readable without overlap: ${scenario.name}`,async({page},testInfo)=>{
  await mock(page);
  await page.setViewportSize({width:scenario.width,height:900});
  await page.addInitScript(theme=>localStorage.setItem('spoof-theme',theme),scenario.theme);
  const instance={id:1,name:scenario.title,mode:scenario.mode,engine:'tun',status:'running',uptime:16620,send_transport:scenario.send,recv_transport:scenario.receive,tun_interface:scenario.iface,tun_address:'10.77.0.1/30',tun_peer:'10.77.0.2',remote_addr:'192.0.2.2',client_ip:'192.0.2.1',spoof_ip:'198.51.100.1',remote_port:8090,recv_port:5001,listen_port:8090,client_port:5001,enabled:true};
  await page.route('**/api/instances',route=>route.fulfill({contentType:'application/json',body:JSON.stringify([instance])}));
  await page.goto(prefix+'/dashboard/tunnels/');
  const card=page.locator('.conn-card');
  await expect(card.getByRole('link',{name:scenario.title,exact:true})).toBeVisible();
  const metadata=card.getByLabel('مشخصات اتصال');
  for(const [label,value] of [['سمت',scenario.role],['ارسال',scenario.sendLabel],['دریافت',scenario.receiveLabel],['رابط',scenario.iface]]){
   const item=metadata.locator('.conn-meta-item').filter({has:page.locator('dt',{hasText:new RegExp('^'+label+'$')})});
   await expect(item.locator('dd')).toHaveText(value);
  }
  await page.evaluate(()=>document.fonts.ready);
  const geometry=await card.evaluate(card=>{
   const overlap=(a:DOMRect,b:DOMRect)=>Math.min(a.right,b.right)-Math.max(a.left,b.left)>1&&Math.min(a.bottom,b.bottom)-Math.max(a.top,b.top)>1;
   const items=Array.from(card.querySelectorAll('.conn-meta-item'));
   const boxes=items.map(item=>item.getBoundingClientRect());
   return {
    overlap:boxes.some((a,i)=>boxes.some((b,j)=>i!==j&&overlap(a,b))),
    textOverlap:items.some(item=>overlap(item.querySelector('dt')!.getBoundingClientRect(),item.querySelector('dd')!.getBoundingClientRect())),
    headerOverlap:overlap(card.querySelector('.conn-title')!.getBoundingClientRect(),card.querySelector('.conn-head > .pill')!.getBoundingClientRect()),
    overflow:document.documentElement.scrollWidth>innerWidth,
    directions:Array.from(card.querySelectorAll('.conn-meta-item bdi')).map(value=>getComputedStyle(value).direction),
   };
  });
  expect(geometry).toEqual({overlap:false,textOverlap:false,headerOverlap:false,overflow:false,directions:['ltr','ltr','ltr']});
  await card.screenshot({path:testInfo.outputPath('connection-card.png')});
 });
}

test('smart pool shows bounded quality rows and locks active selection', async ({ page }) => {
 await mock(page, true); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await expect(page.getByRole('heading', { name: 'استخر مبدأهای اسپوف', exact: true })).toBeVisible();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).toBeDisabled();
 await expect(page.locator('.pool-table tbody tr')).toHaveCount(2);
 await expect(page.locator('.pool-table')).toContainText('45.157.244.1');
 await expect(page.locator('.pool-state.quarantined')).toContainText('قرنطینه');
 await expect(page.getByRole('button', { name: 'دریافت از طرف مقابل' })).toBeEnabled();
 await page.getByRole('button', { name: 'دریافت از طرف مقابل' }).click();
 await expect(page.getByRole('status')).toContainText('درخواست دریافت فهرست');
});
test('pool file import keeps unsaved selection and renders correctly on mobile', async ({ page }) => {
 await page.setViewportSize({ width: 390, height: 844 }); await mock(page);
 await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.getByRole('checkbox', { name: /فارس.txt/ }).check();
 await page.locator('.pool-file-input').setInputFiles({ name: 'جدید.txt', mimeType: 'text/plain', buffer: Buffer.from('192.0.2.1\n192.0.2.2\n192.0.2.3\n') });
 await expect(page.getByRole('checkbox', { name: /جدید.txt/ })).toBeVisible();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).toBeChecked();
 expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy();
});

test('manual IPs belong to the default group and TXT groups stay independent', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.getByText('ویرایش آی‌پی‌های دستیِ گروه پیش‌فرض', { exact: true }).click();
 const editor = page.getByRole('textbox', { name: 'آی‌پی‌های گروه پیش‌فرض', exact: true });
 await expect(editor).toHaveValue('198.51.100.1');
 await editor.fill('198.51.100.9\n198.51.100.10\n198.51.100.9');
 await page.getByRole('button', { name: 'ذخیرهٔ گروه پیش‌فرض', exact: true }).click();
 await expect(editor).toHaveValue('198.51.100.9\n198.51.100.10');
 await expect(page.getByRole('checkbox', { name: /پیش‌فرض/ })).toBeChecked();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).not.toBeChecked();
 expect(await page.locator('.pool-file-row').filter({ hasText: 'پیش‌فرض' }).getByRole('button', { name: 'حذف', exact: true }).count()).toBe(0);
});
test('pool drafts survive tab changes and applying pools retains other form drafts', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByLabel('نام اتصال', { exact: true }).fill('نام ذخیره‌نشده');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.getByRole('checkbox', { name: /فارس.txt/ }).check();
 await page.getByRole('tab', { name: 'تنظیمات اتصال', exact: true }).click();
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).toBeChecked();
 await page.getByRole('radio', { name: /انتخاب بر اساس کیفیت/ }).check();
 await page.getByRole('button', { name: 'اعمال انتخاب و روش ارسال' }).click();
 await expect(page.getByRole('status')).toContainText('مبدأ یکتا ذخیره شد');
 await page.getByRole('tab', { name: 'تنظیمات اتصال', exact: true }).click();
 await expect(page.getByLabel('نام اتصال', { exact: true })).toHaveValue('نام ذخیره‌نشده');
});

test('an external configuration revision blocks overwriting pool settings', async ({ page }) => {
 await mock(page);
 await page.route('**/api/instances/1/pools', route => route.fulfill({ contentType: 'application/json', body: JSON.stringify({ files: [{ id: 1, kind: 'default', name: 'پیش‌فرض', count: 1, enabled: true, position: 0, hash: 'one' }], mode: 'legacy', probe_pps: 25, auto_peer_sync: false, total: 1, peer_count: 1, legacy_content: false, updated_at: '2026-11-05T13:00:00Z' }) }));
 await page.goto(prefix + '/dashboard/tunnels/edit/?id=1'); await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await expect(page.locator('.pool-feedback[role=alert]')).toContainText('در جای دیگری تغییر کرده');
 await page.getByRole('radio', { name: /انتخاب بر اساس کیفیت/ }).check();
 await expect(page.getByRole('button', { name: 'اعمال انتخاب و روش ارسال' })).toBeDisabled();
});
test('deleting an inactive TXT group requires its named confirmation and supports Escape', async ({ page }) => {
 await mock(page); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1'); await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.locator('.pool-file-row').filter({ hasText: 'فارس.txt' }).getByRole('button', { name: 'حذف', exact: true }).click();
 await expect(page.getByRole('dialog', { name: 'حذف گروه TXT' })).toContainText('فارس.txt');
 await page.keyboard.press('Escape'); await expect(page.getByRole('dialog')).toBeHidden();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).toBeVisible();
 await page.locator('.pool-file-row').filter({ hasText: 'فارس.txt' }).getByRole('button', { name: 'حذف', exact: true }).click();
 await page.getByRole('button', { name: 'حذف گروه', exact: true }).click();
 await expect(page.getByRole('checkbox', { name: /فارس.txt/ })).toHaveCount(0);
 await expect(page.getByRole('checkbox', { name: /پیش‌فرض/ })).toBeVisible();
});

for (const scenario of [{ name:'desktop dark', width:1440, height:1000, theme:'dark' }, { name:'mobile light', width:390, height:844, theme:'light' }]) {
 test(`pool groups layout: ${scenario.name}`, async({page},testInfo)=>{
  await page.setViewportSize({width:scenario.width,height:scenario.height});
  await mock(page);
  await page.addInitScript(theme=>localStorage.setItem('spoof-theme',theme),scenario.theme);
  await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');
  await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();
  await expect(page.getByRole('heading',{name:'گروه‌های استخر این سرور'})).toBeVisible();
  await expect(page.getByRole('checkbox',{name:/پیش‌فرض/})).toBeChecked();
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:testInfo.outputPath('pool-groups.png'),fullPage:true});
 });
}

test('pool explains waiting for the other engine to finish readiness checks', async({page})=>{
 await mock(page,true);
 await page.route('**/api/instances/1/pools/state**',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({available:true,runtime:{peer_ready:false,control_fresh:true,our_sources_accepted:true,peer:{supported:true,status:'synced',count:1,progress:100},pool:{mode:'adaptive',total:1,healthy:1,testing:0,quarantined:0,unknown:0,offset:0,items:[]}}})}));
 await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();
 await expect(page.getByText('طرف مقابل هنوز آمادهٔ ارسال داده نیست؛ منتظر تکمیل تأیید مبدأ و همگام‌سازی آن سمت هستیم.')).toBeVisible();
});

test('retrying after a failed TXT upload keeps the pending group selection',async({page})=>{
 await mock(page);
 await page.route('**/api/instances/1/pools/upload',route=>route.fulfill({status:409,contentType:'application/json',body:JSON.stringify({error:'this address file is already imported'})}));
 await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();
 await page.getByRole('checkbox',{name:/فارس.txt/}).check();
 await page.locator('.pool-file-input').setInputFiles({name:'duplicate.txt',mimeType:'text/plain',buffer:Buffer.from('192.0.2.1')});
 await page.getByRole('button',{name:'دریافت دوباره',exact:true}).click();
 await expect(page.getByRole('checkbox',{name:/فارس.txt/})).toBeChecked();
 await expect(page.getByText('تغییرات هنوز اعمال نشده‌اند.')).toBeVisible();
});

for(const save of ['ذخیرهٔ گروه پیش‌فرض','اعمال انتخاب و روش ارسال']){
 test(`clear default and activate pending TXT atomically using ${save}`,async({page})=>{
  await mock(page);await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();
  await page.getByText('ویرایش آی‌پی‌های دستیِ گروه پیش‌فرض',{exact:true}).click();
  const editor=page.getByRole('textbox',{name:'آی‌پی‌های گروه پیش‌فرض',exact:true});await expect(editor).toHaveValue('198.51.100.1');await editor.fill('');
  await expect(page.getByRole('button',{name:save,exact:true})).toBeDisabled();
  await page.getByRole('checkbox',{name:/فارس.txt/}).check();
  await page.getByRole('button',{name:save,exact:true}).click();
  await expect(editor).toHaveValue('');await expect(page.getByRole('checkbox',{name:/فارس.txt/})).toBeChecked();await expect(page.getByRole('checkbox',{name:/پیش‌فرض/})).not.toBeChecked();
  await expect(page.locator('.pool-feedback.error')).toHaveCount(0);
 });
}

const updateReady={current:'v0.4.1',latest:'v0.4.2',release_url:'https://github.com/xemadx/spoof-tunnel/releases/tag/v0.4.2',supported:true,available:true};
test('panel updater confirms a version, survives disconnect and exposes rollback without a second install',async({page})=>{
 await mock(page);let installs=0,reads=0;
 await page.route('**/api/updates**',route=>{
  const path=new URL(route.request().url()).pathname;let data:unknown=updateReady;let status=200;
  if(path.endsWith('/install')){installs++;data={...updateReady,job:{id:'update-one',from:'v0.4.1',target:'v0.4.2',phase:'queued',progress:0}};}
  else if(installs && !path.endsWith('/check')){reads++;if(reads===1){status=503;data={error:'restarting'};}else data={...updateReady,job:{id:'update-one',from:'v0.4.1',target:'v0.4.2',phase:'rolled_back',progress:90,error:'injected_startup_failure'}};}
  return route.fulfill({status,contentType:'application/json',body:JSON.stringify(data)});
 });
 await page.goto(prefix+'/dashboard/settings/');
 await page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'}).click();
 await expect(page.getByRole('dialog',{name:'نصب به‌روزرسانی پنل'})).toBeVisible();await page.keyboard.press('Escape');expect(installs).toBe(0);
 await page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'}).click();await page.getByRole('button',{name:'شروع نصب v0.4.2'}).click();
 await expect(page.getByText(/ارتباط پنل موقتاً قطع است/)).toBeVisible();
 await expect(page.getByText('آپدیت کامل نشد؛ نسخهٔ قبلی بازیابی شد',{exact:true})).toBeVisible({timeout:10000});
 expect(installs).toBe(1);await expect(page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'})).toBeEnabled();
});
test('successful update reloads the page once and preserves the signed-in session',async({page})=>{
 await mock(page);let installed=false,navigations=0;
 await page.route('**/api/system',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({panel_version:installed?'v0.4.2':'v0.4.1',hostname:'test',cpus:2,memory_mb:20})}));
 page.on('request',request=>{if(request.resourceType()==='document'&&new URL(request.url()).pathname.endsWith('/dashboard/settings/'))navigations++;});
 await page.route('**/api/updates**',route=>{
  const path=new URL(route.request().url()).pathname;
  const job={id:'update-success',from:'v0.4.1',target:'v0.4.2',phase:'succeeded',progress:100};
  let data:unknown=installed?{...updateReady,current:'v0.4.2',available:false,job}:updateReady;
  if(path.endsWith('/install')){installed=true;data={...updateReady,job:{...job,phase:'queued',progress:0}};}
  return route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
 });
 await page.goto(prefix+'/dashboard/settings/');await page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'}).click();await page.getByRole('button',{name:'شروع نصب v0.4.2'}).click();
 await expect.poll(()=>navigations).toBe(2);await expect(page.getByText('به‌روزرسانی با موفقیت انجام شد',{exact:true})).toBeVisible();await expect(page.locator('.brand-copy .mono')).toHaveText('v0.4.2');
 expect(await page.evaluate(()=>sessionStorage.getItem('spoof-panel-pending-update'))).toBeNull();
 expect(await page.evaluate(()=>localStorage.getItem('token'))).toBe('review');
 await page.waitForTimeout(2200);expect(navigations).toBe(2);
});
test('GitHub check errors are recoverable and never start an installation',async({page})=>{
 await mock(page);let checks=0;
 await page.route('**/api/updates**',route=>{
  const check=new URL(route.request().url()).pathname.endsWith('/check');if(check)checks++;
  return route.fulfill({status:check?502:200,contentType:'application/json',body:JSON.stringify(check?{error:'github_http_403'}:{current:'v0.4.1',supported:true,available:false})});
 });
 await page.goto(prefix+'/dashboard/settings/');await page.getByRole('button',{name:'بررسی نسخهٔ جدید'}).click();
 await expect(page.getByText('گیت‌هاب فعلاً درخواست را محدود کرده است؛ کمی بعد دوباره بررسی کن.')).toBeVisible();
 await expect(page.getByRole('button',{name:'بررسی نسخهٔ جدید'})).toBeEnabled();expect(checks).toBe(1);
});
test('missing update service gives an installation instruction and blocks install',async({page})=>{
 await mock(page);await page.route('**/api/updates**',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({...updateReady,supported:false,reason:'missing_update_service'})}));
 await page.goto(prefix+'/dashboard/settings/');await expect(page.getByText('سرویس آپدیت نصب نیست؛ یک بار نصب‌کنندهٔ جدید را روی این سرور اجرا کن.')).toBeVisible();await expect(page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'})).toBeDisabled();
});
test('opening settings during an existing update restores its progress without starting another job',async({page},testInfo)=>{
 await mock(page);let posts=0;await page.route('**/api/updates**',route=>{if(route.request().method()==='POST')posts++;return route.fulfill({contentType:'application/json',body:JSON.stringify({...updateReady,job:{id:'existing-update',from:'v0.4.1',target:'v0.4.2',phase:'installing',progress:75}})});});
 await page.goto(prefix+'/dashboard/settings/');await expect(page.getByText('نصب پنل و موتور',{exact:true})).toBeVisible();await expect(page.getByRole('progressbar',{name:'پیشرفت به‌روزرسانی'})).toHaveAttribute('value','75');await expect(page.getByText('۷۵٪',{exact:true})).toBeVisible();await expect(page.getByRole('button',{name:'بررسی نسخهٔ جدید'})).toBeDisabled();await expect(page.getByLabel('رمز عبور فعلی',{exact:true})).toBeDisabled();expect(posts).toBe(0);await page.locator('.panel-update').screenshot({path:testInfo.outputPath('update-progress.png')});
});
for(const mode of [{name:'desktop dark',width:1280,height:900,theme:'dark'},{name:'mobile light',width:390,height:844,theme:'light'}]){
 test(`panel update layout ${mode.name}`,async({page},testInfo)=>{
  await page.setViewportSize({width:mode.width,height:mode.height});await mock(page);await page.addInitScript(theme=>localStorage.setItem('spoof-theme',theme),mode.theme);
  await page.route('**/api/updates**',route=>route.fulfill({contentType:'application/json',body:JSON.stringify(updateReady)}));await page.goto(prefix+'/dashboard/settings/');
  await expect(page.getByRole('button',{name:'نصب نسخهٔ v0.4.2'})).toBeVisible();expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);await page.locator('.panel-update').screenshot({path:testInfo.outputPath('panel-update.png')});
 });
}

test('pool logs render readable reasons and retain malformed raw diagnostics',async({page})=>{
 await mock(page,true);
 await page.routeWebSocket('**/api/instances/1/logs',socket=>{
  socket.send('2026/11/06 01:00:00 '+JSON.stringify({event:'spoof_pool',mode:'adaptive',total:512,healthy:2,testing:3,unknown:506,quarantined:1,active:'198.51.100.3',peer_status:'synced',peer_count:40,peer_progress:100,reason:'reverse_reference_stale'}));
  socket.send('2026/11/06 01:00:01 {malformed diagnostic');
 });
 await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'لاگ زندهٔ تونل'}).click();
 await expect(page.locator('.log-viewer')).toContainText('مرجع رفت‌وبرگشت تازه نیست');await expect(page.locator('.log-viewer')).toContainText('مبدأ فعال 198.51.100.3');await expect(page.locator('.log-viewer')).toContainText('{malformed diagnostic');
});
test('live log reconnect replaces history and burst rendering stays bounded',async({page})=>{
 await mock(page,true);let connections=0;
 await page.routeWebSocket('**/api/instances/1/logs',socket=>{
  connections++;
  if(connections===1){socket.send('history-once');setTimeout(()=>socket.close(),250);}
  else {for(let i=0;i<1000;i++)socket.send('line-'+i);}
 });
 await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'لاگ زندهٔ تونل'}).click();
 await expect(page.locator('.log-viewer')).toContainText('history-once');
 await expect(page.locator('.log-viewer')).toContainText('line-999',{timeout:10000});await expect(page.locator('.log-viewer')).not.toContainText('history-once');
 expect(await page.locator('.log-viewer>div[dir=auto]').count()).toBeLessThanOrEqual(800);
 await page.getByRole('button',{name:'پاک کردن لاگ'}).click();await expect(page.locator('.log-viewer')).not.toContainText('line-999');
});
test('source quality explains large-packet timeouts separately from an unknown reverse path',async({page})=>{
 await mock(page,true);await page.route('**/api/instances/1/pools/state**',route=>route.fulfill({contentType:'application/json',body:JSON.stringify({available:true,runtime:{peer_ready:true,control_fresh:false,our_sources_accepted:true,peer:{supported:true,status:'configured',count:1},pool:{mode:'adaptive',total:2,healthy:0,testing:0,unknown:1,quarantined:1,offset:0,items:[{ip:'192.0.2.1',state:'quarantined',reason:'large_probe_timeout',rtt_ms:0,loss_pct:60,probes:4,replies:1,failures:3,retry_at:Math.floor(Date.now()/1000)+600},{ip:'192.0.2.2',state:'unknown',reason:'reverse_unconfirmed',rtt_ms:0,loss_pct:0,probes:1,replies:0,failures:0}]}}})}));
 await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();await expect(page.getByText('عدم پاسخ به آزمون بستهٔ بزرگ',{exact:true})).toBeVisible();await expect(page.getByText('مسیر برگشت هنوز تأیید نشده',{exact:true})).toBeVisible();await expect(page.getByText('فهرست ذخیره‌شده در تنظیمات',{exact:true})).toBeVisible();
});

test('bounded source set validates its budget and saves with the dispatch mode',async({page})=>{
 const {writes}=await mock(page);await page.goto(prefix+'/dashboard/tunnels/edit/?id=1');await page.getByRole('tab',{name:'استخر IPهای اسپوف'}).click();await page.getByRole('checkbox',{name:'استفاده از مجموعهٔ منتخب'}).check();await expect(page.getByRole('radio',{name:/انتخاب بر اساس کیفیت/})).toBeChecked();const count=page.getByLabel('سقف مبدأهای در حال استفاده',{exact:true});await count.fill('101');await expect(page.getByRole('button',{name:'اعمال انتخاب و روش ارسال'})).toBeDisabled();await count.fill('100');await page.getByLabel('بازسازی مجموعه هر چند دقیقه',{exact:true}).fill('30');await page.getByRole('button',{name:'اعمال انتخاب و روش ارسال'}).click();expect(writes.find(w=>w.path.endsWith('/pools/settings'))?.data).toMatchObject({active_size:100,refresh_seconds:1800,mode:'adaptive',probe_pps:25});
});
