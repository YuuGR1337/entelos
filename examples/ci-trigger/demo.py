#!/usr/bin/env python3
"""Application workflow orchestration with unchanged lifecycle commands to the Go JSONL host."""
import json
from pathlib import Path
import shutil
import subprocess
from support import ROOT,Driver,READS,encode,decode,content_ref,envelope,MIMES,fixture
from adapter import Ingress,project,link,verify_link,build_ref as projection_build
from ordinary import build_ref as ordinary_build

class CIDriver(Driver):
    """The CI consumer through the Go JSONL host. runtime is the CEL executable or the ordinary runtime executable."""
    def __init__(self,work,runtime,backend,host,seed=None):
        self.spec=fixture();self.backend=backend;self.namespace='ci-'+backend
        super().__init__(work,runtime,{'function':self.spec['function'],'scope':self.spec['scope']},host)
        if seed:shutil.copytree(seed,self.repo)
        for path in self.contexts.values():
            context=decode(path.read_bytes());context['worker']=self.spec['worker'];path.write_bytes(encode(context))
    def call(self,actor,op,name=None,expected='ok',**values):
        if op not in READS:values.setdefault('request_key',self.namespace+':'+(name or op+'-'+str(self.counter+1)))
        return super().call(actor,op,name,expected,**values)
    def result(self,value):return value['result']
    def bundle(self,stage,binary,wrong=False):
        policy=self.spec['policies'][stage]
        if self.backend=='cel':
            build=decode(subprocess.check_output([str(binary),'-build']))['implementation_ref'];profile='cel-string-bool/v1'
            source=self.spec['policies']['initial' if wrong else stage]['expression']+'\n'
        else:build=ordinary_build();profile='ordinary-ci/v1';source='ordinary:'+('initial' if wrong else stage)+'\n'
        files={'environment':encode(self.spec['environment']),'source':source.encode(),'fixed':encode({'target_branch':policy['target_branch']})}
        manifest={'function_id':self.spec['function'],'profile':profile,'caller_variables':self.spec['caller_variables'],'implementation_ref':build,
                  'limits':{'max_request_bytes':4096,'max_string_utf8_bytes':256,'max_source_utf8_bytes':4096,'parser_recursion_limit':64,'evaluation_cost_limit':1000}}
        for name,field in [('environment','environment_ref'),('source','source_ref'),('fixed','fixed_ref')]:manifest[field]=content_ref(envelope(MIMES[name],files[name]))
        files['manifest']=encode(manifest)
        return {k:v.decode() for k,v in files.items()}
    def source_plan(self,stage,source_supersedes=None,plan_supersedes=None):
        policy=self.spec['policies'][stage]
        q={'function':self.spec['function'],'from':policy['from'],'until':policy['until'],'text':policy['source_text']+' Projection implementation: '+projection_build(),
           'supersedes':source_supersedes or [],'reason':'Manual neutral '+stage+' policy'}
        source=self.result(self.call('reviewer','source',name=stage+'-source',**q))['source_ref']
        plan=self.result(self.call('reviewer','plan',name=stage+'-plan',source_ref=source,cases=policy['cases'],origin='Literal expected rows from approved neutral CI policy table',supersedes=plan_supersedes or []))['plan_ref']
        return source,plan
    def propose(self,stage,source,base,binary,name=None,wrong=False,expected='ok'):
        policy=self.spec['policies'][stage]
        return self.call('author','propose',name=name or stage+'-proposal',expected=expected,function=self.spec['function'],
                         **{'from':policy['from'],'until':policy['until']},base_snapshot=base,source_ref=source,bundle=self.bundle(stage,binary,wrong),reason=stage+' consumer proposal')
    def qualified(self,snapshot,plan,prefix):
        report=self.result(self.call('runner','qualify',name=prefix+'-qualify',snapshot_ref=snapshot,plan_ref=plan))
        decision=self.result(self.call('reviewer','decide',name=prefix+'-decide',report_ref=report['report_ref'],reason='manual CI fixture approval'))['decision_ref']
        return report,decision

