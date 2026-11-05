#!/usr/bin/env python3
"""Build public pinned tools and run this source-only reference in an external work directory."""
import argparse
import gzip
import hashlib
import json
import os
import re
import shutil
from pathlib import Path
import subprocess
import sys
import time

ROOT=Path(__file__).resolve().parent
PIN='5eac36c53cd8971fa1e5548374238183e0d8b302'
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--work',type=Path,required=True)
p.add_argument('--skills',type=Path,help='optional clean public author-tools checkout at the pinned revision')
a=p.parse_args();w=a.work.resolve()
if w==ROOT or ROOT in w.parents:p.error('work must be outside this source tree')
if w.exists():p.error('%s already exists. Choose a path that does not exist, for example --work "$(mktemp -d)/run". An earlier run is not overwritten.'%w)
for name in ['runs','bin','tmp','cache/build','cache/mod','cache/gopath','cache/telemetry']:(w/name).mkdir(parents=True,exist_ok=True)
(w/'cache/telemetry/mode').write_text('off\n')
env={k:v for k,v in os.environ.items() if not k.startswith('GIT_')}|{
 'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':os.devnull,'GIT_TERMINAL_PROMPT':'0',
 'GOCACHE':str(w/'cache/build'),'GOMODCACHE':str(w/'cache/mod'),'GOPATH':str(w/'cache/gopath'),
 'GOTMPDIR':str(w/'tmp'),'TMPDIR':str(w/'tmp'),'TEST_TELEMETRY_DIR':str(w/'cache/telemetry'),
 'GOENV':'off','GOWORK':'off','GOFLAGS':'','GOTOOLCHAIN':'local','PYTHONDONTWRITEBYTECODE':'1',
 'LIFECYCLE_CLIENT_SOURCE':str(ROOT/'src/lifecycle-core/client'),'LIFECYCLE_HOST':str(w/'bin/lifecycle-core'),
 'LIFECYCLE_CEL_BINARY':str(w/'bin/cel-handoff'),
 'LIFECYCLE_CEL_BUNDLE':str(w/'record-bundle'),'LIFECYCLE_FIXTURE':str(ROOT/'src/cel-lifecycle/fixtures/lifecycle-scenario.json'),
 'EXPORT_TEST_WORK':str(w/'boundaries'),'PROTOCOL_TEST_WORK':str(w/'protocol'),'QUICKSTART_TEST_WORK':str(w/'quickstart-failures'),'DECISION_EXPORT_SOURCE':str(ROOT)}
for key in list(os.environ):
 if key.startswith('GIT_'):os.environ.pop(key)
os.environ.update(env)
commands=[];checks=[];start=time.monotonic()
def inventory():
 return {str(p.relative_to(ROOT)):hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(ROOT.rglob('*')) if p.is_file()}
def write(path,value):path.write_text(json.dumps(value,indent=2,ensure_ascii=False)+'\n')
def run(name,args,cwd=ROOT,extra=None):
 # Each stage prints one progress line to stderr. Stdout keeps only the final summary JSON.
 print('verify: %4ds start %s'%(time.monotonic()-start,name),file=sys.stderr,flush=True)
 result=subprocess.run([str(x) for x in args],cwd=cwd,env=env|(extra or {}),capture_output=True,text=True)
 record={'name':name,'args':[str(x) for x in args],'cwd':str(cwd),'env':extra or {},'exit_code':result.returncode,'stdout':result.stdout,'stderr':result.stderr}
 commands.append(record);write(w/'runs'/(name+'.json'),record);return result
def check(name,question,passed,actual):
 checks.append({'name':name,'question':question,'passed':bool(passed),'actual':actual})
 if not passed:raise AssertionError(name)
