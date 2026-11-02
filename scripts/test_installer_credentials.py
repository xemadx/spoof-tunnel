#!/usr/bin/env python3
"""Real setup/HTTP checks including a controlling-terminal upgrade prompt."""
import errno,json,os,pathlib,pty,re,select,subprocess,tempfile,time,urllib.request,urllib.error
root=pathlib.Path(__file__).resolve().parents[1]
binary=str(pathlib.Path(os.environ.get('SPOOF_PANEL_BINARY',root/'dist/spoof-panel')).resolve())
helper=str(root/'scripts/setup-credentials.sh')
with tempfile.TemporaryDirectory(prefix='spoof-installer-test-') as data:
 env={**os.environ,'SPOOF_PUBLIC_IP':'127.0.0.1','SPOOF_DATA_DIR':data}
 args=['bash','-c','source "$1"; spoof_setup_credentials "$2" "$3"','installer-test',helper,binary,data]
 def setup(action):return subprocess.check_output(args,env={**env,'SPOOF_PASSWORD_ACTION':action},text=True,timeout=20)
 def password(output):return re.search(r'Password:\s+(\S+)',output).group(1)
 first=password(setup('keep'));assert len(first)>=12
 assert first not in pathlib.Path(data,'credentials.txt').read_text()
 assert 'unchanged (not stored)' in setup('keep')
 second=password(setup('reset'));assert second!=first and len(second)==32
 child,fd=pty.fork()
 if child==0:os.execvpe(args[0],args,{**env,'SPOOF_PASSWORD_ACTION':'ask'})
 output=b'';sent=False;deadline=time.monotonic()+20
 while time.monotonic()<deadline:
  ready,_,_=select.select([fd],[],[],.2)
  if not ready:continue
  try:chunk=os.read(fd,65536)
  except OSError as error:
   if error.errno==errno.EIO:break
   raise
  if not chunk:break
  output+=chunk
  if b'Choose [1/2]:' in output and not sent:os.write(fd,b'2\n');sent=True
 else:os.kill(child,9);raise AssertionError('interactive prompt timed out')
 os.close(fd);_,status=os.waitpid(child,0);assert status==0 and sent
 third=password(output.decode());assert third not in [first,second]
 info=pathlib.Path(data,'credentials.txt').read_text();assert third not in info
 username=re.search(r'Username: (.+)',info).group(1);base=re.search(r'URL: (.+)',info).group(1).rstrip('/')
 process=subprocess.Popen([binary,'--bind','127.0.0.1'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 try:
  for _ in range(50):
   try:urllib.request.urlopen(base+'/api/auth/check',timeout=2).close();break
   except OSError:time.sleep(.1)
  for value,expected in [(first,401),(second,401),(third,200)]:
   req=urllib.request.Request(base+'/api/auth/login',data=json.dumps({'username':username,'password':value}).encode(),headers={'Content-Type':'application/json'})
   try:
    with urllib.request.urlopen(req,timeout=5) as response:status=response.status
   except urllib.error.HTTPError as error:status=error.code
   assert status==expected,status
 finally:
  process.terminate();process.wait(timeout=10)
 print('PASS initial password display, upgrade preservation, explicit reset, real TTY choice, old-password rejection and successful new-password login')
