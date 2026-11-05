#!/usr/bin/env python3
"""Build the pinned hosts, run the record-filter loop and print the answer, correction, replay and restatement."""
import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys

ROOT=Path(__file__).resolve().parents[2]
CLIENT=ROOT/'src/lifecycle-core/client'
FIXTURE=ROOT/'src/cel-lifecycle/fixtures/lifecycle-scenario.json'
GO_MIN=(1,27,2)

class MissingStep(Exception):pass
def find(trace,name):
 for x in trace:
  if isinstance(x,dict) and x.get('name')==name:
   try:return x['response']['data']['result']
   except (KeyError,TypeError):raise MissingStep('step %s has no response.data.result'%name)
 raise MissingStep('step %s is not in the trace'%name)
def receipt_command(w,ref):
 """Return a shell command that prints the receipt ref from the demo Git store. It uses the read operation `receipt` of the Go lifecycle host."""
 q=lambda x:shlex.quote(str(x));loop=w/'loop'
 request=json.dumps({'op':'receipt','receipt_ref':ref},separators=(',',':'))
 return "printf '%%s\\n' %s | %s --repo %s --context %s --runtime %s --work %s | python3 -m json.tool"%(
  q(request),q(w/'bin/lifecycle-core'),q(loop/'state.git'),q(loop/'reader.json'),q(w/'bin/cel-handoff'),q(loop))
def write_summary(w,summary):(w/'summary.json').write_text(json.dumps(summary,indent=2,ensure_ascii=False)+'\n')
def step_failed(w,step,code,stdout,stderr):
 # Keep the failed step's output in summary.json and on stderr, then stop.
 write_summary(w,{'kind':'QuickstartSummary','status':'failed','failed_step':step,'exit_code':code,'stdout':stdout,'stderr':stderr,'work':str(w)})
 sys.stderr.write('%s failed (exit %d)\n%s%s'%(step,code,stdout,stderr))
 print('Result: failed. %s failed. Details: %s'%(step,w/'summary.json'));raise SystemExit(1)

def summarize(w,fixture,build,loop):
 """Make the summary from the loop result. Each failure path records its reason."""
 expected=fixture['expected'];deps={x['Path']:x['Version'] for x in build['build']['Deps']};errors=[]
 path=w/'loop/scenario-results.json'
 try:
  results=json.loads(path.read_text())
  if not isinstance(results,dict):raise ValueError('the top level is not an object')
 except FileNotFoundError:results={'status':'failed'};errors.append('%s does not exist'%path.name)
 except ValueError as error:results={'status':'failed'};errors.append('%s is not valid JSON: %s'%(path.name,error))
 if loop.returncode:
  # Keep the scenario's own error output; a nonzero exit is a failure even with a results file.
  results['status']='failed';results['error']=results.get('error') or loop.stderr.strip() or 'scenario.py exited with status %d'%loop.returncode
 summary={'kind':'QuickstartSummary','function':fixture['function'],'implementation_ref':build['implementation_ref'],'cel_go':deps.get('cel.dev/cel-go'),
          'loop_status':results.get('status'),'loop_error':results.get('error'),
          'loop_output':{'exit_code':loop.returncode,'stdout':loop.stdout,'stderr':loop.stderr},'expected':expected,'work':str(w)}
 checks={}
 if results.get('status')=='passed':
  try:
   trace=results['trace'];pins=results['pins']
   answer=find(trace,'november-before-correction');replay=find(trace,'pinned-replay');restated=find(trace,'later-restatement')
   correction=find(trace,'correction-source')
   summary|={'input':fixture['original_input'],'date':'2026-11-15',
    'answer':{'value':answer['actual']['value'],'receipt_ref':answer['receipt_ref'],'snapshot_ref':answer['receipt']['selection']['snapshot_ref']},
    'correction':{'from_state':fixture['amendment']['allowed_state'],'to_state':fixture['correction']['allowed_state'],'source_ref':correction['source_ref'],
                  'event_ref':pins['correction_event'],'snapshot_ref':pins['correction_snapshot']},
    'replay':{'value':replay['actual']['value'],'snapshot_ref':replay['receipt']['selection']['snapshot_ref']},
    'restatement':{'value':restated['actual']['value'],'snapshot_ref':restated['receipt']['selection']['snapshot_ref']}}
   summary['receipt_command']=receipt_command(w,summary['answer']['receipt_ref'])
   # The expected values come from the fixture. The replay must keep the answer's snapshot; the restatement must use the correction.
   checks={'answer-value':summary['answer']['value'] is expected['future_november_reviewed'],
           'replay-value':summary['replay']['value'] is expected['pinned_replay'],
           'restatement-value':summary['restatement']['value'] is expected['later_restatement'],
           'replay-keeps-answer-snapshot':summary['replay']['snapshot_ref']==summary['answer']['snapshot_ref'],
           'restatement-uses-correction-snapshot':summary['restatement']['snapshot_ref']==pins['correction_snapshot']}
  except MissingStep as error:errors.append(str(error))
  except (KeyError,TypeError) as error:errors.append('the scenario results are incomplete: %s %s'%(type(error).__name__,error))
 summary['errors']=errors;summary['checks']=checks;summary['failed_checks']=[k for k,v in checks.items() if not v]
 summary['matches_fixture']=bool(checks) and not errors and not summary['failed_checks']
 summary['status']='passed' if results.get('status')=='passed' and summary['matches_fixture'] else 'failed'
 return summary

