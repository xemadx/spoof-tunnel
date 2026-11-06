import {test,expect,type Page} from '@playwright/test';
import type {BenchState,BenchResult} from '../lib/api';
const root='/reviewprefix/dashboard/connection-test/';
const carriers=['icmp','udp','tcp','ipip','experimental'];
const direction={sent:1000,received:999,send_errors:0,seconds:3,mbps:19.9,loss_pct:.1,capped:true};
const rows:BenchResult[]=Array.from({length:25},(_,index)=>({index,send:carriers[Math.floor(index/5)],receive:carriers[index%5],status:index===4?'failed':'passed',error:index===4?'no authenticated response on this carrier pair':undefined,rtt_ms:39,p95_ms:48,ping_sent:12,ping_replies:12,upload:direction,download:{...direction,mbps:18.2,capped:false},score:10}));
const complete:BenchState={role:'local',status:'done',phase:'',current:24,progress:100,expires:Math.floor(Date.now()/1000)+900,options:{rate_mbps:20,seconds:3},results:rows,recommended:7,applied:false};
async function mock(page:Page,initial:BenchState={...complete,status:'idle',role:'',results:[],recommended:-1}){
 let state=structuredClone(initial);const writes:{path:string;body:Record<string,unknown>}[]=[];
 await page.addInitScript(()=>{localStorage.setItem('token','review');localStorage.setItem('spoof-theme','light')});
 await page.route('**/api/**',async route=>{const req=route.request(),path=new URL(req.url()).pathname.split('/api')[1];let data:unknown={};if(req.method()==='POST'){writes.push({path,body:req.postDataJSON()})}
 if(path==='/system')data={panel_version:'v0.4.2',hostname:'test'};
 else if(path==='/instances')data=[];
 else if(path==='/connection-test')data=state;
 else if(path==='/connection-test/start'){state={...complete,status:'running',phase:'latency',current:0,progress:0,results:[],recommended:-1};data=state}
 else if(path==='/connection-test/receiver'){state={...complete,role:'remote',status:'waiting',current:-1,progress:0,results:[],recommended:-1};data={code:'ephemeral-test-code',state}}
 else if(path==='/connection-test/stop'){state={...state,status:'stopped'};data=state}
 else if(path==='/connection-test/preview')data={token:'approved-plan',index:req.postDataJSON().index,name:'آزمون ترکیبی',replace:true,target_id:1,remote_target_id:4,send:'udp',receive:'tcp',subnet:'10.77.0.1/30',expires:Math.floor(Date.now()/1000)+240};
 else if(path==='/connection-test/apply'){state={...state,status:'applying',phase:'prepare'};data=state}
 await route.fulfill({contentType:'application/json',body:JSON.stringify(data)})});return {writes,setState:(s:BenchState)=>{state=s}}
}
test('starting the benchmark does not create or stop a tunnel',async({page})=>{
 const {writes}=await mock(page);await page.goto(root);await page.getByLabel('کد موقت طرف مقابل').fill('temporary-code');await expect(page.getByText(/۳۷۵ MB/)).toBeVisible();await page.getByRole('button',{name:'شروع آزمون ۲۵ ترکیب'}).click();await expect(page.getByRole('status')).toContainText('سنجش تأخیر');await expect(page.getByLabel('کد موقت طرف مقابل')).toBeDisabled();expect(writes).toHaveLength(1);expect(writes[0].path).toBe('/connection-test/start');expect(writes[0].body.options).toEqual({rate_mbps:20,seconds:3});await page.getByRole('button',{name:'توقف آزمون'}).click();await page.getByRole('button',{name:'طرف مقابل · معمولاً خارج'}).click();await expect(page.getByLabel('آی‌پی واقعی همین سرور')).toBeVisible();
});
test('receiver creates a scoped code with explicit apply capability',async({page})=>{
 const {writes}=await mock(page);await page.goto(root);await page.getByRole('button',{name:'طرف مقابل · معمولاً خارج'}).click();for(const [label,value] of [['آی‌پی واقعی همین سرور','192.0.2.2'],['آی‌پی واقعی سرور کنترل‌کننده','192.0.2.1'],['مبدأ جعلی برای ارسال همین سرور','198.51.100.2'],['مبدأ جعلی برای ارسال سرور کنترل‌کننده','198.51.100.1']])await page.getByLabel(label,{exact:true}).fill(value);await page.getByRole('checkbox',{name:/اجازهٔ ساخت یا تغییر/}).check();await page.getByRole('button',{name:'ساخت کد موقت و انتظار'}).click();await expect(page.getByLabel('کد آزمون اتصال')).toHaveValue('ephemeral-test-code');expect(writes[0].body).toMatchObject({allow_apply:true,port_a:39090,port_b:39091,source:'198.51.100.2',peer_source:'198.51.100.1'});
});
test('result preview requires a separate explicit approval',async({page})=>{
 const {writes}=await mock(page,complete);await page.goto(root);await expect(page.getByText('حداقل یک جهت به سقف آزمون رسیده؛',{exact:false})).toBeVisible();await page.getByRole('button',{name:'بررسی و تأیید پیشنهاد'}).click();await expect(page.getByRole('dialog')).toBeVisible();expect(writes.map(w=>w.path)).toEqual(['/connection-test/preview']);await page.getByRole('button',{name:'تأیید و راه‌اندازی دو سرور'}).click();await expect(page.getByRole('dialog')).toHaveCount(0);await expect(page.getByRole('heading',{name:'در حال اعمال نتیجه'})).toBeVisible();expect(writes.map(w=>w.path)).toEqual(['/connection-test/preview','/connection-test/apply']);expect(writes[1].body).toEqual({token:'approved-plan'});await expect(page.getByRole('button',{name:'پایان نشست'})).toHaveCount(0);
});
test('failed combination shows its cause and cannot be applied',async({page})=>{
 await mock(page,complete);await page.goto(root);await page.getByRole('button',{name:/ارسال ICMP، برگشت IP · ۲۵۳: ناموفق/}).click();await expect(page.locator('.ct-detail')).toContainText('پاسخ احرازشدهٔ دوطرفه دریافت نشد');await expect(page.locator('.ct-detail button')).toHaveCount(0);
});
test('zero successful pairs never offer a recommendation',async({page})=>{
 await mock(page,{...complete,recommended:-1,results:rows.map(r=>({...r,status:'failed'}))});await page.goto(root);await expect(page.getByText(/هیچ ترکیبی شرایط پیشنهاد را نداشت/)).toBeVisible();await expect(page.getByRole('button',{name:'بررسی و تأیید پیشنهاد'})).toHaveCount(0);
});
for(const scenario of [{width:1440,theme:'dark'},{width:390,theme:'light'}])test(`matrix remains contained at ${scenario.width}px ${scenario.theme}`,async({page},testInfo)=>{
 await mock(page,complete);await page.setViewportSize({width:scenario.width,height:1000});await page.addInitScript(theme=>localStorage.setItem('spoof-theme',theme),scenario.theme);const errors:string[]=[];page.on('pageerror',e=>errors.push(e.message));await page.goto(root);await expect(page.locator('.ct-matrix tbody button')).toHaveCount(25);await page.evaluate(()=>document.fonts.ready);expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);expect(await page.locator('.ct-results').evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth})).toBe(true);await page.evaluate(()=>scrollTo(0,0));await page.screenshot({path:testInfo.outputPath('connection-test-results.png'),fullPage:true});expect(errors).toEqual([]);
});
