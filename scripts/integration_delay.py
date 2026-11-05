import sys,subprocess,time,json,base64,os,pathlib
import tempfile
root=pathlib.Path(tempfile.mkdtemp(prefix='spoof-delay-')); names=[f'review-delay-a-{os.getpid()}',f'review-delay-b-{os.getpid()}'];procs=[];logs=[]
binary=os.environ.get('SPOOF_L3_BINARY',str(pathlib.Path(__file__).resolve().parents[1]/'dist/spoof-l3'))
def run(*a): return subprocess.run(a,check=True,capture_output=True,text=True)
try:
 for n in names:run('ip','netns','add',n)
 run('ip','link','add','rvda','type','veth','peer','name','rvdb')
 for i,(n,dev) in enumerate(zip(names,['rvda','rvdb'])):
  run('ip','link','set',dev,'netns',n)
  run('ip','-n',n,'link','set','lo','up');run('ip','-n',n,'address','add',f'192.0.2.{i+1}/30','dev',dev);run('ip','-n',n,'link','set',dev,'up');run('ip','-n',n,'route','add',f'198.51.100.{20 if i==0 else 10}/32','dev',dev)
  run('ip','netns','exec',n,'tc','qdisc','add','dev',dev,'root','netem','delay','1700ms')
 key=base64.b64encode(os.urandom(32)).decode()
 for i,n in enumerate(names):
  c=dict(role=['local','remote'][i],local_ip=f'192.0.2.{i+1}',peer_ip=f'192.0.2.{2-i}',spoof_source=f'198.51.100.{10 if i==0 else 20}',peer_spoof=f'198.51.100.{20 if i==0 else 10}',send='experimental',receive='experimental',send_port=8090,receive_port=5001,interface='revdelay',address=f'10.77.0.{i+1}/30',peer_address=f'10.77.0.{2-i}',mtu=1280,psk=key)
  if os.environ.get('SPOOF_SMART_POOL') == '1':
   c.update(pool_policy=dict(mode='adaptive',probe_pps=25,cooldown_seconds=600),auto_peer_sync=True,control_socket=str(root/f'delay-{i}.sock'))
  cfg=root/f'delay-{i}.json';cfg.write_text(json.dumps(c));log=open(root/f'delay-{i}.log','w+');logs.append(log)
  procs.append(subprocess.Popen(['ip','netns','exec',n,binary,'--config',str(cfg)],stdout=log,stderr=log))
 if os.environ.get('SPOOF_SMART_POOL') == '1':
  import socket
  deadline=time.monotonic()+40
  while time.monotonic()<deadline:
   ready=[]
   for i in range(2):
    try:
     with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as ipc:
      ipc.settimeout(1);ipc.connect(str(root/f'delay-{i}.sock'));ipc.sendall(b'{"op":"status"}\n');status=json.load(ipc.makefile())['status']
      ready.append(status['pool']['healthy']>0 and status['our_sources_accepted'] and status['peer_ready'])
    except (OSError,ValueError,KeyError):ready.append(False)
   if all(ready):break
   time.sleep(.1)
  else:raise AssertionError('delayed smart engines did not become ready')
 else:time.sleep(12)
 p=run('ip','netns','exec',names[0],'ping','-c','1','-W','5','10.77.0.2');print('INNER_PING',p.stdout)
 for i,log in enumerate(logs):
  log.flush();log.seek(0);lines=log.read().splitlines();stats=[json.loads(s[s.index('{'):]) for s in lines if 'spoof_stats' in s];assert any(s['peer_connected'] for s in stats), f'delayed peer was not authenticated: {lines}'
  print('PASS delayed heartbeat',i,'RTT > 3 seconds')
finally:
 for p in procs:p.terminate()
 for p in procs:
  try:p.wait(timeout=5)
  except subprocess.TimeoutExpired:p.kill();p.wait()
 for l in logs:l.close()
 for n in names:subprocess.run(['ip','netns','del',n],capture_output=True)

 import shutil
 shutil.rmtree(root)
