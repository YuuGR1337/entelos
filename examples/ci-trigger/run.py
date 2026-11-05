#!/usr/bin/env python3
"""Execute second-consumer reuse and comparison; all generated state stays in --work."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
from support import LIFE,ROOT,Fault,encode,decode,fixture,handle,read_store,record,root,host_sources
from scenario import Driver as RecordDriver,demonstrate as original_demonstrate
from demo import CIDriver,demonstrate
from adapter import Ingress,project,link,verify_link

p=argparse.ArgumentParser(description=__doc__);p.add_argument('--work',type=Path,required=True);p.add_argument('--cel-binary',type=Path,required=True);p.add_argument('--author-binary',type=Path,required=True);p.add_argument('--record-bundle',type=Path,required=True);p.add_argument('--host',type=Path,required=True,help='Go lifecycle-core executable')
a=p.parse_args();w=a.work.resolve()
SOURCE=ROOT.parents[1]
if w==SOURCE or SOURCE in w.parents:p.error('work must be outside the source tree')
if w.exists():p.error('work directory exists; preserve previous runs')
w.mkdir(parents=True);(w/'tmp').mkdir();os.environ['TMPDIR']=str(w/'tmp');os.environ['PYTHONDONTWRITEBYTECODE']='1';os.environ['LIFECYCLE_CLIENT_SOURCE']=str(LIFE)
for key in list(os.environ):
    if key.startswith('GIT_'):os.environ.pop(key)
checks=[];commands=[];traces={};pins={};errors=[];direct_operations=[]
def check(name,question,passed,actual):
    checks.append({'name':name,'question':question,'passed':bool(passed),'actual':actual})
    if not passed:raise AssertionError(name)
def command(name,args):
    proc=subprocess.run([str(x) for x in args],capture_output=True,text=True)
    commands.append({'name':name,'args':[str(x) for x in args],'exit_code':proc.returncode,'stdout':proc.stdout,'stderr':proc.stderr})
    return proc
def fault(code,fn):
    try:fn()
    except Fault as e:
        if e.code!=code:raise AssertionError((code,e.record()))
        return e.record()
    raise AssertionError('expected '+code)

def direct(name,context,request,expected='ok'):
    """Send one request to a new Go host process on the shared CI store, with a context file of the host."""
    response,_=handle(a.host,ci.repo,context,a.cel_binary,w,request)
    result=response.get('data',response) if response['status']=='ok' else response
    direct_operations.append({'name':name,'request':request,'response':response,'expected_status':expected})
    if response['status']!=expected:raise AssertionError(name+': '+str(response))
    return result

try:
    decl=fixture();inventory=decode((SOURCE/'checksums.json').read_bytes())
    core=SOURCE/'src/lifecycle-core';life_files=host_sources()
    life_hashes={x:hashlib.sha256((core/x).read_bytes()).hexdigest() for x in life_files}
    cel=SOURCE/'src/cel-handoff';cel_files=['handoff.go','json.go','identity.go','cmd/cel-handoff/main.go','go.mod','go.sum']
    cel_hashes={x:hashlib.sha256((cel/x).read_bytes()).hexdigest() for x in cel_files}
    check('unchanged-production-sources','Do exported shared sources retain their reviewed checksums?',all(inventory['src/lifecycle-core/'+x]==v for x,v in life_hashes.items()) and all(inventory['src/cel-handoff/'+x]==v for x,v in cel_hashes.items()),{'lifecycle':life_hashes,'cel':cel_hashes,'changed_shared_files':[]})
    actual=decode(subprocess.check_output([str(a.cel_binary),'-build']));deps={x['Path']:x['Version'] for x in actual['build']['Deps']}
    check('pinned-runtime','Does the freshly identified host use the pinned CEL dependency closure?',deps.get('cel.dev/cel-go')=='v0.32.0' and deps.get('cel.dev/expr')=='v0.25.3',actual)
    original=RecordDriver(w/'original',a.cel_binary,decode((SOURCE/'src/cel-lifecycle/fixtures/lifecycle-scenario.json').read_bytes()),a.host.resolve())
    original_pins=original_demonstrate(original,a.record_bundle);traces['original']={'trace':original.trace,'checks':original.checks,'pins':original_pins}
    ci=CIDriver(w/'cel',a.cel_binary,'cel',a.host.resolve(),original.repo);ci_pins=demonstrate(ci,a.cel_binary,seeded=True);traces['cel']={'trace':ci.trace,'checks':ci.checks,'pins':ci_pins}
    # The ordinary runtime runs with this interpreter, so its build identity equals the identity in the proposals.
    ordinary=w/'bin/ordinary-runtime';ordinary.parent.mkdir()
    ordinary.write_text('#!/bin/sh\nexec %s -B %s "$@"\n'%(shlex.quote(sys.executable),shlex.quote(str(ROOT/'ordinary.py'))));ordinary.chmod(0o755)
    py=CIDriver(w/'ordinary',ordinary,'ordinary',a.host.resolve());py_pins=demonstrate(py,a.cel_binary);traces['ordinary']={'trace':py.trace,'checks':py.checks,'pins':py_pins}
    check('same-full-completion-targets','Can both CEL and ordinary frozen functions complete the same lifecycle targets?',all(x['passed'] for d in (ci,py) for x in d.checks),{'cel':ci.checks,'ordinary':py.checks})
    check('behavior-baseline','Does ordinary code match independently stated correction/replay outcomes?',ci_pins['old_answer']['value'] is py_pins['old_answer']['value'] is False and ci_pins['new_answer']['value'] is py_pins['new_answer']['value'] is True,{'cel':ci_pins,'ordinary':py_pins})
    # The original and new function are now in one root history, with distinct logical worker configuration.
    old_reader=original.work/'reader.json';old_caller=original.work/'caller.json'
    replay=direct('original-replay-after-ci',old_reader,{'op':'replay','expected_revision':root(ci.repo),'request_key':'record-after-ci:replay','receipt_ref':original_pins['old_receipt'],'reason':'verify coexisting consumer did not change old closure'})
    old_snapshot=record(read_store(ci.repo),original_pins['correction_snapshot'],'snapshot')
    old_call=direct('original-current-call-after-ci',old_caller,{'op':'invoke','expected_revision':root(ci.repo),'request_key':'record-after-ci:invoke','function':'record.visible','date':'2026-11-15','expected_bundle_ref':old_snapshot['bundle_ref'],'inputs':{'state':'reviewed','archived':False}})
    check('two-consumer-coexistence','Does new consumer history preserve the first consumer resolution, preparation and replay?',replay['result']['actual']['value'] is True and old_call['result']['actual']['value'] is False,{'original_replay':replay,'original_current_call':old_call})
    # Probe the existing one-slot worker model, then restore the original worker preparation.
    ci_worker_context=decode(ci.contexts['worker'].read_bytes());ci_worker_context['worker']='local-worker'
    overloaded=w/'ci-worker-local.json';overloaded.write_bytes(encode(ci_worker_context))
    direct('shared-worker-load-probe',overloaded,{'op':'load','expected_revision':root(ci.repo),'request_key':'worker-slot:ci-load','function':decl['function'],'date':'2026-11-15'})
    slot_error=direct('original-worker-slot-error',old_caller,{'op':'invoke','expected_revision':root(ci.repo),'request_key':'worker-slot:old-call','function':'record.visible','date':'2026-11-15','expected_bundle_ref':old_snapshot['bundle_ref'],'inputs':{'state':'reviewed','archived':False}},expected='revision_not_loaded')
    direct('restore-original-worker',original.work/'worker.json',{'op':'load','expected_revision':root(ci.repo),'request_key':'worker-slot:restore','function':'record.visible','date':'2026-11-15'})
    check('worker-configuration-burden','Does reuse require distinct acknowledgement slots instead of a shared cache claim?',slot_error['status']=='revision_not_loaded',slot_error)
    # Projection rejects malformed transport separately from a valid false computation result.
    raw=(ROOT/'fixtures/ready-review-envelope.json').read_bytes();valid=decode(raw);ingress=Ingress(True,decl['repository']);projection_errors=[]
    projection_errors.append(fault('unverified_ingress',lambda:project(raw,Ingress(False,decl['repository']))))
    for name,edit,code in [
      ('null-draft',lambda x:x['body']['pull_request'].update(draft=None),'invalid_envelope'),
      ('missing-branch',lambda x:x['body']['pull_request']['base'].pop('ref'),'invalid_envelope'),
      ('string-draft',lambda x:x['body']['pull_request'].update(draft='false'),'invalid_envelope'),
      ('ambiguous-header',lambda x:x['headers'].update({'x-github-event':['pull_request']}),'invalid_envelope'),
      ('multi-header',lambda x:x['headers'].update({'X-GitHub-Event':['pull_request','push']}),'invalid_envelope'),
      ('wrong-event',lambda x:x['headers'].update({'X-GitHub-Event':['push']}),'unsupported_event'),
      ('foreign-repository',lambda x:x['body']['repository'].update(full_name='other/repository'),'out_of_scope'),
      ('self-verification',lambda x:x.update(verified=True),'invalid_envelope')]:
        altered=copy.deepcopy(valid);edit(altered);error=fault(code,lambda:project(encode(altered),ingress));projection_errors.append({'case':name,**error})
    check('application-projection-failures','What does the real host have to reject before a typed call?',True,projection_errors)
    # Audit retention failure is explicit and does not change normalized generic replay.
    audit=ci_pins['audit'];audit_dir=ci.work/'audit';raw_path=audit_dir/(audit['audit']['raw_ref'][7:]+'.json');saved=raw_path.read_bytes();raw_path.write_bytes(saved+b' ')
    damaged=fault('artifact_mismatch',lambda:verify_link(audit,audit_dir,ingress));raw_path.write_bytes(saved)
    check('audit-integrity-and-boundary','Can raw-event evidence fail independently of retained normalized execution inputs?',verify_link(audit,audit_dir,ingress)==decl['original_inputs'],damaged)
    # Existing author tools handle the new declarations/rules without a new authoring subsystem.
    author=[]
    for stage,policy in decl['policies'].items():
        files=ci.bundle(stage,a.cel_binary);path=w/'author'/stage;path.mkdir(parents=True)
        (path/'env.json').write_text(files['environment'])
        fixed=decode(files['fixed'].encode());cases=[{'testCase':c['case'],'bindings':c['inputs']|fixed,'expected':c['value']} for c in policy['cases'] if c['status']=='ok']
        (path/'cases.json').write_bytes(encode(cases))
        proc=command('author-'+stage,[a.author_binary,'eval','-env',path/'env.json','-expr',policy['expression'],'-tests',path/'cases.json'])
        observed=json.loads(proc.stdout);author.append({'stage':stage,'result':observed})
        check('author-'+stage,'Can existing authoring tools compile/evaluate this policy and literal expected outcomes?',proc.returncode==0 and all(x['status']=='pass' for x in observed['testResults']) and not observed.get('skippedTestCases'),observed)
    # Ordinary caller CLI is reused unchanged for the second declared contract.
    client=command('source-free-client',[sys.executable,'-B',LIFE/'caller.py','--host',a.host,'--repo',ci.repo,'--context',ci.contexts['caller'],'--runtime',a.cel_binary,'--work',ci.work,'--key','ci-client:ready-review','--function',decl['function'],'--date','2026-11-15','--inputs',json.dumps(decl['original_inputs'])])
    client_result=json.loads(client.stdout);check('unchanged-caller-client','Can the original generic client call a new declared function without source/tests/parameters?',client.returncode==0 and client_result['data']['result']['actual']['value'] is True,client_result)
    for name,driver in [('cel',ci),('ordinary',py)]:
        fsck=command('fsck-'+name,['git','--git-dir='+str(driver.repo),'fsck','--full','--no-dangling'])
        check('fsck-'+name,'Is the accepted multi-consumer/baseline history intact?',fsck.returncode==0,{'exit_code':fsck.returncode,'stderr':fsck.stderr})
        view=read_store(driver.repo)
        objects=[{'ref':ref,'media_type':mime,'body_utf8':raw_body.decode()} for ref,(mime,raw_body) in sorted(view['objects'].items())]
        (w/(name+'-store-export.json')).write_bytes(encode({'cursor':view['cursor'],'state':view['state'],'objects':objects}))
    pins={'original':original_pins,'cel':ci_pins,'ordinary':py_pins}
except Exception as e:errors.append(repr(e))
finally:
    # Retain partially completed driver traces as failures rather than discarding the run.
    for name,driver in [('original',locals().get('original')),('cel',locals().get('ci')),('ordinary',locals().get('py'))]:
        if driver:traces.setdefault(name,{'trace':driver.trace,'checks':driver.checks})
    result={'kind':'SecondConsumerReuse','status':'passed' if not errors and all(c['passed'] for c in checks) else 'failed','checks':checks,'errors':errors,'pins':pins,'traces':traces,
            'direct_operations':direct_operations,'measurements':{'observed_lifecycle_cli_operations':{k:len(v['trace']) for k,v in traces.items()},'recorded_tool_commands':len(commands),'direct_lifecycle_operations':len(direct_operations),'measurement_scope':'Driver CLI envelopes, explicit direct Host commands and recorded author/client/fsck commands only; internal Git/CEL subprocesses and build probes are not a total process count','model_trials':'not_run','human_time':'not_measured','latency':'not_benchmarked'},
            'limitations':['native Tekton/cluster not executed','ingress verification simulated, no pipeline launch','audit sidecar not atomic with Git receipt','ordinary baseline shares lifecycle; no lifecycle superiority claim','numeric/structured wire remains unsupported']}
    for name,value in [('results.json',result),('commands.json',commands)]: (w/name).write_bytes(encode(value))
    print(json.dumps({'status':result['status'],'checks':len(checks),'errors':errors}));raise SystemExit(result['status']!='passed')