def short(ref):return str(ref)[:19]+'...'
def report(summary):
 expected=summary['expected'];v=lambda x:json.dumps(x)
 print()
 print('Entelos demo: each answer stays explainable after its rule changes.')
 print('The rule %s answers one question: can a user see this record?'%summary['function'])
 rule=Path(summary['work'])/'record-bundle/rule.cel'
 if rule.exists():
  f=json.loads(FIXTURE.read_text());i,a=f['initial'],f['amendment']
  print('  Rule:   %s'%rule.read_text().strip())
  print('  Values: allowed_state is "%s" from %s, then "%s" from %s.'%(i['allowed_state'],i['from'],a['allowed_state'],a['from']))
 print()
 if 'restatement' in summary:
  s=summary;c=s['correction']
  record='"%s", %s'%(s['input'].get('state'),'archived' if s['input'].get('archived') else 'not archived')
  print('  1. Answer       Is a %s record visible on %s? -> %s (expected %s)'%(record,s['date'],v(s['answer']['value']),v(expected['future_november_reviewed'])))
  print('                  A receipt in the Git store saves the exact rule version, input and answer.')
  print('  2. Rule change  A reviewer changes allowed_state from "%s" to "%s".'%(c['from_state'],c['to_state']))
  print('                  This makes a new rule version. The old version and the receipt stay.')
  print('  3. Replay       Why was the answer %s? Run the receipt on its old rule version. -> %s (expected %s)'%(v(s['answer']['value']),v(s['replay']['value']),v(expected['pinned_replay'])))
  print('  4. Restatement  Under the new rule, what is the answer for the same record and date? -> %s (expected %s)'%(v(s['restatement']['value']),v(expected['later_restatement'])))
  print('                  This is a new, separate answer. The first answer stays in the history.')
  print()
  print('  The expected values come from the fixture file, not from this run.')
  print('  Input:   %s'%json.dumps(s['input'],separators=(',',':')))
  print('  Records: receipt %s, old rule %s,'%(short(s['answer']['receipt_ref']),short(s['answer']['snapshot_ref'])))
  print('           new rule %s, change event %s'%(short(c['snapshot_ref']),short(c['event_ref'])))
  print('  To print the receipt of answer 1 from the Git store, run:')
  print('    %s'%s['receipt_command'])
 print('  Build:   %s (cel-go %s)'%(short(summary['implementation_ref']),summary['cel_go']))
 if summary['loop_error']:print('  The loop failed: %s'%summary['loop_error'])
 for error in summary['errors']:print('  Error: %s'%error)
 for name in summary['failed_checks']:print('  Failed comparison: %s'%name)
 if summary['loop_output']['exit_code']:print('  The scenario stdout and stderr are in summary.json (loop_output).')
 print('Result: %s. Details: %s'%(summary['status'],Path(summary['work'])/'summary.json'))
 if summary['status']=='passed':print('Next: read "After the demo" in README.md.')