def demonstrate(driver,binary,seeded=False):
    d=driver;spec=d.spec;fun=spec['function'];raw=(ROOT/'fixtures/ready-review-envelope.json').read_bytes();ingress=Ingress(True,spec['repository'])
    projected=project(raw,ingress)
    d.check('nested-payload-projection','What domain transport work is needed before the primitive callable?',projected==spec['original_inputs'],projected)
    if seeded:
        policy=spec['policies']['initial']
        d.call('reviewer','source',name='request-namespace-probe',expected='idempotency_conflict',request_key='initial-source',function=fun,**{'from':policy['from'],'until':policy['until']},text=policy['source_text'],supersedes=[],reason='probe a key already used by the first consumer')
    s1,p1=d.source_plan('initial');first=d.result(d.propose('initial',s1,'',binary));snap1=first['snapshot_ref']
    q1,decision=d.qualified(snap1,p1,'initial');event1=d.result(d.call('reviewer','activate',name='initial-activate',decision_ref=decision,targets=[]))['event_ref']
    desc=d.call('caller','describe',name='typed-discovery',function=fun,date='2026-10-15')
    properties=desc['callable']['inputs']['properties']
    d.check('different-typed-contract','Do new declarations drive discovery without a copied type compiler?',set(properties)==set(spec['caller_variables']) and 'target_branch' not in properties and desc['callable']['inputs']['additionalProperties'] is False,desc)
    d.call('caller','invoke',name='before-load',expected='revision_not_loaded',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs={'action':'opened','base_branch':'main','draft':False})
    d.call('worker','load',name='initial-load',function=fun,date='2026-10-15')
    first_call=d.result(d.call('caller','invoke',name='initial-call',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs={'action':'opened','base_branch':'main','draft':False}))
    invalid=d.result(d.call('caller','invoke',name='fixed-input-override',function=fun,date='2026-10-15',expected_bundle_ref=first['bundle_ref'],inputs={'action':'opened','base_branch':'other','draft':False,'target_branch':'other'}))
    d.check('strict-call-boundary','Does the second consumer retain successful true and fixed-data protection?',first_call['actual']['value'] is True and invalid['actual']['status']=='invalid_input' and 'value' not in invalid['actual'],{'valid':first_call,'invalid':invalid})
    history=d.cursor()
    s2,p2=d.source_plan('amendment',[s1],[p1]);wrong=d.result(d.propose('amendment',s2,snap1,binary,name='wrong-formula',wrong=True))['snapshot_ref']
    failed=d.result(d.call('runner','qualify',name='wrong-formula-qualify',snapshot_ref=wrong,plan_ref=p2))
    d.call('reviewer','decide',name='wrong-formula-rejected',expected='qualification_failed',report_ref=failed['report_ref'],reason='candidate omitted a required action')
    d.check('independent-formula-failure','Can source-supplied cases reject a valid but wrong program?',failed['report']['status']=='failed',failed)
    d.propose('amendment',s2,snap1,binary,name='stale-proposal',expected='stale_proposal')
    future=d.result(d.propose('amendment',s2,wrong,binary));snap2=future['snapshot_ref'];q2,decision=d.qualified(snap2,p2,'amendment')
    d.call('reviewer','activate',name='overlap-without-mask',expected='ambiguous_activation',decision_ref=decision,targets=[])
    update=d.call('reviewer','activate',name='future-activate',decision_ref=decision,targets=[event1]);event2=update['result']['event_ref']
    later=d.call('reader','resolve',function=fun,date='2026-11-15');earlier=d.call('reader','resolve',function=fun,date='2026-10-15');old=d.call('reader','resolve',function=fun,date='2026-11-15',cursor=history)
    d.check('future-current-history','Do the existing masks and historical reads work for a new function?',later['snapshot_ref']==snap2 and earlier['snapshot_ref']==snap1 and old['snapshot_ref']==snap1,{'future':later,'earlier':earlier,'historical':old})
    d.call('caller','invoke',name='future-unloaded',expected='revision_not_loaded',function=fun,date='2026-11-15',expected_bundle_ref=future['bundle_ref'],inputs=projected)
    d.call('worker','load',name='future-load',function=fun,date='2026-11-15');d.call('worker','ack',name='future-ack',event_ref=event2)
    old_call=d.result(d.call('caller','invoke',name='missed-ready-review',function=fun,date='2026-11-15',expected_bundle_ref=future['bundle_ref'],inputs=projected))
    audit=link(raw,ingress,old_call,d.work/'audit');reprojected=verify_link(audit,d.work/'audit',ingress)
    d.check('application-audit-link','Which raw-envelope/projection evidence stays outside generic receipts?',reprojected==projected and audit['audit']['receipt_ref']==old_call['receipt_ref'],audit)
    observation=d.result(d.call('observer','observe',name='missing-run-observation',receipt_ref=old_call['receipt_ref'],route='source',message='Verified PR left draft but approved policy did not include ready_for_review.'))
    task=d.result(d.call('maintainer','claim',name='claim-source-correction',task_id=observation['task_id'],checkpoint='0'))
    saved=d.result(d.call('maintainer','checkpoint',name='save-correction',task_id=observation['task_id'],checkpoint=task['checkpoint'],token=task['token'],status='working',note='Request explicit policy/case amendment; do not launch a pipeline.'))
    s3,p3=d.source_plan('correction',[s2],[p2]);d.call('reviewer','decide',name='old-source-blocked',expected='stale_qualification',report_ref=q2['report_ref'],reason='future source/cases replaced')
    corrected=d.result(d.propose('correction',s3,snap2,binary));snap3=corrected['snapshot_ref'];q3,decision=d.qualified(snap3,p3,'correction')
    policy=spec['policies']['correction']
    suspended=d.result(d.call('reviewer','suspend',name='suspend-old-policy',function=fun,**{'from':policy['from'],'until':policy['until']},targets=[event2],reason='explicit stop before corrected policy'))['event_ref']
    d.call('caller','describe',name='no-fallback',expected='out_of_scope',function=fun,date='2026-11-15')
    event3=d.result(d.call('reviewer','activate',name='activate-correction',decision_ref=decision,targets=[event2,suspended]))['event_ref'];correction_cursor=d.cursor()
    d.call('caller','invoke',name='stale-discovery',expected='artifact_mismatch',function=fun,date='2026-11-15',expected_bundle_ref=future['bundle_ref'],inputs=projected)
    d.call('worker','load',name='load-correction',function=fun,date='2026-11-15');d.call('worker','ack',name='ack-correction',event_ref=event3)
    current=d.result(d.call('caller','invoke',name='corrected-ready-review',function=fun,date='2026-11-15',expected_bundle_ref=corrected['bundle_ref'],inputs=projected))
    replay=d.result(d.call('reader','replay',name='pinned-replay',receipt_ref=old_call['receipt_ref'],reason='historical CI eligibility only; no pipeline launch'))
    restated=d.result(d.call('reader','restate',name='restatement',receipt_ref=old_call['receipt_ref'],cursor=correction_cursor,reason='apply approved action correction to the original event input'))
    retained=d.call('reader','receipt',name='retained-old-receipt',receipt_ref=old_call['receipt_ref'])
    d.check('correction-replay-restatement','Does a formula correction preserve the old false and compute a separate true?',old_call['actual']['value'] is spec['expected_replay'] and replay['actual']['value'] is spec['expected_replay'] and current['actual']['value'] is spec['expected_restatement'] and restated['actual']['value'] is spec['expected_restatement'] and retained==old_call['receipt'] and replay['receipt']['selection']['snapshot_ref']==snap2 and restated['receipt']['selection']['snapshot_ref']==snap3,{'old':old_call,'current':current,'replay':replay,'restatement':restated})
    completed=d.result(d.call('maintainer','checkpoint',name='complete-correction-task',task_id=observation['task_id'],checkpoint=saved['checkpoint'],token=saved['token'],status='complete',note='Source/formula corrected; historical answer replayed and restated.',correction_snapshot=snap3))
    d.check('durable-correction-task','Can the second workflow use the same stored task/checkpoint contract?',completed['status']=='complete' and completed['correction_snapshot']==snap3,completed)
    return {'snapshot':snap3,'bundle':corrected['bundle_ref'],'event':event3,'old_receipt':old_call['receipt_ref'],'old_answer':old_call['actual'],'new_answer':restated['actual'],'audit':audit,'history':history,'correction_cursor':correction_cursor}
