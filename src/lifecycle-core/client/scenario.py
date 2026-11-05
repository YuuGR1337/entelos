#!/usr/bin/env python3
"""Run the record.visible correction and replay loop through the Go JSONL host cmd/lifecycle-core.

The expected answers come from the fixture. Each step is one new Go host process.
"""
import argparse
import json
from pathlib import Path
import lifecycle_client
from lifecycle_client import encode,decode,envelope,content_ref,MIMES

class Driver(lifecycle_client.Driver):
    def bundle(self,example,allowed):
        data={key:(example/filename).read_bytes() for key,filename in {'manifest':'manifest.json','environment':'env.json','source':'rule.cel','fixed':'fixed.json'}.items()}
        data['fixed']=encode({'allowed_state':allowed})
        m=decode(data['manifest'])
        for key,field in [('environment','environment_ref'),('source','source_ref'),('fixed','fixed_ref')]:m[field]=content_ref(envelope(MIMES[key],data[key]))
        data['manifest']=encode(m)
        return {key:value.decode() for key,value in data.items()}
    def cases(self,allowed):
        # Expected truth values are stated by the approved source table, not evaluated from a candidate.
        return [{'case':'visible-current','inputs':{'state':allowed,'archived':False},'status':'ok','value':True},
                {'case':'different-state','inputs':{'state':'closed','archived':False},'status':'ok','value':False},
                {'case':'archived-current','inputs':{'state':allowed,'archived':True},'status':'ok','value':False}]
    def source_plan(self,stage,source_supersedes=None,plan_supersedes=None):
        f=self.fixture;table=f[stage]
        source=self.result(self.call('reviewer','source',name=stage+'-source',function=f['function'],
                                    **{'from':table['from'],'until':table['until']},
                                    text='Invented approved source table: visible state '+table['allowed_state']+'; archived records are invisible.',
                                    supersedes=source_supersedes or [],reason=stage+' requirement'))['source_ref']
        plan=self.result(self.call('reviewer','plan',name=stage+'-plan',source_ref=source,cases=self.cases(table['allowed_state']),
                                  origin='Independent host fixture from the invented source table',supersedes=plan_supersedes or []))['plan_ref']
        return source,plan
    def propose(self,stage,source,base,bundle,name=None,expected='ok'):
        table=self.fixture[stage]
        # A stage that names a stale mode declares it in the proposal.
        mode={'stale_mode':table['stale_mode']} if 'stale_mode' in table else {}
        return self.call('author','propose',name=name or stage+'-proposal',expected=expected,function=self.fixture['function'],
                         **{'from':table['from'],'until':table['until']},base_snapshot=base,source_ref=source,bundle=bundle,reason=stage+' proposal',**mode)
    def qualify_decide(self,snapshot,plan,prefix):
        q=self.result(self.call('runner','qualify',name=prefix+'-qualify',snapshot_ref=snapshot,plan_ref=plan))
        d=self.result(self.call('reviewer','decide',name=prefix+'-decide',report_ref=q['report_ref'],reason='manual fixture approval'))
        return q,d['decision_ref']

