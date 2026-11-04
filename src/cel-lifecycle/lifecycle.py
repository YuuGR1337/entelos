"""Small trusted-host lifecycle around the unchanged primitive CEL callable."""
import datetime
import hashlib
from pathlib import Path
import subprocess
import sys
from git_store import Fault,encode,decode,media,envelope,content_ref
from cel_runtime import FILENAMES,MIMES

# Roles are installed by the embedding host, never read from a request.
SCHEMA={
 'source':('reviewer',{'function','from','until','text','supersedes','reason'}),
 'plan':('reviewer',{'source_ref','cases','origin','supersedes'}),
 'propose':('author',{'function','from','until','base_snapshot','source_ref','bundle','reason'}),
 'qualify':('runner',{'snapshot_ref','plan_ref'}),
 'decide':('reviewer',{'report_ref','reason'}),
 'activate':('reviewer',{'decision_ref','targets'}),
 'suspend':('reviewer',{'function','from','until','targets','reason'}),
 'load':('worker',{'function','date'}),
 'invoke':('caller',{'function','date','expected_bundle_ref','inputs'}),
 'replay':('reader',{'receipt_ref','inputs_json','reason'}),
 'restate':('reader',{'receipt_ref','cursor','reason'}),
 'observe':('observer',{'receipt_ref','route','message'}),
 'claim':('maintainer',{'task_id','checkpoint'}),
 'checkpoint':('maintainer',{'task_id','checkpoint','token','status','note','correction_snapshot'}),
 'takeover':('reviewer',{'task_id','checkpoint','owner','reason'}),
 'ack':('worker',{'event_ref'}),
 'describe':('caller',{'function','date'}),
 'resolve':('reader',{'function','date','cursor'}),
 'inspect':('reviewer',{'cursor'}),
 'record':('reviewer',{'ref','kind','cursor'}),
 'receipt':('reader',{'receipt_ref'}),
}
READS={'describe','resolve','inspect','record','receipt'}

def date(value):
    if not isinstance(value,str):raise Fault('invalid_interval','date must be an ISO string')
    try: parsed=datetime.date.fromisoformat(value)
    except ValueError as e:raise Fault('invalid_interval',str(e)) from e
    if parsed.isoformat()!=value:raise Fault('invalid_interval','date must be canonical ISO date')
    return value

def interval(record):
    date(record['from']);date(record['until'])
    if record['from']>=record['until']:raise Fault('invalid_interval','empty or reversed interval')

def overlaps(a,b):return a['from']<b['until'] and b['from']<a['until']
def contains(a,b):return a['from']<=b['from'] and b['until']<=a['until']
def same(a,b):return a['function']==b['function'] and a['scope']==b['scope']

def implementation():
    root=Path(__file__).resolve().parent
    body=b''.join(name.encode()+b'\0'+(root/name).read_bytes()+b'\0' for name in ['git_store.py','cel_runtime.py','lifecycle.py','cli.py'])
    body+=sys.version.encode()+subprocess.check_output(['git','--version'])
    return content_ref(envelope(media('implementation'),body))

