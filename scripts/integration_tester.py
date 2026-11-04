#!/usr/bin/env python3
"""Verify sender coverage in a loopback-only namespace: no public packets."""
import os,pathlib,subprocess,tempfile
root=pathlib.Path(__file__).resolve().parents[1]
name='spoof-tester-'+str(os.getpid())
with tempfile.TemporaryDirectory(prefix='spoof-tester-') as temp:
 binary=str(pathlib.Path(temp)/'tester.test')
 subprocess.run(['go','test','-race','-c','-o',binary,'./internal/tester'],cwd=root,check=True)
 subprocess.run(['ip','netns','add',name],check=True)
 try:
  subprocess.run(['ip','-n',name,'link','set','lo','up'],check=True)
  subprocess.run(['ip','netns','exec',name,'env','SPOOF_TESTER_ISOLATED=1',binary,'-test.run','TestSenderDeliversEveryListedIP|TestCancelledSenderDoesNotClaimCompletion|TestReceiverRejectsDuplicatesAndUnrelatedTraffic','-test.v'],check=True,timeout=30)
 finally:subprocess.run(['ip','netns','del',name],check=True)