def demonstrate(d,example):
    f=d.fixture;fun=f['function'];exp=f['expected']
    s1,p1=d.source_plan('initial')
    b1=d.bundle(example,f['initial']['allowed_state'])
    candidate=d.result(d.propose('initial',s1,'',b1));snap1=candidate['snapshot_ref']
    d.call('caller','describe',expected='out_of_scope',function=fun,date='2026-10-15')
    d.call('author','activate',expected='forbidden',decision_ref='forged',targets=[])
    d.call('author','qualify',expected='forbidden',snapshot_ref=snap1,plan_ref=p1)
    d.call('author','decide',expected='forbidden',report_ref='forged',reason='self-certify')
    q1,dec1=d.qualify_decide(snap1,p1,'initial')
    initial=d.result(d.call('reviewer','activate',name='initial-activate',decision_ref=dec1,targets=[]))['event_ref']
    descriptor=d.call('caller','describe',function=fun,date='2026-10-15')
    d.call('caller','invoke',name='before-load',expected='revision_not_loaded',function=fun,date='2026-10-15',expected_bundle_ref=candidate['bundle_ref'],inputs={'state':'ready','archived':False})
    d.call('worker','load',name='initial-load',function=fun,date='2026-10-15')
    original=d.result(d.call('caller','invoke',name='october-call',function=fun,date='2026-10-15',expected_bundle_ref=candidate['bundle_ref'],inputs={'state':'ready','archived':False}))
    invalid=d.result(d.call('caller','invoke',name='fixed-binding-override',function=fun,date='2026-10-15',expected_bundle_ref=candidate['bundle_ref'],inputs={'state':'ready','archived':False,'allowed_state':'closed'}))
    d.check('typed-caller-boundary','Does lifecycle invocation retain the tested rejection of a fixed-parameter override?',invalid['actual']['status']=='invalid_input' and 'value' not in invalid['actual'],invalid)
    historical=d.cursor()
    d.check('initial-qualified-loaded-call','Can the approved bundle serve a typed call only after the selected revision is loaded?',original['actual']['value'] is exp['initial_october_ready'] and q1['report']['status']=='passed',original)
    # A valid CEL bundle with the wrong host parameter must fail independent cases.
    wrong=d.result(d.propose('initial',s1,snap1,d.bundle(example,'closed'),name='wrong-data-proposal'))['snapshot_ref']
    bad=d.result(d.call('runner','qualify',name='wrong-data-qualify',snapshot_ref=wrong,plan_ref=p1))
    d.call('reviewer','decide',name='wrong-data-admission',expected='qualification_failed',report_ref=bad['report_ref'],reason='try bad data')
    d.check('independent-cases-block-bad-data','Does a wrong fixed parameter remain a failed inactive proposal?',bad['report']['status']=='failed',bad)
    # A report from a role-allowed but uninstalled runner is not admissible.
    fake=d.result(d.call('other-runner','qualify',name='other-runner',snapshot_ref=snap1,plan_ref=p1))
    d.call('reviewer','decide',expected='untrusted_runner',report_ref=fake['report_ref'],reason='reject other runner')
    s2,p2=d.source_plan('amendment',[s1],[p1])
    b2=d.bundle(example,f['amendment']['allowed_state'])
    amendment=d.result(d.propose('amendment',s2,wrong,b2));snap2=amendment['snapshot_ref']
    d.propose('amendment',s2,wrong,b2,name='stale-competing-proposal',expected='stale_proposal')
    q2,dec2=d.qualify_decide(snap2,p2,'amendment')
    d.call('reviewer','activate',name='ambiguous-write',expected='ambiguous_activation',decision_ref=dec2,targets=[])
    old_root=d.cursor()
    future_write=d.call('reviewer','activate',name='future-activate',decision_ref=dec2,targets=[initial]);future=future_write['result']['event_ref']
    future_cursor=future_write['cursor']
    october=d.call('reader','resolve',function=fun,date='2026-10-15')
    november=d.call('reader','resolve',function=fun,date='2026-11-15')
    oldnov=d.call('reader','resolve',function=fun,date='2026-11-15',cursor=historical)
    d.check('future-and-historical-selection','Do interval masks retain the earlier period and the captured historical selection?',october['snapshot_ref']==snap1 and november['snapshot_ref']==snap2 and oldnov['snapshot_ref']==snap1,{'october':october,'november':november,'historical_november':oldnov})
    d.call('caller','invoke',expected='revision_not_loaded',function=fun,date='2026-11-15',expected_bundle_ref=amendment['bundle_ref'],inputs=f['original_input'])
    d.call('worker','load',name='future-load',function=fun,date='2026-11-15')
    ack=d.call('worker','ack',name='future-ack',event_ref=future)
    retry=d.call('reviewer','activate',name='future-activate',expected_revision=old_root,decision_ref=dec2,targets=[initial])
    d.check('successful-key-after-later-writes','Does retry after load/ack return the original activation cursor and result?',retry['reused'] and retry['cursor']==future_cursor and retry['result']==future_write['result'],retry)
    d.call('reviewer','activate',name='future-activate',expected='idempotency_conflict',decision_ref=dec2,targets=[])
    d.call('worker','ack',name='future-ack',expected_revision=old_root,event_ref=future)
    before=d.result(d.call('caller','invoke',name='november-before-correction',function=fun,date='2026-11-15',expected_bundle_ref=amendment['bundle_ref'],inputs=f['original_input']))
    d.check('future-answer','Is the old answer explicit before correction?',before['actual']['value'] is exp['future_november_reviewed'],before)
    observation=d.result(d.call('observer','observe',name='report-discrepancy',receipt_ref=before['receipt_ref'],route='source',message='The approved future state was transcribed incorrectly. Need a source correction.'))
    task=d.result(d.call('maintainer','claim',name='claim-correction',task_id=observation['task_id'],checkpoint='0'))
    saved=d.result(d.call('maintainer','checkpoint',name='save-correction-work',task_id=observation['task_id'],checkpoint=task['checkpoint'],token=task['token'],status='working',note='Correction source requested; no rule change yet.'))
    d.call('other-maintainer','claim',expected='task_claimed',task_id=observation['task_id'],checkpoint=saved['checkpoint'])
    taken=d.result(d.call('reviewer','takeover',name='manual-takeover',task_id=observation['task_id'],checkpoint=saved['checkpoint'],owner='other-maintainer',reason='Manually resume the saved correction task.'))
    d.call('maintainer','checkpoint',expected='stale_fence',task_id=observation['task_id'],checkpoint=taken['checkpoint'],token=saved['token'],status='complete',note='stale owner')
    s3,p3=d.source_plan('correction',[s2],[p2])
    stale=d.result(d.call('runner','qualify',name='superseded-cases',snapshot_ref=snap2,plan_ref=p2))
    d.call('reviewer','decide',expected='stale_qualification',report_ref=q2['report_ref'],reason='old source no longer valid here')
    # The initial evidence is still applicable to a nonoverlapping October snapshot.
    octave=dict(f['initial']);f['initial']=dict(octave,until='2026-11-01')
    oct_candidate=d.result(d.propose('initial',s1,snap2,b1,name='october-retained-source'))['snapshot_ref']
    f['initial']=octave
    oct_report=d.result(d.call('runner','qualify',name='october-retained-cases',snapshot_ref=oct_candidate,plan_ref=p1))
    d.check('scoped-source-and-case-supersession','Does correction retain old evidence/cases and leave unrelated dates qualified?',stale['report']['status']=='unresolved' and oct_report['report']['status']=='passed',{'superseded':stale,'nonoverlap':oct_report})
    corrected=d.result(d.propose('correction',s3,oct_candidate,d.bundle(example,f['correction']['allowed_state'])));snap3=corrected['snapshot_ref']
    q3,dec3=d.qualify_decide(snap3,p3,'correction')
    suspension=d.result(d.call('reviewer','suspend',name='suspend-wrong-future',function=fun,**{'from':f['correction']['from'],'until':f['correction']['until']},targets=[future],reason='stop serving incorrect source data'))['event_ref']
    d.call('caller','describe',name='suspension-no-fallback',expected='out_of_scope',function=fun,date='2026-11-15')
    # The worker loaded the amendment. The amendment declares no stale mode, so the invocation fails closed.
    d.call('caller','invoke',name='revoked-fails-closed',expected='activation_revoked',function=fun,date='2026-11-15',expected_bundle_ref=amendment['bundle_ref'],inputs=f['original_input'])
    d.call('caller','invoke',name='expired-fails-closed',expected='activation_expired',function=fun,date='2026-12-15',expected_bundle_ref=amendment['bundle_ref'],inputs=f['original_input'])
    current_oct=d.call('reader','resolve',function=fun,date='2026-10-15')
    enabled=d.result(d.call('reviewer','activate',name='activate-correction',decision_ref=dec3,targets=[future,suspension]))['event_ref']
    correction_cursor=d.cursor()
    d.call('caller','invoke',name='old-discovery',expected='artifact_mismatch',function=fun,date='2026-11-15',expected_bundle_ref=amendment['bundle_ref'],inputs=f['original_input'])
    d.call('caller','invoke',name='correction-before-load',expected='revision_not_loaded',function=fun,date='2026-11-15',expected_bundle_ref=corrected['bundle_ref'],inputs=f['original_input'])
    d.call('worker','load',name='load-correction',function=fun,date='2026-11-15')
    d.call('worker','ack',name='ack-correction',event_ref=enabled)
    after=d.result(d.call('caller','invoke',name='november-corrected',function=fun,date='2026-11-15',expected_bundle_ref=corrected['bundle_ref'],inputs=f['original_input']))
    replay=d.result(d.call('reader','replay',name='pinned-replay',receipt_ref=before['receipt_ref'],reason='Reproduce the earlier answer without resolving latest.'))
    restated=d.result(d.call('reader','restate',name='later-restatement',receipt_ref=before['receipt_ref'],cursor=correction_cursor,reason='Use the approved source correction for this original input/date.'))
    d.call('reader','replay',name='changed-replay-inputs',expected='input_mismatch',receipt_ref=before['receipt_ref'],inputs_json='{"state":"approved","archived":false}',reason='must not change old inputs')
    retained=d.call('reader','receipt',receipt_ref=before['receipt_ref'])
    d.check('correction-pinned-replay-restatement','Can an operator preserve the old answer and compute a separate corrected answer?',after['actual']['value'] is exp['corrected_november_reviewed'] and replay['actual']['value'] is exp['pinned_replay'] and replay['receipt']['matches_original'] is True and restated['actual']['value'] is exp['later_restatement'] and retained==before['receipt'] and replay['receipt']['selection']['snapshot_ref']==snap2 and restated['receipt']['selection']['snapshot_ref']==snap3 and current_oct['snapshot_ref']==snap1,{'before':before,'after':after,'replay':replay,'restatement':restated,'retained_old_receipt':retained})
    # The correction declares continue_stale. After its until date, the loaded correction gives the answer, and the receipt is stale.
    stale_call=d.result(d.call('caller','invoke',name='expired-continue-stale',function=fun,date='2026-12-15',expected_bundle_ref=corrected['bundle_ref'],inputs=f['original_input']))
    stale_read=d.call('reader','receipt',receipt_ref=stale_call['receipt_ref'])
    d.check('stale-continuation','Does an expired continue_stale activation give a stale receipt, while live answers stay not stale?',stale_call['actual']['value'] is exp['stale_after_expiry'] and stale_read['stale'] is True and stale_read['stale_mode']=='continue_stale' and stale_read['stale_reason']=='expired' and stale_read['selection']['snapshot_ref']==snap3 and after['receipt']['stale'] is False and after['receipt']['stale_mode']=='continue_stale' and before['receipt']['stale'] is False and before['receipt']['stale_mode']=='fail_closed',{'stale':stale_read,'live_correction':after['receipt'],'live_amendment':before['receipt']})
    complete=d.result(d.call('other-maintainer','checkpoint',name='complete-correction',task_id=observation['task_id'],checkpoint=taken['checkpoint'],token=taken['token'],status='complete',note='Source correction activated; original answer replayed and correction restated.',correction_snapshot=snap3))
    d.check('restart-and-fenced-task','Can a fresh process resume a saved task while rejecting the old owner?',complete['status']=='complete' and complete['correction_snapshot']==snap3,complete)
    state=d.call('reviewer','inspect')['state']
    d.check('retained-history-and-outbox','Are earlier evidence/plans/receipts retained with acknowledged load events?',len(state['sources'])==3 and len(state['plans'])==3 and state['outbox'][enabled]['status']=='acknowledged' and before['receipt_ref'] in state['receipts'],{'sources':state['sources'],'plans':state['plans'],'outbox':state['outbox'],'receipts':state['receipts']})
    return {'initial_snapshot':snap1,'future_snapshot':snap2,'correction_snapshot':snap3,'future_event':future,'correction_event':enabled,'decision':dec3,'old_receipt':before['receipt_ref'],'new_receipt':restated['receipt_ref'],'historical_cursor':historical,'correction_cursor':correction_cursor,'stale_receipt':stale_call['receipt_ref']}

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--work',type=Path,required=True);p.add_argument('--runtime',type=Path,required=True);p.add_argument('--bundle',type=Path,required=True);p.add_argument('--fixture',type=Path,required=True)
    p.add_argument('--host',type=Path,required=True,help='Go lifecycle-core executable');a=p.parse_args()
    a.work.mkdir(parents=True,exist_ok=True);driver=Driver(a.work,a.runtime,decode(a.fixture.read_bytes()),a.host.resolve())
    error=None;pins={}
    try:pins=demonstrate(driver,a.bundle)
    except Exception as e:error=repr(e)
    result={'kind':'NeutralLifecycleDemonstration','status':'passed' if error is None else 'failed','checks':driver.checks,'pins':pins,'error':error,'trace':driver.trace}
    (a.work/'scenario-results.json').write_bytes(encode(result));print(json.dumps({'status':result['status'],'checks':len(driver.checks),'error':error}));raise SystemExit(error is not None)
