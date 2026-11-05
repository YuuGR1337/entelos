#!/usr/bin/env python3
"""Regression checks for exported stream framing and application audit identity."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT=Path(os.environ.get('DECISION_EXPORT_SOURCE',Path(__file__).resolve().parents[1])).resolve()
os.environ['LIFECYCLE_CLIENT_SOURCE']=str(ROOT/'src/lifecycle-core/client')
sys.path.insert(0,str(ROOT/'examples/ci-trigger'))
from support import Fault,encode,content_ref,envelope,fixture,root
from adapter import Ingress,project,link,verify_link
WORK=Path(os.environ['EXPORT_TEST_WORK']);WORK.mkdir(parents=True,exist_ok=True)
# LIFECYCLE_HOST names the Go JSONL host cmd/lifecycle-core. The ordinary baseline of ci-trigger runs under the same host.
HOST=Path(os.environ['LIFECYCLE_HOST'])

class ExportBoundaries(unittest.TestCase):
 def setUp(self):self.work=Path(tempfile.mkdtemp(dir=WORK));self.repo=self.work/'state.git'
 def test_lifecycle_oversized_line_cannot_execute_a_remainder(self):
  context={'actor':'reviewer','roles':['reviewer'],'scope':'framing-demo','runner_actor':'fixture-runner','worker':'framing-worker','functions':['record.visible']}
  path=self.work/'context.json';path.write_bytes(encode(context))
  q={'op':'source','expected_revision':'','request_key':'oversized-remainder','function':'record.visible','from':'2026-10-01','until':'2026-12-01','text':'Neutral framing regression.','supersedes':[],'reason':'Do not execute a fragment of a rejected line.'}
  wire=b' '*1048577+encode(q)
  p=subprocess.run([str(HOST),'--repo',str(self.repo),'--context',str(path),'--runtime',sys.executable,'--work',str(self.work)],input=wire,capture_output=True)
  self.assertEqual(root(self.repo),'','a fragment of the rejected line changed accepted state')
  messages=[json.loads(line) for line in p.stdout.splitlines()]
  self.assertEqual(len(messages),1);self.assertEqual(messages[0]['status'],'invalid_request');self.assertNotEqual(p.returncode,0)
 def test_audit_identity_covers_the_receipt_link(self):
  raw=(ROOT/'examples/ci-trigger/fixtures/ready-review-envelope.json').read_bytes();ingress=Ingress(True,fixture()['repository']);inputs=project(raw,ingress)
  invocation={'receipt_ref':'sha256:'+'1'*64,'receipt':{'inputs_ref':content_ref(envelope('application/json',encode(inputs)))}}
  audit=link(raw,ingress,invocation,self.work/'audit')
  self.assertEqual(verify_link(audit,self.work/'audit',ingress),inputs)
  altered=copy.deepcopy(audit);altered['audit']['receipt_ref']='sha256:'+'2'*64
  with self.assertRaises(Fault) as caught:verify_link(altered,self.work/'audit',ingress)
  self.assertEqual(caught.exception.code,'artifact_mismatch')
 def test_audit_paths_require_content_reference_syntax(self):
  raw=(ROOT/'examples/ci-trigger/fixtures/ready-review-envelope.json').read_bytes();ingress=Ingress(True,fixture()['repository']);inputs=project(raw,ingress)
  invocation={'receipt_ref':'sha256:'+'1'*64,'receipt':{'inputs_ref':content_ref(envelope('application/json',encode(inputs)))}}
  audit=link(raw,ingress,invocation,self.work/'audit');audit['audit']['raw_ref']='sha256:../../unapproved'
  with self.assertRaises(Fault) as caught:verify_link(audit,self.work/'audit',ingress)
  self.assertEqual(caught.exception.code,'invalid_reference')

if __name__=='__main__':
 class Results(unittest.TextTestResult):
  def __init__(self,*a,**kw):super().__init__(*a,**kw);self.passed=[]
  def addSuccess(self,test):self.passed.append(test.id());super().addSuccess(test)
 r=unittest.TextTestRunner(verbosity=2,resultclass=Results).run(unittest.defaultTestLoader.loadTestsFromTestCase(ExportBoundaries))
 record={'tests_run':r.testsRun,'passed':r.passed,'failures':[{'test':t.id(),'traceback':s} for t,s in r.failures],'errors':[{'test':t.id(),'traceback':s} for t,s in r.errors],'successful':r.wasSuccessful()}
 (WORK/'test-results.json').write_bytes(encode(record));raise SystemExit(not r.wasSuccessful())
