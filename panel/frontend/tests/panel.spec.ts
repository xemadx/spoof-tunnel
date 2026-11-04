import { test, expect, type Page } from '@playwright/test';
const prefix = '/reviewprefix';
const runtimeErrors = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => { const errors: string[] = []; runtimeErrors.set(page, errors); page.on('pageerror', error => errors.push(error.message)); });
test.afterEach(({ page }) => { expect(runtimeErrors.get(page)).toEqual([]); });
async function mock(page: Page) {
 let instance = { id: 1, name: 'Review tunnel', mode: 'local', engine: 'tun', status: 'stopped', uptime: 30, send_transport: 'icmp', recv_transport: 'icmp', tun_interface: 'spcheck', tun_address: '10.77.0.1/30', tun_peer: '10.77.0.2', remote_addr: '192.0.2.2', spoof_ip: '198.51.100.1', peer_spoof_ip: '198.51.100.2', spoof_ip_list: '198.51.100.1', remote_port: 8090, recv_port: 5001, enabled: true };
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
   data = request.method() === 'PUT' ? instance : { instance, status: 'stopped', uptime: 0 };
  } else if (path === '/instances/1/spoof-ips') {
   if (request.method() === 'PUT') { instance.spoof_ip_list = request.postDataJSON().content; data = { ok: true, count: 1, instance }; }
   else data = { content: instance.spoof_ip_list, count: 1 };
  } else if (path.endsWith('/status') && path !== '/tester/status') data = { status: 'stopped', uptime: 0 };
  else if (path === '/tester/status') { statusCalls++; data = { status: 'running', mode: 'receiver', total_ips: 1, progress: 10, recv_count: 0, results: [] }; }
  else if (path === '/auth/login') { status = 401; data = { error: 'invalid credentials' }; }
  else if (path.endsWith('/forwards')) data = [];
  await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(data) });
 });
 return { writes, statusCalls: () => statusCalls };
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
test('saving general settings retains a newly saved spoof pool', async ({ page }) => {
 const state = await mock(page); await page.goto(prefix + '/dashboard/tunnels/edit/?id=1');
 await page.getByRole('tab', { name: 'استخر IPهای اسپوف' }).click();
 await page.locator('textarea').fill('198.51.100.22');
 await page.getByRole('button', { name: 'ذخیرهٔ استخر IP' }).click();
 await expect(page.getByRole('button', { name: 'ذخیره شد ✓' })).toBeVisible();
 await page.getByRole('tab', { name: 'تنظیمات اتصال', exact: true }).click();
 await page.getByRole('button', { name: 'ذخیرهٔ تنظیمات اتصال' }).click();
 await expect.poll(() => state.writes.find(w => w.path === '/instances/1')?.data.spoof_ip_list).toBe('198.51.100.22');
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
