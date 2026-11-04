#!/usr/bin/env python3
"""Three simultaneous foreign peers behind one Iran panel, in private namespaces."""
import concurrent.futures, json, os, pathlib, shutil, subprocess, tempfile, time
ROOT=pathlib.Path(__file__).resolve().parents[1]
CORE=str(pathlib.Path(os.environ.get('SPOOF_L3_BINARY', ROOT/'dist/spoof-l3')).resolve())
PANEL=str(pathlib.Path(os.environ.get('SPOOF_PANEL_BINARY', ROOT/'dist/spoof-panel')).resolve())
NAMES=[f'spm-{os.getpid()}-{n}' for n in ['iran','foreign1','foreign2','foreign3','switch']]
PROCESSES=[]
def run(*args,**options):
    result=subprocess.run(args,text=True,capture_output=True,**options)
    if result.returncode: raise RuntimeError(f'{args}: {result.stdout} {result.stderr}')
    return result
API_CODE='''import json,sys,urllib.request,urllib.error
base,token,method,path,payload=sys.argv[1:]
headers={'Content-Type':'application/json'}
if token:headers['Authorization']='Bearer '+token
req=urllib.request.Request(base+'/api'+path,data=None if payload=='null' else payload.encode(),headers=headers,method=method)
try:
 with urllib.request.urlopen(req,timeout=10) as r:print(json.dumps([r.status,json.loads(r.read())]))
except urllib.error.HTTPError as e:print(json.dumps([e.code,json.loads(e.read())]))
'''
SERVER_CODE='''import socket,socketserver,threading,sys
marker=sys.argv[1].encode()
class Handler(socketserver.BaseRequestHandler):
 def handle(self):
  line=self.request.makefile('rb').readline()
  self.request.sendall(marker+line)
class Server(socketserver.ThreadingTCPServer):allow_reuse_address=True;daemon_threads=True
def udp():
 s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',19002))
 while True:
  data,peer=s.recvfrom(4096);s.sendto(marker+data,peer)
threading.Thread(target=udp,daemon=True).start()
Server(('0.0.0.0',19001),Handler).serve_forever()
'''
CLIENT_CODE='''import socket,sys
peer,entry,marker=sys.argv[1:];marker=marker.encode();payload=b'isolated-service-probe\\n'
for host,port in [(peer,19001),('192.0.2.10',int(entry))]:
 s=socket.create_connection((host,port),timeout=5);s.sendall(payload);s.shutdown(socket.SHUT_WR);data=b''
 while True:
  part=s.recv(4096)
  if not part:break
  data+=part
 s.close();assert data==marker+payload,(host,port,data)
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(5);s.sendto(b'udp-probe',(peer,19002));assert s.recv(4096)==marker+b'udp-probe';s.close()
'''
def main():
    if os.geteuid()!=0:raise SystemExit('run as root')
    with tempfile.TemporaryDirectory(prefix='spoof-multipeer-') as temp:
        directory=pathlib.Path(temp); token=''; base=''
        def api(method,path,payload=None,expected=200):
            response=json.loads(run('ip','netns','exec',NAMES[0],'python3','-c',API_CODE,base,token,method,path,json.dumps(payload),timeout=15).stdout)
            assert response[0]==expected,(method,path,response)
            return response[1]
        def spawn(namespace,args):
            process=subprocess.Popen(['ip','netns','exec',namespace,*args],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            PROCESSES.append(process);return process
        try:
            for name in NAMES:run('ip','netns','add',name)
            run('ip','-n',NAMES[-1],'link','add','br0','type','bridge');run('ip','-n',NAMES[-1],'link','set','br0','up')
            for index,name in enumerate(NAMES[:-1]):
                host=f'sp{index}a';switch=f'sp{index}b'
                run('ip','link','add',host,'type','veth','peer','name',switch)
                run('ip','link','set',host,'netns',name);run('ip','link','set',switch,'netns',NAMES[-1])
                run('ip','-n',NAMES[-1],'link','set',switch,'master','br0');run('ip','-n',NAMES[-1],'link','set',switch,'up')
                run('ip','-n',name,'link','set','lo','up');run('ip','-n',name,'address','add',f'192.0.2.{10*(index+1)}/24','dev',host);run('ip','-n',name,'link','set',host,'up');run('ip','-n',name,'route','add','198.51.100.0/24','dev',host)
            data=directory/'panel';data.mkdir();shutil.copy2(CORE,data/'spoof-l3')
            env={**os.environ,'SPOOF_DATA_DIR':str(data),'SPOOF_PUBLIC_IP':'192.0.2.10'}
            subprocess.run([PANEL,'--setup','--setup-user','multipeer-test','--setup-pass','isolated-multipeer-password','--setup-port','18767'],env=env,check=True,stdout=subprocess.DEVNULL)
            prefix=next(line.split(': ',1)[1] for line in (data/'credentials.txt').read_text().splitlines() if line.startswith('Web Path:'))
            base='http://127.0.0.1:18767'+prefix
            panel=subprocess.Popen(['ip','netns','exec',NAMES[0],PANEL,'--bind','127.0.0.1'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);PROCESSES.append(panel)
            for attempt in range(50):
                try:api('GET','/auth/check');break
                except RuntimeError:time.sleep(.1)
            else:raise RuntimeError('panel did not start')
            token=api('POST','/auth/login',{'username':'multipeer-test','password':'isolated-multipeer-password'})['token']
            for index,name in enumerate(NAMES[1:-1]):spawn(name,['python3','-c',SERVER_CODE,f'FOREIGN-{index+1}:'])
            for carrier in ['icmp','udp','tcp','ipip','experimental']:
                ids=[];cores=[];keys=set();interfaces=set()
                for index in range(3):
                    pair=dict(engine='tun',name=f'{carrier}-foreign-{index+1}',local_ip='192.0.2.10',remote_ip=f'192.0.2.{20+10*index}',local_spoof='198.51.100.10',remote_spoof='198.51.100.20',listen='127.0.0.1:5000',forward='127.0.0.1:51820',upload=carrier,download=carrier,local_port=5001,remote_port=8090,save_role='local',tun_subnet=f'10.77.0.{index*4}/30',auto_start=False)
                    result=api('POST','/pairs',pair);ident=result['saved']['id'];ids.append(ident)
                    api('POST','/pairs',pair,expected=400)
                    remote=result['remote'];keys.add(remote['psk']);interfaces.add(remote['tun_interface'])
                    config=dict(role='remote',local_ip=remote['local_ip'],peer_ip=remote['client_ip'],spoof_source=remote['spoof_ip'],peer_spoof=remote['peer_spoof_ip'],send=remote['send_transport'],receive=remote['recv_transport'],send_port=remote['client_port'],receive_port=remote['listen_port'],interface=remote['tun_interface'],address=remote['tun_address'],peer_address=remote['tun_peer'],mtu=remote['tun_mtu'],psk=remote['psk'])
                    path=directory/f'{carrier}-{index}.json';path.write_text(json.dumps(config));os.chmod(path,0o600)
                    cores.append(spawn(NAMES[index+1],[CORE,'--config',str(path)]))
                    api('POST',f'/instances/{ident}/start',{})
                    api('POST',f'/instances/{ident}/forwards',dict(listen_port=11001+index,target_port=19001,protocol='tcp',enabled=True),expected=201)
                assert len(keys)==3 and len(interfaces)==3
                api('POST',f'/instances/{ids[1]}/forwards',dict(listen_port=11001,target_port=19001,enabled=True),expected=400)
                for attempt in range(40):
                    statuses=[api('GET',f'/instances/{ident}/status') for ident in ids]
                    if all(status['telemetry']['peer_connected'] for status in statuses):break
                    time.sleep(.2)
                else:raise AssertionError('all peers were not authenticated')
                def probe(index):
                    run('ip','netns','exec',NAMES[0],'python3','-c',CLIENT_CODE,f'10.77.0.{2+index*4}',str(11001+index),f'FOREIGN-{index+1}:',timeout=15)
                with concurrent.futures.ThreadPoolExecutor(max_workers=6) as workers:
                    list(workers.map(probe,[index%3 for index in range(18)]))
                # Kill only the core owned by this private namespace and config.
                candidates=[]
                for pid in run('ip','netns','pids',NAMES[0]).stdout.split():
                    try:arguments=pathlib.Path('/proc',pid,'cmdline').read_bytes().split(b'\0')
                    except FileNotFoundError:continue
                    if any(arg.endswith(f'tunnel-config-{ids[0]}.json'.encode()) for arg in arguments):candidates.append(int(pid))
                assert len(candidates)==1,candidates
                os.kill(candidates[0],9)
                probe(1);probe(2)
                for attempt in range(80):
                    status=api('GET',f'/instances/{ids[0]}/status')
                    if not pathlib.Path('/proc',str(candidates[0])).exists() and status['status']=='running' and status['telemetry']['peer_connected']:
                        try:probe(0);break
                        except RuntimeError:pass
                    time.sleep(.2)
                else:raise AssertionError('crashed core was not recovered')
                api('POST',f'/instances/{ids[0]}/stop',{})
                probe(1);probe(2)  # Stopping one tunnel must leave its neighbours operational.
                assert not api('GET',f'/instances/{ids[0]}/forwards')[0]['listening']
                api('POST',f'/instances/{ids[0]}/start',{})
                for attempt in range(40):
                    if api('GET',f'/instances/{ids[0]}/status')['telemetry']['peer_connected']:break
                    time.sleep(.2)
                else:raise AssertionError('restarted peer did not authenticate')
                probe(0);probe(1);probe(2)
                for index,ident in enumerate(ids):
                    rules=api('GET',f'/instances/{ident}/forwards');assert rules[0]['listening'];assert rules[0]['target']==f'10.77.0.{2+index*4}:19001'
                print(f'PASS simultaneous {carrier}: one Iran IP, three peers, shared carrier ports AND spoof source, separate TUN networks, concurrent service replies and forward ports; duplicate subnet/entry rejected; crash recovery and manual stop/restart isolated',flush=True)
                for ident in ids:api('DELETE',f'/instances/{ident}')
                for process in cores:process.terminate();process.wait(timeout=5)
        finally:
            for process in reversed(PROCESSES):
                if process.poll() is None:
                    process.terminate()
                    try:process.wait(timeout=10)
                    except subprocess.TimeoutExpired:process.kill();process.wait()
            for name in NAMES:subprocess.run(['ip','netns','del',name],capture_output=True)
if __name__=='__main__':main()
