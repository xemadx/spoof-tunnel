#!/usr/bin/env python3
"""Exercise the built panel's actual sender API in a loopback-only namespace."""
import json,os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
binary=str(pathlib.Path(os.environ.get('SPOOF_PANEL_BINARY',root/'dist/spoof-panel')).resolve())
name='spoof-tester-api-'+str(os.getpid())
client=r"""
import json,sys,time,urllib.request,urllib.error
base=sys.argv[1];token=''
def api(path,data=None):
 headers={'Content-Type':'application/json'}
 if token:headers['Authorization']='Bearer '+token
 req=urllib.request.Request(base+'/api'+path,data=None if data is None else json.dumps(data).encode(),headers=headers)
 with urllib.request.urlopen(req,timeout=5) as r:return json.load(r)
for _ in range(60):
 try:api('/auth/check');break
 except OSError:time.sleep(.1)
else:raise AssertionError('server did not start')
token=api('/auth/login',{'username':'tester-api','password':'isolated-test-password'})['token']
for protocol in ['icmp','tcp']:
 for count in [6,1000]:
  sources='\n'.join('10.0.%d.%d'%(i//128,(i%128)*2) for i in range(count))
  result=api('/tester/start',{'mode':'sender','protocol':protocol,'dst_ip':'127.0.0.1','dst_port':19119,'timeout':10,'packet_count':10,'max_packet_loss':0,'ip_list':sources})
  assert result['ip_count']==count,result
  for _ in range(200):
   state=api('/tester/status')
   if state['status']!='running':break
   time.sleep(.01)
  assert state['status']=='done' and state['processed_ips']==count and state['sent_packets']==count*10 and state['failed_packets']==0 and state['progress']==100,state
  print('PASS real HTTP sender',protocol,count,'sources',state['sent_packets'],'packets',flush=True)
"""
with tempfile.TemporaryDirectory(prefix='spoof-tester-api-') as data:
 env={**os.environ,'SPOOF_DATA_DIR':data,'SPOOF_PUBLIC_IP':'127.0.0.1'}
 subprocess.run([binary,'--setup','--setup-user','tester-api','--setup-pass','isolated-test-password','--setup-port','18799'],env=env,check=True,stdout=subprocess.DEVNULL)
 info=pathlib.Path(data,'credentials.txt').read_text();prefix=next(l.split(': ',1)[1] for l in info.splitlines() if l.startswith('Web Path:'))
 subprocess.run(['ip','netns','add',name],check=True)
 process=None
 try:
  subprocess.run(['ip','-n',name,'link','set','lo','up'],check=True)
  process=subprocess.Popen(['ip','netns','exec',name,binary,'--bind','127.0.0.1'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
  subprocess.run(['ip','netns','exec',name,'python3','-c',client,'http://127.0.0.1:18799'+prefix],check=True,timeout=30)
 finally:
  if process is not None:process.terminate();process.wait(timeout=10)
  subprocess.run(['ip','netns','del',name],check=True)