class Host:
    def __init__(self,store,runtime,context):
        self.store,self.runtime,self.context=store,runtime,context
        if not isinstance(context,dict):raise Fault('configuration_error','host context must be an object')
        for name in ['actor','roles','scope','runner_actor','worker','functions']:
            if name not in context:raise Fault('configuration_error','missing host context '+name)
        if any(not isinstance(context[k],str) or not context[k] for k in ['actor','scope','runner_actor','worker']):raise Fault('configuration_error','host actor/scope/runner/worker must be nonempty strings')
        for name in ['roles','functions']:
            if not isinstance(context[name],list) or not context[name] or any(not isinstance(x,str) or not x for x in context[name]):raise Fault('configuration_error','host '+name+' must be a nonempty string list')
        self.build_ref=implementation()
    def owned(self,record):
        if record['scope']!=self.context['scope']:raise Fault('out_of_scope','exact trusted scope required')
        if record['function'] not in self.context['functions']:raise Fault('out_of_scope','function is not installed')
        return record
    def target(self,q):
        value={'function':q['function'],'scope':self.context['scope'],'from':q['from'],'until':q['until']}
        self.owned(value);interval(value);return value
    def handle(self,q):
        if not isinstance(q,dict) or q.get('op') not in SCHEMA:raise Fault('invalid_request','unknown operation')
        op=q['op'];role,fields=SCHEMA[op]
        if role not in self.context['roles']:raise Fault('forbidden','host role does not permit '+op,'authority')
        allowed=fields|{'op'}|(set() if op in READS else {'expected_revision','request_key'})
        if set(q)-allowed:raise Fault('invalid_request','unexpected fields: '+','.join(sorted(set(q)-allowed)))
        try:
            if op in READS:return self.read_operation(op,q)
            if 'expected_revision' not in q or 'request_key' not in q:raise Fault('invalid_request','revision and request key required')
            payload={'request':{k:v for k,v in q.items() if k not in ('expected_revision','request_key')},
                     'actor':self.context['actor'],'scope':self.context['scope'],'policy_runner':self.context['runner_actor']}
            return self.store.transact(q['expected_revision'],q['request_key'],payload,lambda v:self.apply(v,op,q))
        except (KeyError,TypeError,ValueError,UnicodeError,AttributeError) as e:
            raise Fault('invalid_request','invalid or missing field: '+str(e)) from e
    def scoped_record(self,v,ref,kind):return self.owned(v.get(ref,kind))
    def files(self,v,snapshot):
        if set(snapshot['files'])!=set(FILENAMES):raise Fault('artifact_unavailable','snapshot dependency closure is incomplete','store')
        return {key:v.get_raw(ref,MIMES[key]) for key,ref in snapshot['files'].items()}
    def source_valid(self,v,source_ref,target):
        source=self.scoped_record(v,source_ref,'source')
        if not same(source,target) or not contains(source,target):return False
        return not any(source_ref in other['supersedes'] and same(other,target) and overlaps(other,target)
                       for other in (v.get(ref,'source') for ref in v.state['sources']))
    def plan_valid(self,v,plan_ref,snapshot):
        plan=self.scoped_record(v,plan_ref,'plan')
        return (plan['source_ref']==snapshot['source_ref'] and contains(plan,snapshot)
                and not any(plan_ref in newer['supersedes'] and same(newer,snapshot) and overlaps(newer,snapshot)
                            for newer in (v.get(ref,'plan') for ref in v.state['plans'])))
    def report_valid(self,v,ref):
        report=self.scoped_record(v,ref,'qualification')
        snapshot=self.scoped_record(v,report['snapshot_ref'],'snapshot')
        if report['status']!='passed':raise Fault('qualification_failed','the exact report did not pass','admission')
        if report['runner']!=self.context['runner_actor']:raise Fault('untrusted_runner','report is not from the installed runner','admission')
        if report['lifecycle_build']!=self.build_ref or report['engine_build']!=self.runtime.build():
            raise Fault('implementation_unavailable','qualification build is not installed','admission')
        if not self.source_valid(v,snapshot['source_ref'],snapshot) or not self.plan_valid(v,report['plan_ref'],snapshot):
            raise Fault('stale_qualification','source or cases have been superseded on this interval','admission')
        return report,snapshot
    def qualified_build(self,v,selection):
        report=self.scoped_record(v,selection['report_ref'],'qualification')
        if report['lifecycle_build']!=self.build_ref:
            raise Fault('implementation_unavailable','installed lifecycle build differs from the qualified build','runtime')
    def events(self,v):
        values=[]
        for ref in v.state['events']:
            event=v.get(ref,'event');interval(event)
            if event.get('kind') not in ('enable','suspend'):raise Fault('invalid_record','unknown registry event')
            event=dict(event,ref=ref);values.append(event)
        known={}
        for event in values:
            for target in event['targets']:
                old=known.get(target)
                if not old or not same(old,event) or not overlaps(old,event):raise Fault('invalid_record','invalid event mask target')
            known[event['ref']]=event
        return values
    def live(self,v,function,at):
        date(at);probe={'function':function,'scope':self.context['scope']};self.owned(probe)
        active={}
        for event in self.events(v):
            if same(event,probe) and event['from']<=at<event['until']:
                for ref in event['targets']:active.pop(ref,None)
                if event['kind']=='enable':active[event['ref']]=event
        if len(active)>1:raise Fault('ambiguous_activation','multiple live revisions; refusing selection','resolve')
        return active
    def resolve(self,v,function,at):
        active=self.live(v,function,at)
        if not active:raise Fault('out_of_scope','no enabled revision for the exact function/scope/date','resolve')
        event=next(iter(active.values()));snapshot=self.scoped_record(v,event['snapshot_ref'],'snapshot')
        return {'function':function,'scope':self.context['scope'],'date':at,'registry_cursor':v.cursor,
                'event_ref':event['ref'],'snapshot_ref':event['snapshot_ref'],'bundle_ref':snapshot['bundle_ref'],
                'engine_build':snapshot['engine_build'],'report_ref':event['report_ref'],'decision_ref':event['decision_ref']}
    def read_operation(self,op,q):
        v=self.store.read(q.get('cursor'))
        if op=='inspect':return {'cursor':v.cursor,'state':v.state}
        if op=='record':return v.get(q['ref'],q['kind'])
        if op=='receipt':return self.scoped_record(v,q['receipt_ref'],'receipt')
        selected=self.resolve(v,q['function'],q['date'])
        if op=='resolve':return selected
        snapshot=self.scoped_record(v,selected['snapshot_ref'],'snapshot')
        desc=self.runtime.execute(self.files(v,snapshot),'describe')
        return {'cursor':v.cursor,'selection':selected,'callable':desc}
    def receipt(self,v,selection,inputs,key,mode,previous='',reason='',original=''):
        snapshot=self.scoped_record(v,selection['snapshot_ref'],'snapshot')
        if self.runtime.build()!=selection['engine_build']:raise Fault('implementation_unavailable','exact replay/execution build unavailable','runtime')
        self.qualified_build(v,selection)
        files=self.files(v,snapshot)
        self.runtime.execute(files,'describe')
        actual=self.runtime.execute(files,'run',inputs=inputs)
        if actual.get('bundle_ref')!=selection['bundle_ref'] or actual.get('implementation_ref')!=selection['engine_build']:
            raise Fault('artifact_mismatch','actual execution identity differs from the selection','runtime')
        input_ref=v.put_raw('application/json',inputs)
        response_ref=v.put_raw('application/json',encode(actual))
        record={'function':selection['function'],'scope':selection['scope'],'mode':mode,'selection':selection,
                'inputs_ref':input_ref,'response_ref':response_ref,'actual_bundle':actual['bundle_ref'],
                'engine_build':actual['implementation_ref'],'lifecycle_build':self.build_ref,'invocation_id':key,
                'previous_receipt':previous,'reason':reason}
        if original:record['matches_original']=response_ref==original
        ref=v.put('receipt',record);v.state['receipts'].append(ref)
        return {'receipt_ref':ref,'receipt':record,'actual':actual}
    def append_event(self,v,event,key):
        interval(event);targets=event['targets']
        if len(targets)!=len(set(targets)):raise Fault('invalid_targets','duplicate mask target')
        known={x['ref']:x for x in self.events(v)}
        for ref in targets:
            if ref not in known or not same(known[ref],event) or not overlaps(known[ref],event):raise Fault('invalid_targets','target must exist in this scope and overlap')
        boundaries={event['from']}
        for old in known.values():
            if same(old,event) and overlaps(old,event):boundaries|={max(event['from'],old['from']),min(event['until'],old['until'])}
        conflicts=set()
        for at in sorted(boundaries):
            if at<event['until']:conflicts.update(self.live(v,event['function'],at))
        if not conflicts.issubset(set(targets)):raise Fault('ambiguous_activation','explicitly mask all overlapping live events')
        if event['kind']=='suspend' and not targets:raise Fault('invalid_targets','suspension must name an existing overlapping event')
        event['event_id']=key
        ref=v.put('event',event);v.state['events'].append(ref)
        v.state['outbox'][ref]={'event_ref':ref,'status':'pending'}
        return {'event_ref':ref}
    def apply(self,v,op,q):
        actor=self.context['actor'];key=q['request_key']
        if op=='source':
            item=self.target(q)|{'text':q['text'],'supersedes':q.get('supersedes',[]),'reason':q['reason'],'attester':actor}
            if not item['text'] or not item['reason']:raise Fault('invalid_request','source text/reason required')
            for ref in item['supersedes']:
                old=self.scoped_record(v,ref,'source')
                if not same(old,item) or not overlaps(old,item):raise Fault('invalid_supersession','source replacement must overlap the same scope/function')
            ref=v.put('source',item)
            if ref not in v.state['sources']:v.state['sources'].append(ref)
            return {'source_ref':ref}
        if op=='plan':
            source=self.scoped_record(v,q['source_ref'],'source')
            cases=q['cases']
            if not isinstance(cases,list) or not cases or not q['origin']:raise Fault('invalid_plan','independent nonempty cases and origin required')
            item={k:source[k] for k in ['function','scope','from','until']}
            item|={'source_ref':q['source_ref'],'cases':cases,'origin':q['origin'],'supersedes':q.get('supersedes',[]),'installer':actor}
            for ref in item['supersedes']:
                old=self.scoped_record(v,ref,'plan')
                if not same(old,item) or not overlaps(old,item):raise Fault('invalid_supersession','case replacement must overlap the same scope/function')
            ref=v.put('plan',item)
            if ref not in v.state['plans']:v.state['plans'].append(ref)
            return {'plan_ref':ref}
        if op=='propose':
            target=self.target(q);head_key=encode([target['function'],target['scope']]).decode().strip()
            if v.state['heads'].get(head_key,'')!=q['base_snapshot']:raise Fault('stale_proposal','explicit proposal base is stale')
            source=self.scoped_record(v,q['source_ref'],'source')
            if not same(source,target) or not contains(source,target):raise Fault('source_not_applicable','source does not cover the candidate')
            if not isinstance(q['bundle'],dict) or any(not isinstance(raw,str) for raw in q['bundle'].values()) or set(q['bundle'])!=set(FILENAMES):raise Fault('invalid_bundle','supply all exact bundle components')
            files={k:raw.encode('utf-8') for k,raw in q['bundle'].items()}
            desc=self.runtime.execute(files,'describe')
            if desc['function_id']!=target['function']:raise Fault('contract_changed','logical function differs')
            if q['base_snapshot']:
                base=self.scoped_record(v,q['base_snapshot'],'snapshot');old=decode(self.files(v,base)['manifest']);new=decode(files['manifest'])
                for field in ['environment_ref','caller_variables','profile','limits']:
                    if old[field]!=new[field]:raise Fault('contract_changed','this slice permits rule/fixed-data changes only')
            refs={k:v.put_raw(MIMES[k],raw) for k,raw in files.items()}
            snapshot=target|{'base_snapshot':q['base_snapshot'],'source_ref':q['source_ref'],'files':refs,
                             'bundle_ref':desc['bundle_ref'],'engine_build':desc['implementation_ref'],'reason':q['reason'],'producer':actor}
            ref=v.put('snapshot',snapshot);v.state['heads'][head_key]=ref
            return {'snapshot_ref':ref,'bundle_ref':desc['bundle_ref']}
        if op=='qualify':
            snapshot=self.scoped_record(v,q['snapshot_ref'],'snapshot');plan=self.scoped_record(v,q['plan_ref'],'plan')
            applicable=self.source_valid(v,snapshot['source_ref'],snapshot) and self.plan_valid(v,q['plan_ref'],snapshot)
            outcome=None;diagnostic=None
            if applicable:
                try:outcome=self.runtime.execute(self.files(v,snapshot),'check',cases=plan['cases'])
                except Fault as e:diagnostic=e.record()
            report={k:snapshot[k] for k in ['function','scope','from','until']}
            status='unresolved' if not applicable or diagnostic else ('passed' if outcome.get('status')=='passed' and outcome.get('bundle_ref')==snapshot['bundle_ref'] and outcome.get('implementation_ref')==snapshot['engine_build'] else 'failed')
            report|={'snapshot_ref':q['snapshot_ref'],'source_ref':snapshot['source_ref'],'plan_ref':q['plan_ref'],
                     'runner':actor,'engine_build':snapshot['engine_build'],'lifecycle_build':self.build_ref,
                     'status':status,'source_and_plan_applicable':applicable,'outcome':outcome,'diagnostic':diagnostic}
            ref=v.put('qualification',report);return {'report_ref':ref,'report':report}
        if op=='decide':
            report,snapshot=self.report_valid(v,q['report_ref'])
            if not q['reason']:raise Fault('invalid_request','admission reason required')
            if actor in (snapshot['producer'],report['runner']):raise Fault('separation_required','reviewer must differ from producer and runner','admission')
            item={k:snapshot[k] for k in ['function','scope','from','until']}
            item|={'snapshot_ref':report['snapshot_ref'],'report_ref':q['report_ref'],'actor':actor,'reason':q['reason'],'disposition':'accept','runner_policy':self.context['runner_actor']}
            return {'decision_ref':v.put('decision',item)}
        if op=='activate':
            decision=self.scoped_record(v,q['decision_ref'],'decision')
            report,snapshot=self.report_valid(v,decision['report_ref'])
            if decision['snapshot_ref']!=report['snapshot_ref'] or decision['disposition']!='accept' or decision['runner_policy']!=self.context['runner_actor']:
                raise Fault('invalid_decision','decision bindings differ from qualification')
            event={k:snapshot[k] for k in ['function','scope','from','until']}
            event|={'kind':'enable','snapshot_ref':report['snapshot_ref'],'report_ref':decision['report_ref'],
                    'decision_ref':q['decision_ref'],'targets':q.get('targets',[]),'actor':actor}
            return self.append_event(v,event,key)
        if op=='suspend':
            event=self.target(q)|{'kind':'suspend','targets':q['targets'],'actor':actor,'reason':q['reason']}
            return self.append_event(v,event,key)
        if op=='load':
            selection=self.resolve(v,q['function'],q['date']);snapshot=self.scoped_record(v,selection['snapshot_ref'],'snapshot')
            self.qualified_build(v,selection)
            actual=self.runtime.execute(self.files(v,snapshot),'describe')
            if actual['bundle_ref']!=selection['bundle_ref'] or actual['implementation_ref']!=selection['engine_build']:
                raise Fault('artifact_mismatch','worker loaded a different revision','runtime')
            loaded={'event_ref':selection['event_ref'],'snapshot_ref':selection['snapshot_ref'],'bundle_ref':actual['bundle_ref'],
                    'engine_build':actual['implementation_ref'],'observed_cursor':v.cursor,'worker':self.context['worker']}
            v.state['workers'][self.context['worker']]=loaded;return loaded
        if op=='invoke':
            selection=self.resolve(v,q['function'],q['date'])
            if selection['bundle_ref']!=q['expected_bundle_ref']:raise Fault('artifact_mismatch','discover the selected bundle before invocation')
            loaded=v.state['workers'].get(self.context['worker'],{})
            if loaded.get('event_ref')!=selection['event_ref'] or loaded.get('snapshot_ref')!=selection['snapshot_ref'] or loaded.get('engine_build')!=selection['engine_build']:
                raise Fault('revision_not_loaded','desired revision differs from observed worker load','runtime')
            return self.receipt(v,selection,encode(q['inputs']),key,'invoke')
        if op in ('replay','restate'):
            old=self.scoped_record(v,q['receipt_ref'],'receipt')
            if q['receipt_ref'] not in v.state['receipts']:raise Fault('artifact_unavailable','receipt is not retained')
            inputs=v.get_raw(old['inputs_ref'],'application/json')
            if op=='replay':
                if old['lifecycle_build']!=self.build_ref:raise Fault('implementation_unavailable','exact lifecycle build is unavailable')
                if 'inputs_json' in q and not isinstance(q['inputs_json'],str):raise Fault('invalid_request','inputs_json must be a JSON string')
                if 'inputs_json' in q and q['inputs_json'].encode()!=inputs:raise Fault('input_mismatch','replay requires byte-identical retained inputs')
                selection=old['selection']
            else:
                if not isinstance(q['cursor'],str) or not q['cursor']:raise Fault('invalid_cursor','restatement requires a concrete later cursor')
                later=self.store.read(q['cursor']);prior=old['selection']['registry_cursor']
                if later.cursor==prior or prior not in self.store.history(later.cursor):raise Fault('invalid_cursor','restatement needs an explicit later accepted root')
                selection=self.resolve(later,old['function'],old['selection']['date'])
            if not q.get('reason'):raise Fault('invalid_request','forensic execution reason required')
            return self.receipt(v,selection,inputs,key,op,q['receipt_ref'],q['reason'],old['response_ref'] if op=='replay' else '')
        if op=='observe':
            receipt=self.scoped_record(v,q['receipt_ref'],'receipt')
            if q['route'] not in ('source','artifact','contract','test','runtime'):raise Fault('invalid_request','route must name an owner')
            item={'function':receipt['function'],'scope':receipt['scope'],'receipt_ref':q['receipt_ref'],'route':q['route'],'message':q['message'],'observer':actor,'observation_id':key}
            ref=v.put('observation',item)
            v.state['tasks'][ref]={'observation_ref':ref,'function':receipt['function'],'scope':receipt['scope'],'status':'open','checkpoint':'0','owner':'','token':'','note':''}
            return {'observation_ref':ref,'task_id':ref}
        if op in ('claim','checkpoint','takeover'):
            task=v.state['tasks'].get(q['task_id'])
            if not task:raise Fault('task_unavailable','unknown correction task')
            self.owned(task)
            if task['checkpoint']!=q['checkpoint'] or task['status']=='complete':raise Fault('stale_checkpoint','task checkpoint is stale or complete')
            if op=='claim':
                if task['owner'] and task['owner']!=actor:raise Fault('task_claimed','another owner holds the task')
                task.update(owner=actor,status='working',token=content_ref(envelope(media('fence'),encode([key,actor,task['checkpoint']]))))
            elif op=='takeover':
                if not q['reason'] or not q['owner']:raise Fault('invalid_request','new owner and reason required')
                task.update(owner=q['owner'],status='working',token=content_ref(envelope(media('fence'),encode([key,q['owner'],task['checkpoint']]))),note=q['reason'])
            else:
                if task['owner']!=actor or task['token']!=q['token']:raise Fault('stale_fence','task owner/fence changed')
                if q['status'] not in ('working','complete'):raise Fault('invalid_request','unknown task status')
                if q.get('correction_snapshot'):self.scoped_record(v,q['correction_snapshot'],'snapshot')
                task.update(status=q['status'],note=q['note'],correction_snapshot=q.get('correction_snapshot',''))
            task['checkpoint']=str(int(task['checkpoint'])+1)
            return dict(task,task_id=q['task_id'])
        if op=='ack':
            event=self.scoped_record(v,q['event_ref'],'event');item=v.state['outbox'].get(q['event_ref'])
            loaded=v.state['workers'].get(self.context['worker'],{})
            if not item or loaded.get('event_ref')!=q['event_ref']:raise Fault('revision_not_loaded','ack requires an observed load of this event')
            item.update(status='acknowledged',worker=self.context['worker']);return dict(item)
        raise Fault('invalid_request','unimplemented operation')
