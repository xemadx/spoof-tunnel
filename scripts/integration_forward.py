#!/usr/bin/env python3
"""Verify delayed TUN readiness in a private namespace with no public traffic."""
import os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
name='spoof-forward-'+str(os.getpid())
with tempfile.TemporaryDirectory(prefix='spoof-forward-') as directory:
 binary=str(pathlib.Path(directory)/'forward.test')
 subprocess.run(['go','test','-race','-c','-o',binary,'./internal/forward'],cwd=root/'panel/backend',check=True)
 subprocess.run(['ip','netns','add',name],check=True)
 try:
  subprocess.run(['ip','-n',name,'link','set','lo','up'],check=True)
  subprocess.run(['ip','netns','exec',name,'env','SPOOF_FORWARD_ISOLATED=1',binary,'-test.run','TestDelayedTunnelBecomesReady|TestStopCancelsTunnelReadinessWait|TestMissingTunnelCannotFallBackToOtherRoutes','-test.v'],check=True,timeout=20)
 finally:subprocess.run(['ip','netns','del',name],check=True)
