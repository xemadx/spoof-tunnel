#!/usr/bin/env python3
"""Root-only, isolated Linux namespaces. No changes to host routing/firewall."""
import base64,json,os,pathlib,subprocess,sys,tempfile,time

ROOT=pathlib.Path(__file__).resolve().parents[1]
BINARY=pathlib.Path(os.environ.get('SPOOF_L3_BINARY',os.environ.get('EMAD_L3_BINARY',str(ROOT/'dist/spoof-l3' if (ROOT/'dist/spoof-l3').is_file() else ROOT/'spoof-l3'))))
def run(*args,**kwargs):
    r=subprocess.run(args,text=True,capture_output=True,**kwargs)
    if r.returncode: raise RuntimeError(f"{args}: {r.stdout} {r.stderr}")
    return r
def main():
    if os.geteuid()!=0:raise SystemExit('run as root')
    names=[f'spoof-a-{os.getpid()}',f'spoof-b-{os.getpid()}'];processes=[]
    report=[]
    try:
        for n in names:run('ip','netns','add',n)
        run('ip','link','add','ema0','type','veth','peer','name','emb0')
        for name,dev,ip,spoof in zip(names,['ema0','emb0'],['192.0.2.1/30','192.0.2.2/30'],['198.51.100.20/32','198.51.100.10/32']):
            run('ip','link','set',dev,'netns',name)
            for args in [('link','set','lo','up'),('address','add',ip,'dev',dev),('link','set',dev,'up'),('route','add',spoof,'dev',dev)]:run('ip','-n',name,*args)
        run('ip','-n',names[0],'route','add','198.51.100.21/32','dev','ema0')
        run('ip','-n',names[1],'route','add','198.51.100.11/32','dev','emb0')
        with tempfile.TemporaryDirectory(prefix='spoof-l3-test-') as tmp:
            for send,recv,multi in ([(p,p,True) for p in ["icmp","udp","tcp","ipip","experimental"]]+[("icmp","tcp",True)] if os.environ.get("SPOOF_SMART_POOL") else [("udp","udp",False)] if os.environ.get("SPOOF_MTU_TEST") else [(a,b,False) for a in ['icmp','udp','tcp','ipip','experimental'] for b in ['icmp','udp','tcp','ipip','experimental']] + [(p,p,True) for p in ['icmp','udp','tcp','ipip','experimental']]):
                key=base64.b64encode(os.urandom(32)).decode();case=[];logs=[]
                try:
                    for i,role in enumerate(['local','remote']):
                        cfg=dict(role=role,local_ip=['192.0.2.1','192.0.2.2'][i],peer_ip=['192.0.2.2','192.0.2.1'][i],spoof_source=['198.51.100.10','198.51.100.20'][i],peer_spoof=['198.51.100.20','198.51.100.10'][i],send=[send,recv][i],receive=[recv,send][i],send_port=[8090,5001][i],receive_port=[5001,8090][i],interface='sptun0',address=['10.77.0.1/30','10.77.0.2/30'][i],peer_address=['10.77.0.2','10.77.0.1'][i],mtu=([1300,1000][i] if os.environ.get("SPOOF_MTU_TEST") else 1280),psk=key)
                        if multi:
                            cfg['spoof_sources']=[['198.51.100.10','198.51.100.11'],['198.51.100.20','198.51.100.21']][i]
                            cfg['peer_spoofs']=[['198.51.100.20','198.51.100.21'],['198.51.100.10','198.51.100.11']][i]
                        if os.environ.get('SPOOF_SMART_POOL'):
                            cfg['pool_policy']={'mode':'adaptive','probe_pps':100}
                            cfg['auto_peer_sync']=True
                            cfg['pool_state_file']=str(pathlib.Path(tmp)/f'{role}-state.json')
                            cfg['control_socket']=str(pathlib.Path(tmp)/f'{role}.sock')
                            cfg['peer_spoofs']=[]
                            cfg['peer_spoof']='198.51.100.250'
                        p=pathlib.Path(tmp)/f'{role}.json';p.write_text(json.dumps(cfg));os.chmod(p,0o600)
                        log=open(pathlib.Path(tmp)/f'{role}.log','w+');logs.append(log)
                        proc=subprocess.Popen(['ip','netns','exec',names[i],str(BINARY),'--config',str(p)],stdout=log,stderr=log);case.append(proc);processes.append(proc)
                    def wait_ready():
                        import socket
                        until=time.monotonic()+35
                        while time.monotonic()<until:
                            ready=[]
                            for role in ['local','remote']:
                                try:
                                    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as ipc:
                                        ipc.settimeout(1);ipc.connect(str(pathlib.Path(tmp)/f'{role}.sock'));ipc.sendall(b'{"op":"status"}\n')
                                        status=json.load(ipc.makefile())['status']
                                        ready.append(status['pool']['healthy']>0 and status['our_sources_accepted'] and status['peer_ready'])
                                except (OSError,ValueError,KeyError):ready.append(False)
                            if all(ready):return
                            assert all(p.poll() is None for p in case),'engine exited during readiness'
                            time.sleep(.1)
                        raise AssertionError('both smart engines did not become ready')
                    if os.environ.get('SPOOF_SMART_POOL'):
                        wait_ready()
                        telemetry_deadline=time.monotonic()+10
                        while time.monotonic()<telemetry_deadline:
                            observed=[]
                            for log in logs:
                                log.flush();log.seek(0);observed.append('"peer_connected":true' in log.read())
                            if all(observed):break
                            time.sleep(.1)
                        else:raise AssertionError('smart heartbeat telemetry did not confirm both peers')
                    else:time.sleep(4)
                    run('ip','netns','exec',names[0],'ping','-c','3','-W','2','10.77.0.2',timeout=12)
                    server='''import socket,threading,time
def serve(kind,port):
 s=socket.socket(socket.AF_INET,kind);s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('10.77.0.2',port));s.settimeout(12)
 if kind==socket.SOCK_STREAM:
  s.listen();c,_=s.accept()
  while True:
   b=c.recv(16384)
   if not b:break
   c.sendall(b)
 else:
  b,a=s.recvfrom(4096);s.sendto(b,a)
 s.close()
ts=[threading.Thread(target=serve,args=(socket.SOCK_STREAM,19001)),threading.Thread(target=serve,args=(socket.SOCK_DGRAM,19002))]
for t in ts:t.start()
for t in ts:t.join()
'''
                    srv=subprocess.Popen(['ip','netns','exec',names[1],'python3','-c',server]);processes.append(srv);time.sleep(.3)
                    client='''import socket
p=bytes(range(256))*512
s=socket.create_connection(('10.77.0.2',19001),timeout=8);s.sendall(p);s.shutdown(socket.SHUT_WR);b=b''
while True:
 c=s.recv(16384)
 if not c:break
 b+=c
assert b==p,('tcp mismatch',len(b));s.close()
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(8);s.sendto(b'x'*2000,('10.77.0.2',19002));assert s.recv(4096)==b'x'*2000;s.close()
'''
                    if os.environ.get('SPOOF_MTU_TEST'):
                        client="import socket,struct,time\nprobe=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);probe.setsockopt(socket.IPPROTO_IP,10,2);probe.setsockopt(socket.IPPROTO_IP,11,1);probe.setblocking(False)\nprobe.sendto(b'd'*1100,('10.77.0.2',19002))\nlimit=time.monotonic()+5\nwhile True:\n try:\n  _,anc,_,_=probe.recvmsg(4096,4096,socket.MSG_ERRQUEUE);break\n except BlockingIOError:\n  assert time.monotonic()<limit,'no ICMP MTU feedback';time.sleep(.01)\nerrors=[struct.unpack('=IBBBBII',data[:16]) for level,kind,data in anc if level==socket.IPPROTO_IP and kind==11]\nassert any(err[0]==90 and err[1]==2 and err[2]==3 and err[3]==4 and err[5]==1000 for err in errors),errors\nprobe.close()\n"+client
                    run('ip','netns','exec',names[0],'python3','-c',client,timeout=20);srv.wait(timeout=5)
                    if os.environ.get('SPOOF_SMART_POOL'):
                        case[0].terminate();case[0].wait(timeout=5)
                        proc=subprocess.Popen(['ip','netns','exec',names[0],str(BINARY),'--config',str(pathlib.Path(tmp)/'local.json')],stdout=logs[0],stderr=logs[0]);case[0]=proc;processes.append(proc)
                        wait_ready()
                        run('ip','netns','exec',names[0],'ping','-c','2','-W','2','10.77.0.2',timeout=10)
                    for log in logs:
                        log.flush();log.seek(0);text=log.read();assert '"peer_connected":true' in text,text
                    report.append(dict(send=send,receive=recv,multi_source=multi,icmp=True,tcp_bytes=131072,udp=True,authenticated_heartbeat=True));print(f'PASS {send}/{recv} multi_source={multi}',flush=True)
                except Exception:
                    for log in logs:
                        log.flush();log.seek(0);print(log.read(),flush=True)
                    raise
                finally:
                    for proc in case:
                        proc.terminate()
                    for proc in case:
                        try:proc.wait(timeout=5)
                        except subprocess.TimeoutExpired:proc.kill();proc.wait()
                    for log in logs:log.close()
    finally:
        for proc in processes:
            if proc.poll() is None:proc.kill();proc.wait()
        for n in names:subprocess.run(['ip','netns','delete',n],capture_output=True)
    print(json.dumps(report,indent=2))
if __name__=='__main__':main()