def check_tools(p):
 # Check the tools before the build, so that a missing tool gives a clear error and the work path stays free.
 for tool,url in [('go','https://go.dev/dl/'),('git','https://git-scm.com/downloads')]:
  if not shutil.which(tool):p.error('%s is not on PATH. Install it from %s, then run the demo again.'%(tool,url))
 problem=go_problem(subprocess.run(['go','version'],capture_output=True,text=True,env=os.environ|{'GOTOOLCHAIN':'local'}))
 if problem:p.error(problem)

def go_problem(result):
 """Return the reason that the `go version` result is not usable, or None. A devel build passes; the build step then shows its own error."""
 need='Go %s or later is necessary'%'.'.join(map(str,GO_MIN));fix='Install it from https://go.dev/dl/, then run the demo again.'
 if result.returncode:return '%s, but `go version` failed (exit %d): %s. %s'%(need,result.returncode,(result.stderr or result.stdout).strip() or 'no output',fix)
 if re.search(r'\bgo version devel\b',result.stdout):return None
 m=re.search(r'go(\d+)\.(\d+)(?:\.(\d+))?',result.stdout)
 if m and tuple(int(x or 0) for x in m.groups())>=GO_MIN:return None
 return '%s; found %s. %s'%(need,result.stdout.strip() or 'no version',fix)

def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--work',type=Path,required=True,help='new directory outside this source tree')
 a=p.parse_args();w=a.work.resolve()
 if w==ROOT or ROOT in w.parents:p.error('work must be outside this source tree')
 check_tools(p)
 if w.exists():p.error('%s already exists. Choose a path that does not exist, for example --work "$(mktemp -d)/run". An earlier run is not overwritten.'%w)
 for name in ['bin','tmp','cache/build','cache/mod','cache/gopath','cache/telemetry']:(w/name).mkdir(parents=True,exist_ok=True)
 (w/'cache/telemetry/mode').write_text('off\n')
 # The same isolated Go and Git settings as verify.py, so the demo builds the same host identity.
 env={k:v for k,v in os.environ.items() if not k.startswith('GIT_')}|{
  'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':os.devnull,'GIT_TERMINAL_PROMPT':'0',
  'GOCACHE':str(w/'cache/build'),'GOMODCACHE':str(w/'cache/mod'),'GOPATH':str(w/'cache/gopath'),
  'GOTMPDIR':str(w/'tmp'),'TMPDIR':str(w/'tmp'),'TEST_TELEMETRY_DIR':str(w/'cache/telemetry'),
  'GOENV':'off','GOWORK':'off','GOFLAGS':'','GOTOOLCHAIN':'local','PYTHONDONTWRITEBYTECODE':'1',
  'LIFECYCLE_CLIENT_SOURCE':str(CLIENT)}
 def run(step,args,cwd=ROOT):
  result=subprocess.run([str(x) for x in args],cwd=cwd,env=env,capture_output=True,text=True)
  if result.returncode:step_failed(w,step,result.returncode,result.stdout,result.stderr)
  return result
 # Each run has a new module cache, so each run downloads the pinned Go modules.
 print('Building the pinned hosts (this downloads the pinned Go modules)...',flush=True)
 binary=w/'bin/cel-handoff'
 run('build-host',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',binary,'./cmd/cel-handoff'],ROOT/'src/cel-handoff')
 identity=run('host-identity',[binary,'-build'])
 try:build=json.loads(identity.stdout)
 except ValueError as error:step_failed(w,'host-identity',identity.returncode or 1,identity.stdout,identity.stderr+'the output is not valid JSON: %s\n'%error)
 run('build-lifecycle',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',w/'bin/lifecycle-core','./cmd/lifecycle-core'],ROOT/'src/lifecycle-core')
 run('prepare-record',[sys.executable,'-B',ROOT/'src/cel-handoff/tools/prepare.py','--bundle',w/'record-bundle','--binary',binary])
 print('Running the record-filter loop...',flush=True)
 loop=subprocess.run([str(x) for x in [sys.executable,'-B',CLIENT/'scenario.py','--work',w/'loop','--runtime',binary,'--bundle',w/'record-bundle','--fixture',FIXTURE,'--host',w/'bin/lifecycle-core']],
                     cwd=ROOT,env=env,capture_output=True,text=True)
 summary=summarize(w,json.loads(FIXTURE.read_text()),build,loop)
 write_summary(w,summary);report(summary)
 return summary['status']!='passed'

if __name__=='__main__':raise SystemExit(main())
