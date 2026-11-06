#!/usr/bin/env python3
"""Real panels, real raw matrix and approved replacement of a live TUN pair.

Runs only inside two disposable namespaces. No external traffic or host routes.
"""
import json, os, pathlib, shutil, subprocess, tempfile, time
from integration_multipeer import API_CODE, SERVER_CODE, CLIENT_CODE

ROOT = pathlib.Path(__file__).resolve().parents[1]
PANEL = str(pathlib.Path(os.environ.get('SPOOF_PANEL_BINARY', ROOT/'dist/spoof-panel')).resolve())
CORE = str(pathlib.Path(os.environ.get('SPOOF_L3_BINARY', ROOT/'dist/spoof-l3')).resolve())

def run(*args, **options):
    result = subprocess.run(args, text=True, capture_output=True, **options)
    if result.returncode: raise RuntimeError((args, result.stdout, result.stderr))
    return result

def main():
    if os.geteuid() != 0: raise SystemExit('run as root')
    names = [f'spb-{os.getpid()}-{i}' for i in range(2)]
    processes, logs, bases, tokens = [], [], [], []
    with tempfile.TemporaryDirectory(prefix='spoof-benchmark-') as tmp:
        directory = pathlib.Path(tmp)
        def api(side, method, path, payload=None, expected=200):
            result = json.loads(run('ip','netns','exec',names[side],'python3','-c',
                API_CODE.replace('timeout=10','timeout=40'), bases[side], tokens[side], method,
                path, json.dumps(payload), timeout=45).stdout)
            assert result[0] == expected, (side, method, path, result)
            return result[1]
        def spawn(side, args, env=None):
            log = (directory/f'process-{len(processes)}.log').open('w+');logs.append(log)
            p = subprocess.Popen(['ip','netns','exec',names[side],*args], env=env, stdout=log, stderr=log)
            processes.append(p);return p
        def wait_peers(ids, timeout=40):
            until=time.monotonic()+timeout
            while time.monotonic()<until:
                states=[api(i,'GET',f'/instances/{ids[i]}/status') for i in range(2)]
                if all(s['telemetry']['peer_connected'] for s in states): return
                time.sleep(.2)
            raise AssertionError(('peer readiness',states))
        try:
            for name in names: run('ip','netns','add',name)
            run('ip','link','add','bma0','type','veth','peer','name','bmb0')
            for i, dev in enumerate(['bma0','bmb0']):
                run('ip','link','set',dev,'netns',names[i])
                for args in [('link','set','lo','up'),('address','add',f'192.0.2.{i+1}/30','dev',dev),('link','set',dev,'up'),('route','add','198.51.100.0/24','dev',dev)]:run('ip','-n',names[i],*args)
                data=directory/f'panel-{i}';data.mkdir();shutil.copy2(CORE,data/'spoof-l3')
                env={**os.environ,'SPOOF_DATA_DIR':str(data),'SPOOF_PUBLIC_IP':f'192.0.2.{i+1}'}
                run(PANEL,'--setup','--setup-user','benchmark','--setup-pass','isolated-benchmark-password','--setup-port','18889',env=env)
                prefix=next(l.split(': ',1)[1] for l in (data/'credentials.txt').read_text().splitlines() if l.startswith('Web Path:'))
                bases.append('http://127.0.0.1:18889'+prefix);tokens.append('')
                spawn(i,[PANEL,'--bind','127.0.0.1'],env)
                for _ in range(60):
                    try:api(i,'GET','/auth/check');break
                    except RuntimeError:time.sleep(.1)
                else:raise AssertionError('panel did not start')
                tokens[i]=api(i,'POST','/auth/login',{'username':'benchmark','password':'isolated-benchmark-password'})['token']
            pair=api(0,'POST','/pairs',dict(engine='tun',name='approved-carrier-change',local_ip='192.0.2.1',remote_ip='192.0.2.2',local_spoof='198.51.100.10',remote_spoof='198.51.100.20',listen='127.0.0.1:5000',forward='127.0.0.1:51820',upload='icmp',download='icmp',local_port=5001,remote_port=8090,save_role='local',tun_subnet='10.88.0.0/30',auto_start=False))
            local=pair['saved'];remote=api(1,'POST','/instances',pair['remote'],201);ids=[local['id'],remote['id']]
            for i in range(2):api(i,'POST',f'/instances/{ids[i]}/start',{})
            wait_peers(ids)
            api(0,'POST',f'/instances/{ids[0]}/forwards',dict(name='preserved-forward',protocol='tcp',listen_port=11001,target_port=19001,enabled=True),201)
            spawn(1,['python3','-c',SERVER_CODE,'FOREIGN:'])
            setup=api(1,'POST','/connection-test/receiver',dict(local_ip='192.0.2.2',peer_ip='192.0.2.1',source='198.51.100.20',peer_source='198.51.100.10',port_a=5001,port_b=8090,target_id=ids[1],allow_apply=True),201)
            api(0,'POST','/connection-test/start',dict(code=setup['code'],target_id=ids[0],options=dict(rate_mbps=1,seconds=2)),202)
            until=time.monotonic()+240
            while time.monotonic()<until:
                state=api(0,'GET','/connection-test')
                for i in range(2):assert api(i,'GET',f'/instances/{ids[i]}/status')['status']=='running'
                if state['status']=='done':break
                time.sleep(.5)
            assert state['status']=='done' and len(state['results'])==25 and all(r['status']=='passed' for r in state['results']),state
            # A concrete asymmetric option: local UDP, remote raw TCP. The UI
            # offers the same explicit per-cell override of the recommendation.
            preview=api(0,'POST','/connection-test/preview',dict(index=7))
            for i in range(2):
                saved=api(i,'GET',f'/instances/{ids[i]}')['instance']
                assert saved['send_transport']=='icmp' and saved['recv_transport']=='icmp'
            api(0,'POST','/connection-test/apply',dict(token=preview['token']),202)
            until=time.monotonic()+65
            while time.monotonic()<until:
                state=api(0,'GET','/connection-test')
                if state['status']!='applying':break
                time.sleep(.25)
            assert state['applied'],state
            assert api(1,'GET','/connection-test')['applied']
            wait_peers(ids)
            for i, original in enumerate([local,remote]):
                saved=api(i,'GET',f'/instances/{ids[i]}')['instance']
                for field in ['id','psk','tun_address','tun_peer','tun_interface']:assert saved[field]==original[field],(field,saved,original)
                assert saved['send_transport']==['udp','tcp'][i] and saved['recv_transport']==['tcp','udp'][i]
                pool=api(i,'GET',f'/instances/{ids[i]}/pools/state')['runtime']['pool']
                assert pool['selected']==1 and pool['active_size']==100,pool
            rules=api(0,'GET',f'/instances/{ids[0]}/forwards');assert rules[0]['listening'] and rules[0]['rule']['name']=='preserved-forward',rules
            client=CLIENT_CODE.replace("'192.0.2.10'","'192.0.2.1'")
            run('ip','netns','exec',names[0],'python3','-c',client,'10.88.0.2','11001','FOREIGN:',timeout=20)
            run('ip','netns','exec',names[0],'ping','-c','3','-W','2','10.88.0.2',timeout=10)
            print('PASS real panels: all 25 raw carrier tests alongside a live TUN; no pre-approval mutation; asymmetric replacement; same keys, IDs, TUNs and forward rules; synchronized UI; verified cohort; real TCP, UDP, ICMP and forwarding after apply',flush=True)
        except Exception:
            for log in logs:log.flush();log.seek(0);print(log.read()[-6000:])
            for i in range(len(bases)):
                try:print('STATE',i,api(i,'GET','/connection-test'))
                except Exception as error:print('DIAGNOSTIC',error)
            raise
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    process.terminate()
                    try:process.wait(timeout=15)
                    except subprocess.TimeoutExpired:process.kill();process.wait()
            for log in logs:log.close()
            for name in names:subprocess.run(['ip','netns','del',name],capture_output=True)

if __name__=='__main__':main()