before=inventory()
try:
 expected=json.loads((ROOT/'checksums.json').read_text())
 check('source-inventory','Do all reviewed source bytes match the candidate inventory?',all(before.get(k)==v for k,v in expected.items()),{'checked_files':len(expected)})
 project=run('project-layout',[sys.executable,'-B',ROOT/'tools/check_project.py'])
 check('project-layout','Do the shared harness layout, links and source inventory validate?',project.returncode==0,{'exit_code':project.returncode,'stdout':project.stdout})
 version=run('go-storage',['go','env','-json','GOCACHE','GOMODCACHE','GOPATH','GOTMPDIR','GOTELEMETRYDIR','GOTELEMETRY','GOENV','GOWORK','GOFLAGS'])
 observed=json.loads(version.stdout);required={k:env[k] for k in ['GOCACHE','GOMODCACHE','GOPATH','GOTMPDIR','GOWORK','GOFLAGS']};required|={'GOENV':'','GOTELEMETRY':'off','GOTELEMETRYDIR':str(w/'cache/telemetry')}
 check('storage','Are build/cache/temp/config/telemetry paths controlled?',version.returncode==0 and observed==required,observed)
 skills=a.skills.resolve() if a.skills else w/'tools/skills'
 if not a.skills:
  clone=run('fetch-author-tools',['git','clone','--no-checkout','https://github.com/cel-expr/skills.git',skills])
  check('fetch-author-tools','Can author tools be fetched from their public source?',clone.returncode==0,{'exit_code':clone.returncode})
  checkout=run('pin-author-tools',['git','checkout','--detach',PIN],skills)
  check('checkout-author-tools','Does the public author checkout resolve the chosen revision?',checkout.returncode==0,{'exit_code':checkout.returncode})
 pin=run('author-revision',['git','rev-parse','HEAD'],skills);clean=run('author-clean',['git','status','--porcelain'],skills)
 check('author-source','Is the author source clean and pinned?',pin.stdout.strip()==PIN and clean.returncode==0 and not clean.stdout,{'revision':pin.stdout.strip(),'status':clean.stdout})
 build=run('build-host',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',w/'bin/cel-handoff','./cmd/cel-handoff'],ROOT/'src/cel-handoff')
 check('build-host','Does the exported host build from its lockfiles?',build.returncode==0,{'exit_code':build.returncode})
 author=run('build-author',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',w/'bin/cel-expr','./cmd/cel-expr'],skills)
 check('build-author','Do the pinned author tools build independently?',author.returncode==0,{'exit_code':author.returncode})
 identity=run('host-identity',[w/'bin/cel-handoff','-build']);host_build=json.loads(identity.stdout)
 deps={x['Path']:x['Version'] for x in host_build['build']['Deps']}
 check('engine-pins','Does the actual host use the pinned CEL engine/expressions?',deps.get('cel.dev/cel-go')=='v0.32.0' and deps.get('cel.dev/expr')=='v0.25.3',host_build)
 (w/'executor').mkdir(parents=True,exist_ok=True)
 go_tests=run('go-tests',['go','test','-mod=readonly','-count=1','-json','./...'],ROOT/'src/cel-handoff',{'EXECUTOR_VECTOR_RESULTS':str(w/'executor/native.json')})
 check('go-contracts','Do the exported Go contract tests pass?',go_tests.returncode==0,{'exit_code':go_tests.returncode})
 vet=run('go-vet',['go','vet','-mod=readonly','./...'],ROOT/'src/cel-handoff')
 check('go-vet','Does Go static analysis pass?',vet.returncode==0,{'exit_code':vet.returncode})
 prepared=run('prepare-record',[sys.executable,'-B',ROOT/'src/cel-handoff/tools/prepare.py','--bundle',w/'record-bundle','--binary',w/'bin/cel-handoff'])
 check('prepare-record','Can a fresh bundle bind the exported build identity?',prepared.returncode==0,{'exit_code':prepared.returncode})
 syntax=run('python-syntax',[sys.executable,'-B','-c','import pathlib; [compile(p.read_bytes(),str(p),"exec") for p in pathlib.Path(".").rglob("*.py")]'])
 check('syntax','Are the exported Python sources syntactically valid?',syntax.returncode==0,{'exit_code':syntax.returncode})
 core=ROOT/'src/lifecycle-core'
 # The export boundary check and the examples use the Go JSONL host, so build it first.
 go_core=[run('go-core-build',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',w/'bin/lifecycle-core','./cmd/lifecycle-core'],core)]
 boundaries=run('export-boundaries',[sys.executable,'-B',ROOT/'tests/test_export_boundaries.py'])
 boundary_path=w/'boundaries/test-results.json'
 boundary_results=json.loads(boundary_path.read_text()) if boundary_path.exists() else {'successful':False,'error':'test_export_boundaries.py wrote no test-results.json','stderr':boundaries.stderr}
 check('export-boundaries','Do oversized stream and audit-identity regressions pass?',boundaries.returncode==0 and boundary_results['successful'],boundary_results)
 # The Go tests run the lifecycle scenario, the storage port vectors and the Host checks.
 # LIFECYCLE_REQUIRE_SCENARIO makes a skipped scenario a failure. The scenario writes its checks and pins to the results file.
 (w/'go-core').mkdir(parents=True,exist_ok=True);scenario_path=w/'go-core/scenario-results.json'
 env.update(LIFECYCLE_REQUIRE_SCENARIO='1',LIFECYCLE_SCENARIO_RESULTS=str(scenario_path))
 go_core.append(run('go-core-tests',['go','test','-mod=readonly','-count=1','./...'],core))
 go_core.append(run('go-core-vet',['go','vet','-mod=readonly','./...'],core))
 scenario=json.loads(scenario_path.read_text()) if scenario_path.exists() else {'status':'failed','error':'the Go scenario wrote no results'}
 check('go-lifecycle-core','Does the Go lifecycle core build, pass the scenario with the fixture values, and pass its tests and vet?',
       scenario.get('status')=='passed' and bool(scenario.get('checks')) and all(x['passed'] for x in scenario['checks']) and len(go_core)==3 and all(x.returncode==0 for x in go_core),
       {'stages':{c['name']:c['exit_code'] for c in commands if c['name'].startswith('go-core-')},
        'scenario':{k:scenario.get(k) for k in ['status','error','steps']}|{'checks':[x['name'] for x in scenario.get('checks',[]) if x.get('passed')]}})
 # The browser executor (decision 0002) is the same command built for js/wasm. In a browser the module serves the
 # JavaScript bridge; under Node with arguments it serves the command line, so the Host can qualify with the exact module.
 # Qualification is for each build: the WASM build has its own implementation_ref.
 handoff=ROOT/'src/cel-handoff';node=shutil.which('node');goroot=run('go-root',['go','env','GOROOT']).stdout.strip()
 node_version=run('node-version',[node,'--version']).stdout.strip() if node else None
 check('node','Is Node available to run the WebAssembly module?',node is not None and goroot!='',{'node':node,'version':node_version,'goroot':goroot})
 wasm_env={'GOOS':'js','GOARCH':'wasm'};lib=Path(goroot)/'lib/wasm';module=w/'bin/cel-handoff.wasm'
 wasm_stages=[run('build-wasm',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',module,'./cmd/cel-handoff'],handoff,wasm_env)]
 # The Go launcher copies the whole environment into the module, and the module has a size limit for arguments and
 # environment. Thus each wrapper starts Node with an empty environment. The test wrapper keeps only the results path.
 wrapper=w/'bin/cel-handoff-wasm';wrapper.write_text('#!/bin/sh\nexec /usr/bin/env -i "%s" "%s" "%s" "$@"\n'%(node,lib/'wasm_exec_node.js',module));wrapper.chmod(0o755)
 test_exec=w/'bin/go-wasm-exec';test_exec.write_text('#!/bin/sh\nexec /usr/bin/env -i EXECUTOR_VECTOR_RESULTS="$EXECUTOR_VECTOR_RESULTS" "%s" "%s" "$@"\n'%(node,lib/'wasm_exec_node.js'));test_exec.chmod(0o755)
 wasm_identity=run('wasm-identity',[wrapper,'-build']);wasm_build=json.loads(wasm_identity.stdout) if wasm_identity.returncode==0 else {}
 wasm_deps={x['Path']:x['Version'] for x in wasm_build.get('build',{}).get('Deps',[])}
 check('wasm-identity','Does the WASM module use the pinned CEL engine and have its own build identity?',
       wasm_stages[0].returncode==0 and wasm_deps.get('cel.dev/cel-go')=='v0.32.0' and wasm_deps.get('cel.dev/expr')=='v0.25.3'
       and wasm_build.get('implementation_ref','') not in ('',host_build['implementation_ref']),
       {'implementation_ref':wasm_build.get('implementation_ref'),'native_implementation_ref':host_build['implementation_ref'],'deps':wasm_deps})
 wasm_stages.append(run('go-tests-wasm',['go','test','-mod=readonly','-count=1','-exec',test_exec,'.'],handoff,wasm_env|{'EXECUTOR_VECTOR_RESULTS':str(w/'executor/wasm-test.json')}))
 wasm_stages.append(run('go-vet-wasm',['go','vet','-mod=readonly','./...'],handoff,wasm_env))
 wasm_stages.append(run('bridge-vectors',[node,handoff/'web/node-check.mjs',module,lib/'wasm_exec.js',ROOT/'src/cel-lifecycle/conformance/executor',w/'executor/bridge.json']))
 def executor_results(name):
  path=w/'executor'/name
  if not path.exists():return None,None
  d=json.loads(path.read_text());text=json.dumps(d['vectors'],sort_keys=True).replace(d['implementation_ref'],'<build>')
  text=re.sub(r'"bundle_ref": "sha256:[0-9a-f]{64}"','"bundle_ref": "<bundle>"',text)
  return d,[{k:v for k,v in r.items() if v is not None} for x in json.loads(text).values() for r in x]
 native,native_steps=executor_results('native.json');test,test_steps=executor_results('wasm-test.json');bridge,bridge_steps=executor_results('bridge.json')
 raw=module.read_bytes() if module.exists() else b''
 size={'module_bytes':len(raw),'gzip_9_bytes':len(gzip.compress(raw,9,mtime=0)),'module_sha256':hashlib.sha256(raw).hexdigest(),
       'node_cold_start_ms':(bridge or {}).get('host',{}).get('cold_start_ms'),'node_version':node_version}
 check('wasm-executor','Do the executor vectors pass on the native build, as WASM under Node and through the bridge, with the same entries except the build and bundle identities?',
       all(x.returncode==0 for x in wasm_stages) and go_tests.returncode==0 and native_steps is not None and bool(native_steps)
       and native_steps==test_steps==bridge_steps and bridge['errors']==[] and bridge['implementation_ref']==wasm_build.get('implementation_ref'),
       {'stages':{c['name']:c['exit_code'] for c in commands if c['name'] in ('build-wasm','go-tests-wasm','go-vet-wasm','bridge-vectors')},
        'steps':len(native_steps or []),'bridge_errors':(bridge or {}).get('errors'),'measurements':size})
 # The Go lifecycle scenario again, with the WASM module as the engine. Each snapshot then pins the WASM build.
 wasm_bundle=w/'record-bundle-wasm';wasm_scenario=w/'go-core-wasm/scenario-results.json';wasm_scenario.parent.mkdir(parents=True,exist_ok=True)
 wasm_prepared=run('prepare-record-wasm',[sys.executable,'-B',handoff/'tools/prepare.py','--bundle',wasm_bundle,'--binary',wrapper])
 wasm_core=run('go-core-scenario-wasm',['go','test','-mod=readonly','-count=1','-run','^TestLifecycleScenario$','.'],core,
               {'LIFECYCLE_CEL_BINARY':str(wrapper),'LIFECYCLE_CEL_BUNDLE':str(wasm_bundle),'LIFECYCLE_SCENARIO_RESULTS':str(wasm_scenario)})
 wasm_result=json.loads(wasm_scenario.read_text()) if wasm_scenario.exists() else {'status':'failed','error':'the WASM scenario wrote no results'}
 wasm_manifest=json.loads((wasm_bundle/'manifest.json').read_text()) if (wasm_bundle/'manifest.json').exists() else {}
 check('wasm-qualification','Does the Host qualify, activate, invoke, replay and restate with the WASM module as the engine?',
       wasm_prepared.returncode==0 and wasm_core.returncode==0 and wasm_result.get('status')=='passed' and bool(wasm_result.get('checks'))
       and all(x['passed'] for x in wasm_result['checks']) and wasm_manifest.get('implementation_ref')==wasm_build.get('implementation_ref'),
       {'exit_code':wasm_core.returncode,'status':wasm_result.get('status'),'error':wasm_result.get('error'),'steps':wasm_result.get('steps'),
        'bundle_implementation_ref':wasm_manifest.get('implementation_ref'),'checks':[x['name'] for x in wasm_result.get('checks',[]) if x.get('passed')]})
 # The Go sidecar runs the session vector files through its Unix socket. Its stub Host and schema table tests are in go-core-tests.
 sidecar=[run('build-sidecar',['go','build','-mod=readonly','-trimpath','-buildvcs=false','-o',w/'bin/entelos-sidecar','./cmd/entelos-sidecar'],core)]
 if sidecar[0].returncode==0:
  sidecar.append(run('sidecar-vectors',[sys.executable,'-B',ROOT/'src/protocol/test_protocol.py','--sidecar',w/'bin/entelos-sidecar','--runtime',w/'bin/cel-handoff','--work',w/'sidecar-protocol']))
 sidecar_path=w/'sidecar-protocol/test-results.json'
 sidecar_results=json.loads(sidecar_path.read_text()) if sidecar_path.exists() else {'successful':False,'error':'no sidecar test-results.json','stages':{c['name']:c['exit_code'] for c in commands[-len(sidecar):]},'stderr':sidecar[-1].stderr}
 session_files={p.name:len(json.loads(p.read_text())['steps']) for p in sorted((ROOT/'src/protocol/vectors').glob('session-*.json'))}
 check('sidecar-protocol','Does the Go sidecar pass every session vector of the wire protocol through its Unix socket?',
       len(sidecar)==2 and all(x.returncode==0 for x in sidecar) and sidecar_results['successful']
       and {f['file']:f['steps'] for f in sidecar_results.get('files',[]) if f['errors']==0}==session_files,
       {'expected_files':session_files,'results':sidecar_results})
 client=run('sidecar-client',[sys.executable,'-B',ROOT/'examples/sidecar-client/run.py','--work',w/'sidecar-client','--sidecar',w/'bin/entelos-sidecar','--cel-binary',w/'bin/cel-handoff','--record-bundle',w/'record-bundle'])
 client_path=w/'sidecar-client/results.json'
 client_results=json.loads(client_path.read_text()) if client_path.exists() else {'status':'failed','error':'run.py wrote no results.json','stderr':client.stderr}
 check('sidecar-client','Does a client that talks only to the sidecar socket complete propose, qualify, decide, activate and invoke?',
       client.returncode==0 and client_results['status']=='passed',{k:client_results.get(k) for k in ['status','error','checks']})
 demo=run('consumer-reuse',[sys.executable,'-B',ROOT/'examples/ci-trigger/run.py','--work',w/'reuse','--cel-binary',w/'bin/cel-handoff','--author-binary',w/'bin/cel-expr','--record-bundle',w/'record-bundle','--host',w/'bin/lifecycle-core'])
 reuse=json.loads((w/'reuse/results.json').read_text())
 check('two-consumers-and-baseline','Do both consumer loops and the ordinary-code comparison pass?',demo.returncode==0 and reuse['status']=='passed',{'status':reuse['status'],'checks':reuse['checks'],'measurements':reuse['measurements'],'errors':reuse['errors']})
 tier=run('tier-eligibility',[sys.executable,'-B',ROOT/'examples/tier-eligibility/run.py','--work',w/'tier','--cel-binary',w/'bin/cel-handoff','--author-binary',w/'bin/cel-expr','--host',w/'bin/lifecycle-core'])
 tier_path=w/'tier/results.json';tier_results=json.loads(tier_path.read_text()) if tier_path.exists() else {'status':'failed','errors':['run.py wrote no results.json'],'stderr':tier.stderr}
 check('tier-eligibility','Does the third consumer pass its hand-written cases, correction, replay and restatement?',tier.returncode==0 and tier_results['status']=='passed',{'status':tier_results['status'],'errors':tier_results.get('errors')})
 failures=run('quickstart-failures',[sys.executable,'-B',ROOT/'examples/quickstart/test_demo.py'])
 failure_path=w/'quickstart-failures/test-results.json'
 failure_results=json.loads(failure_path.read_text()) if failure_path.exists() else {'successful':False,'error':'test_demo.py wrote no test-results.json','exit_code':failures.returncode,'stderr':failures.stderr}
 check('quickstart-failures','Does each demo failure path write summary.json with a named reason?',failures.returncode==0 and failure_results['successful'],failure_results)
 quick=run('quickstart',[sys.executable,'-B',ROOT/'examples/quickstart/demo.py','--work',w/'quickstart'])
 expected_loop=json.loads(Path(env['LIFECYCLE_FIXTURE']).read_text())['expected']
 summary=json.loads((w/'quickstart/summary.json').read_text()) if (w/'quickstart/summary.json').exists() else {}
 check('quickstart','Does the one-command demo build the verified host identity and reach the fixture answer, replay and restatement?',
       quick.returncode==0 and summary.get('status')=='passed' and summary.get('implementation_ref')==host_build['implementation_ref']
       and summary.get('answer',{}).get('value') is expected_loop['future_november_reviewed']
       and summary.get('replay',{}).get('value') is expected_loop['pinned_replay'] and summary.get('restatement',{}).get('value') is expected_loop['later_restatement']
       and summary['replay']['snapshot_ref']==summary['answer']['snapshot_ref'] and summary['restatement']['snapshot_ref']==summary['correction']['snapshot_ref'],
       {'exit_code':quick.returncode,'stdout':quick.stdout,'stderr':quick.stderr,'expected':expected_loop,'summary':summary})
 check('source-unchanged','Did verification leave every candidate byte unchanged?',inventory()==before,{'unchanged':inventory()==before})
except Exception as error:checks.append({'name':'runner-completion','question':'Did standalone verification finish?','passed':False,'actual':repr(error)})
finally:
 write(w/'commands.json',commands)
 result={'kind':'StandaloneDecisionReferenceAcceptance','checks':checks,'summary':{'passed':sum(x['passed'] for x in checks),'failed':sum(not x['passed'] for x in checks)},
         'candidate_before':before,'candidate_after':inventory(),'python_version':sys.version,
         'environment':{k:env[k] for k in ['TMPDIR','GOCACHE','GOMODCACHE','GOPATH','GOTMPDIR','GOENV','GOWORK','GOTOOLCHAIN','TEST_TELEMETRY_DIR']},
         'limits':'Local reference checks only; no model/performance/deployment benchmark.'}
 write(w/'results.json',result);print(json.dumps(result['summary']));raise SystemExit(bool(result['summary']['failed']))
