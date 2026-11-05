#!/usr/bin/env python3
"""Run the tier-eligibility consumer on the unchanged src/ reference. All generated state stays in --work."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT=Path(__file__).resolve().parent
SOURCE=ROOT.parents[1]
LIFE=SOURCE/'src/lifecycle-core/client'
PREPARE=SOURCE/'src/cel-handoff/tools/prepare.py'
sys.path.insert(0,str(LIFE))
from lifecycle_client import Driver,encode,decode,content_ref,envelope,MIMES

def fixture(name):return decode((ROOT/'fixtures'/name).read_bytes())

class TierDriver(Driver):
    """Reuse the generic Driver calls to the Go JSONL host; add the parts for this function."""
    def __init__(self,work,binary,spec,prepared,host):
        super().__init__(work,binary,spec,host);self.prepared=prepared
    def bundle(self,fixed):
        # The author files come from prepare.py. Only the fixed values change for each stage.
        data={key:(self.prepared/filename).read_bytes() for key,filename in {'manifest':'manifest.json','environment':'env.json','source':'rule.cel'}.items()}
        data['fixed']=encode(fixed);m=decode(data['manifest'])
        for key,field in [('environment','environment_ref'),('source','source_ref'),('fixed','fixed_ref')]:m[field]=content_ref(envelope(MIMES[key],data[key]))
        data['manifest']=encode(m)
        return {key:value.decode() for key,value in data.items()}
    def source_plan(self,stage,supersedes=()):
        table=self.fixture['stages'][stage];fun=self.fixture['function']
        old_sources=[s for s,_ in supersedes];old_plans=[p for _,p in supersedes]
        source=self.result(self.call('reviewer','source',name=stage+'-source',function=fun,**{'from':table['from'],'until':table['until']},
                                     text=table['source_text'],supersedes=old_sources,reason=stage+' tier table'))['source_ref']
        # The cases are the hand-written rows of the fixture, not the output of a candidate.
        plan=self.result(self.call('reviewer','plan',name=stage+'-plan',source_ref=source,cases=table['cases'],
                                   origin='Hand-written rows from the invented tier table',supersedes=old_plans))['plan_ref']
        return source,plan
    def propose(self,stage,source,base,fixed,name=None,expected='ok'):
        table=self.fixture['stages'][stage]
        return self.call('author','propose',name=name or stage+'-proposal',expected=expected,function=self.fixture['function'],
                         **{'from':table['from'],'until':table['until']},base_snapshot=base,source_ref=source,bundle=self.bundle(fixed),reason=stage+' proposal')
    def admit(self,snapshot,plan,prefix):
        report=self.result(self.call('runner','qualify',name=prefix+'-qualify',snapshot_ref=snapshot,plan_ref=plan))
        decision=self.result(self.call('reviewer','decide',name=prefix+'-decide',report_ref=report['report_ref'],reason='manual tier fixture approval'))['decision_ref']
        return report,decision

def lifecycle(d):
    f=d.fixture;fun=f['function'];st=f['stages'];exp=f['expected'];nov=f['original_date'];original=f['original_input']
    s1,p1=d.source_plan('initial')
    first=d.result(d.propose('initial',s1,'',st['initial']['fixed']));snap1=first['snapshot_ref']
    d.call('author','qualify',name='author-self-qualify',expected='forbidden',snapshot_ref=snap1,plan_ref=p1)
    q1,dec1=d.admit(snap1,p1,'initial')
    initial=d.result(d.call('reviewer','activate',name='initial-activate',decision_ref=dec1,targets=[]))['event_ref']
    described=d.call('caller','describe',name='october-discovery',function=fun,date='2026-10-15')['callable']
    d.check('caller-schema','Does discovery show only tier and trial, and hide the fixed values?',
            set(described['inputs']['properties'])=={'tier','trial'} and described['inputs']['additionalProperties'] is False,described)
    d.call('caller','invoke',name='october-before-load',expected='revision_not_loaded',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs=f['october_input'])
    d.call('worker','load',name='october-load',function=fun,date='2026-10-15')
    october=d.result(d.call('caller','invoke',name='october-gold-paid',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs=f['october_input']))
    override=d.result(d.call('caller','invoke',name='october-fixed-override',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs=f['october_input']|{'trial_allowed':True}))
    d.check('october-answer','Does the loaded initial rule give the stated October answer, and refuse a fixed-value override?',
            october['actual']['value'] is exp['october_gold_paid'] and q1['report']['status']=='passed' and override['actual']['status']=='invalid_input' and 'value' not in override['actual'],
            {'october':october,'override':override})
    history=d.cursor()
    s2,p2=d.source_plan('amendment',[(s1,p1)])
    # Failure case: a stale artifact that keeps the initial fixed values must fail the November cases.
    stale=d.result(d.propose('amendment',s2,snap1,st['initial']['fixed'],name='stale-artifact-proposal'))['snapshot_ref']
    bad=d.result(d.call('runner','qualify',name='stale-artifact-qualify',snapshot_ref=stale,plan_ref=p2))
    d.call('reviewer','decide',name='stale-artifact-decide',expected='qualification_failed',report_ref=bad['report_ref'],reason='stale artifact')
    d.check('stale-artifact-fails','Does a stale artifact fail the independent November cases and stay inactive?',bad['report']['status']=='failed',bad['report'])
    amendment=d.result(d.propose('amendment',s2,stale,st['amendment']['fixed']));snap2=amendment['snapshot_ref']
    q2,dec2=d.admit(snap2,p2,'amendment')
    future=d.result(d.call('reviewer','activate',name='amendment-activate',decision_ref=dec2,targets=[initial]))['event_ref']
    resolved={'october':d.call('reader','resolve',function=fun,date='2026-10-15')['snapshot_ref'],
              'november':d.call('reader','resolve',function=fun,date=nov)['snapshot_ref'],
              'november_at_history':d.call('reader','resolve',function=fun,date=nov,cursor=history)['snapshot_ref']}
    d.check('dated-selection','Does November select the amendment, while October and the earlier cursor keep the initial rule?',
            resolved=={'october':snap1,'november':snap2,'november_at_history':snap1},resolved)
    d.call('worker','load',name='november-load',function=fun,date=nov);d.call('worker','ack',name='november-ack',event_ref=future)
    before=d.result(d.call('caller','invoke',name='november-before-correction',function=fun,date=nov,expected_bundle_ref=amendment['bundle_ref'],inputs=original))
    d.check('november-answer','Is the November answer for a silver trial account recorded before the correction?',
            before['actual']['value'] is exp['november_silver_trial_before_correction'],before)
    task=d.result(d.call('observer','observe',name='report-trial-error',receipt_ref=before['receipt_ref'],route='source',
                         message='The November table allows trial accounts. The approved policy does not.'))
    claim=d.result(d.call('maintainer','claim',name='claim-correction',task_id=task['task_id'],checkpoint='0'))
    s3,p3=d.source_plan('correction',[(s2,p2)])
    d.call('reviewer','decide',name='old-report-after-correction',expected='stale_qualification',report_ref=q2['report_ref'],reason='source was corrected')
    corrected=d.result(d.propose('correction',s3,snap2,st['correction']['fixed']));snap3=corrected['snapshot_ref']
    q3,dec3=d.admit(snap3,p3,'correction')
    st3=st['correction']
    suspension=d.result(d.call('reviewer','suspend',name='suspend-amendment',function=fun,**{'from':st3['from'],'until':st3['until']},targets=[future],reason='stop the incorrect November table'))['event_ref']
    d.call('caller','describe',name='suspended-no-fallback',expected='out_of_scope',function=fun,date=nov)
    enabled=d.result(d.call('reviewer','activate',name='correction-activate',decision_ref=dec3,targets=[future,suspension]))['event_ref']
    correction_cursor=d.cursor()
    d.call('caller','invoke',name='stale-discovery',expected='artifact_mismatch',function=fun,date=nov,expected_bundle_ref=amendment['bundle_ref'],inputs=original)
    d.call('worker','load',name='correction-load',function=fun,date=nov);d.call('worker','ack',name='correction-ack',event_ref=enabled)
    after=d.result(d.call('caller','invoke',name='november-after-correction',function=fun,date=nov,expected_bundle_ref=corrected['bundle_ref'],inputs=original))
    replay=d.result(d.call('reader','replay',name='pinned-replay',receipt_ref=before['receipt_ref'],reason='Show why the earlier answer was true.'))
    restated=d.result(d.call('reader','restate',name='later-restatement',receipt_ref=before['receipt_ref'],cursor=correction_cursor,reason='Apply the corrected table to the original input.'))
    d.call('reader','replay',name='changed-replay-inputs',expected='input_mismatch',receipt_ref=before['receipt_ref'],inputs_json='{"tier":"gold","trial":true}',reason='an old receipt keeps its inputs')
    retained=d.call('reader','receipt',name='retained-old-receipt',receipt_ref=before['receipt_ref'])
    d.check('correction-replay-restatement','Does the correction keep the earlier answer and give a separate corrected answer?',
            after['actual']['value'] is exp['november_silver_trial_after_correction'] and replay['actual']['value'] is exp['pinned_replay']
            and restated['actual']['value'] is exp['later_restatement'] and retained==before['receipt']
            and replay['receipt']['selection']['snapshot_ref']==snap2 and restated['receipt']['selection']['snapshot_ref']==snap3,
            {'after':after,'replay':replay,'restatement':restated})
    done=d.result(d.call('maintainer','checkpoint',name='complete-correction',task_id=task['task_id'],checkpoint=claim['checkpoint'],token=claim['token'],
                         status='complete',note='Trial rule corrected; earlier answer replayed and restated.',correction_snapshot=snap3))
    d.check('correction-task','Is the correction task complete and tied to the correction snapshot?',done['status']=='complete' and done['correction_snapshot']==snap3,done)
    return {'initial_snapshot':snap1,'amendment_snapshot':snap2,'correction_snapshot':snap3,'stale_snapshot':stale,
            'old_receipt':before['receipt_ref'],'replay_receipt':replay['receipt_ref'],'restatement_receipt':restated['receipt_ref'],
            'historical_cursor':history,'correction_cursor':correction_cursor}

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work',type=Path,required=True);p.add_argument('--cel-binary',type=Path,required=True);p.add_argument('--author-binary',type=Path,required=True)
    p.add_argument('--host',type=Path,required=True,help='Go lifecycle-core executable')
    a=p.parse_args();w=a.work.resolve();binary=a.cel_binary.resolve()
    if w==SOURCE or SOURCE in w.parents:p.error('work must be outside the source tree')
    if w.exists():p.error('work directory exists; preserve previous runs')
    w.mkdir(parents=True);(w/'tmp').mkdir();os.environ['TMPDIR']=str(w/'tmp');os.environ['PYTHONDONTWRITEBYTECODE']='1'
    for key in list(os.environ):
        if key.startswith('GIT_'):os.environ.pop(key)
    checks=[];commands=[];errors=[];pins={};driver=None
    def check(name,question,passed,actual):
        checks.append({'name':name,'question':question,'passed':bool(passed),'actual':actual})
        if not passed:raise AssertionError(name)
    def command(name,args,data=None):
        proc=subprocess.run([str(x) for x in args],input=data,capture_output=True,text=True)
        commands.append({'name':name,'args':[str(x) for x in args],'exit_code':proc.returncode,'stdout':proc.stdout,'stderr':proc.stderr});return proc
    try:
        callable_spec=fixture('callable.json');life=fixture('lifecycle.json');bundle=w/'bundle'
        # 1. Author: write the bundle with the unchanged prepare.py and this example's fixture.
        prep=command('prepare',[sys.executable,'-B',PREPARE,'--bundle',bundle,'--binary',binary,'--fixture',ROOT/'fixtures/callable.json'])
        check('prepare','Does the unchanged prepare.py write a bundle from a new fixture?',prep.returncode==0,{'exit_code':prep.returncode,'stderr':prep.stderr})
        # 2. Author: the pinned upstream CEL tool evaluates the rule against the hand-written ok cases.
        evaluated=command('author-eval',[a.author_binary,'eval','-env',bundle/'env.json','-expr',callable_spec['expression'],'-tests',bundle/'upstream-tests.json'])
        result=json.loads(evaluated.stdout or '{}')
        check('author-eval','Do the upstream CEL tools compile the rule and agree with the stated ok cases?',
              evaluated.returncode==0 and result.get('testResults') and all(x['status']=='pass' for x in result['testResults']) and not result.get('skippedTestCases'),result)
        # 3. Host: discovery and the host-owned cases, with refusals beside the ok answers.
        described=json.loads(command('describe',[binary,'-bundle',bundle,'describe']).stdout)
        check('host-describe','Does the host give a caller schema with tier and trial only?',described.get('function_id')==callable_spec['function_id']
              and described['inputs']['required']==callable_spec['caller_variables'] and set(described['inputs']['properties'])=={'tier','trial'},described)
        report=json.loads(command('host-check',[binary,'-bundle',bundle,'-cases',bundle/'cases.json','check']).stdout)
        check('host-cases','Does the host agree with each hand-written case, including the refusals?',report.get('status')=='passed' and len(report['cases'])==len(callable_spec['expected_calls']),report)
        stale=json.loads(command('stale-bundle-ref',[binary,'-bundle',bundle,'run'],encode({'bundle_ref':'sha256:00','inputs':{'tier':'gold','trial':False}}).decode()).stdout)
        check('host-stale-reference','Does the host refuse a call that names another bundle?',stale.get('status')=='artifact_mismatch' and 'value' not in stale,stale)
        # 4. Lifecycle: approval, dated activation, answer, correction, replay and restatement through the unchanged JSONL CLI.
        driver=TierDriver(w/'life',binary,life,bundle,a.host.resolve());pins=lifecycle(driver)
        # 5. Caller: the unchanged generic client calls the new function after the correction.
        client=command('client',[sys.executable,'-B',LIFE/'caller.py','--host',a.host,'--repo',driver.repo,'--context',driver.contexts['caller'],'--runtime',binary,'--work',driver.work,
                                 '--key','tier-client','--function',life['function'],'--date',life['original_date'],'--inputs',json.dumps(life['original_input'])])
        called=json.loads(client.stdout or '{}')
        check('unchanged-client','Does the unchanged caller.py call the new function and get the corrected answer?',
              client.returncode==0 and called['data']['result']['actual']['value'] is life['expected']['november_silver_trial_after_correction'],called)
        fsck=command('fsck',['git','--git-dir='+str(driver.repo),'fsck','--full','--no-dangling'])
        check('fsck','Is the lifecycle Git history intact?',fsck.returncode==0,{'exit_code':fsck.returncode,'stderr':fsck.stderr})
    except Exception as e:errors.append(repr(e))
    finally:
        # Keep a partial trace as a failure; do not discard the run.
        trace={'trace':driver.trace,'checks':driver.checks} if driver else {}
        status='passed' if not errors and all(c['passed'] for c in checks) and all(c['passed'] for c in trace.get('checks',[])) else 'failed'
        result={'kind':'TierEligibilityConsumer','status':status,'checks':checks,'lifecycle':trace,'pins':pins,'errors':errors,
                'limitations':['invented policy; no real customer data','string/bool inputs and a bool result only','local Git store; host context files are trusted']}
        for name,value in [('results.json',result),('commands.json',commands)]:(w/name).write_bytes(encode(value))
        print(json.dumps({'status':status,'checks':len(checks)+len(trace.get('checks',[])),'errors':errors}));raise SystemExit(status!='passed')
