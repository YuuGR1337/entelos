"""Conformance at the local Host/JSONL and GitStore transaction boundaries."""
import copy
import multiprocessing
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from git_store import GitStore,Fault,encode,decode
from cel_runtime import CELRuntime
from lifecycle import Host
from scenario import Driver,demonstrate

WORK=Path(os.environ['LIFECYCLE_TEST_WORK'])
BINARY=Path(os.environ['LIFECYCLE_CEL_BINARY'])
BUNDLE=Path(os.environ['LIFECYCLE_CEL_BUNDLE'])
FIXTURE=Path(os.environ['LIFECYCLE_FIXTURE'])
ROOT=Path(__file__).resolve().parent

class BarrierStore(GitStore):
    def __init__(self,path,barrier):super().__init__(path);self.barrier=barrier
    def transact(self,expected,key,payload,apply):
        def prepared(view):
            result=apply(view);self.barrier.wait(timeout=20);return result
        return super().transact(expected,key,payload,prepared)

def race(repo,work,context,barrier,queue,expected,decision,target,key):
    try:
        host=Host(BarrierStore(repo,barrier),CELRuntime(BINARY,work/'tmp'),context)
        value=host.handle({'op':'activate','decision_ref':decision,'targets':[target],
                           'expected_revision':expected,'request_key':key})
        queue.put({'key':key,'status':'ok','data':value})
    except Fault as e:queue.put({'key':key,**e.record()})
    except Exception as e:queue.put({'key':key,'status':'unexpected','error':repr(e)})

class Conformance(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        WORK.mkdir(parents=True,exist_ok=True)
        cls.driver=Driver(ROOT,WORK/'baseline',BINARY,decode(FIXTURE.read_bytes()))
        cls.driver.work.mkdir(parents=True,exist_ok=True)
        # Driver creates context files; baseline is created before construction below in main.
        cls.pins=demonstrate(cls.driver,BUNDLE)
        cls.context=decode(cls.driver.contexts['reviewer'].read_bytes())
        (WORK/'baseline-results.json').write_bytes(encode({'checks':cls.driver.checks,'trace':cls.driver.trace,'pins':cls.pins}))
    def setUp(self):
        self.directory=Path(tempfile.mkdtemp(dir=WORK,prefix=self._testMethodName+'-'))
        self.repo=self.directory/'state.git';shutil.copytree(self.driver.repo,self.repo)
        self.store=GitStore(self.repo)
    def host(self,actor='reviewer',store=None,runtime=None,scope=None):
        context=decode(self.driver.contexts[actor].read_bytes())
        if scope is not None:context['scope']=scope
        return Host(store or self.store,runtime or CELRuntime(BINARY,self.directory/'tmp'),context)
    def fault(self,code,fn):
        with self.assertRaises(Fault) as caught:fn()
        self.assertEqual(caught.exception.code,code)
        return caught.exception.record()
    def request(self,op,**fields):return {'op':op,'expected_revision':self.store.root(),'request_key':self._testMethodName}|fields
    def save(self,value):
        (WORK/(self._testMethodName+'.json')).write_bytes(encode(value))
    def test_two_real_activation_writers_publish_only_one_root(self):
        before=self.store.read();ctx=multiprocessing.get_context('fork');barrier=ctx.Barrier(2);queue=ctx.Queue()
        procs=[ctx.Process(target=race,args=(self.repo,self.directory,self.context,barrier,queue,before.cursor,
                                            self.pins['decision'],self.pins['correction_event'],'racer-'+str(i))) for i in range(2)]
        for process in procs:process.start()
        for process in procs:process.join(30);self.assertFalse(process.is_alive());self.assertEqual(process.exitcode,0)
        results=[queue.get(timeout=5) for _ in procs]
        self.assertEqual(sorted(x['status'] for x in results),['ok','stale_revision'])
        loser=next(x for x in results if x['status']=='stale_revision')
        self.assertIn('root CAS lost',loser['diagnostic']['message'])
        after=self.store.read();self.assertEqual(len(after.state['events']),len(before.state['events'])+1)
        self.assertEqual(len(after.state['outbox']),len(before.state['outbox'])+1)
        self.assertEqual(sum(x['key'] in after.state['requests'] for x in results),1)
        self.assertEqual(self.store.read(before.cursor).state,before.state)
        self.save({'results':results,'before':before.cursor,'after':after.cursor,'accepted_event_delta':len(after.state['events'])-len(before.state['events'])})
    def test_interruption_before_root_publication_publishes_no_outbox(self):
        class BeforeCAS(GitStore):
            def git(self,*args,**kwargs):
                if args and args[0]=='update-ref':raise Fault('simulated_interruption','injected before CAS','store')
                return super().git(*args,**kwargs)
        before=self.store.read()
        def stage(view):
            ref=view.put('test-marker',{'note':'unpublished'});view.state['outbox'][ref]={'status':'pending'}
            return {'marker':ref}
        fault=self.fault('simulated_interruption',lambda:BeforeCAS(self.repo).transact(before.cursor,'interrupted',{'op':'test-marker'},stage))
        after=self.store.read();self.assertEqual(before.cursor,after.cursor);self.assertEqual(before.state,after.state)
        self.save({'fault':fault,'root_unchanged':before.cursor==after.cursor,'outbox_unchanged':before.state['outbox']==after.state['outbox']})
    def test_lost_commit_response_recovers_original_result_after_later_write(self):
        class LostReply(GitStore):
            def git(self,*args,**kwargs):
                result=super().git(*args,**kwargs)
                if args and args[0]=='update-ref':raise Fault('simulated_reply_loss','injected after successful CAS','store')
                return result
        before=self.store.root();payload={'op':'test-marker'}
        def stage(view):return {'marker':view.put('test-marker',{'note':'committed before reply loss'})}
        self.fault('simulated_reply_loss',lambda:LostReply(self.repo).transact(before,'reply-lost',payload,stage))
        accepted=self.store.root();self.assertNotEqual(before,accepted)
        self.store.transact(accepted,'later',{'op':'later'},lambda v:{'later':'accepted'})
        retry=self.store.transact(before,'reply-lost',payload,lambda v:self.fail('retry applied command twice'))
        self.assertTrue(retry['reused']);self.assertEqual(retry['cursor'],accepted)
        self.fault('idempotency_conflict',lambda:self.store.transact(before,'reply-lost',{'op':'different'},stage))
        self.save({'original_cursor':accepted,'retry':retry,'current_cursor':self.store.root()})
    def test_replay_refuses_a_missing_runtime_build(self):
        before=self.store.root()
        host=self.host('reader',runtime=CELRuntime(self.directory/'absent-binary',self.directory/'tmp'))
        fault=self.fault('implementation_unavailable',lambda:host.handle(self.request('replay',receipt_ref=self.pins['old_receipt'],reason='missing implementation test')))
        self.assertEqual(before,self.store.root());self.save(fault)
    def test_missing_retained_blob_is_not_replaced_by_latest(self):
        view=self.store.read();receipt=view.get(self.pins['old_receipt'],'receipt');name='o-'+receipt['inputs_ref'][7:]
        # Deliberate trusted-store corruption in an isolated test copy. Normal Host operations never delete artifacts.
        self.store.transact(view.cursor,'simulate-retention-loss',{'op':'test-corruption'},lambda v:(v.entries.pop(name),{'removed':receipt['inputs_ref']})[1])
        fault=self.fault('artifact_unavailable',lambda:self.host('reader').handle(self.request('replay',receipt_ref=self.pins['old_receipt'],reason='missing input test')))
        self.assertEqual(view.get_raw(receipt['inputs_ref'],'application/json'),encode({'archived':False,'state':'reviewed'}))
        self.save({'fault':fault,'old_cursor_still_retains_input':view.cursor})
    def test_imported_ambiguous_registry_fails_closed(self):
        view=self.store.read();event=view.get(self.pins['correction_event'],'event')
        event=dict(event,targets=[],event_id='deliberately-invalid-import')
        def corrupt(v):
            ref=v.put('event',event);v.state['events'].append(ref);return {'event_ref':ref}
        self.store.transact(view.cursor,'invalid-import',{'op':'test-corruption'},corrupt)
        fault=self.fault('ambiguous_activation',lambda:self.host('reader').handle({'op':'resolve','function':'record.visible','date':'2026-11-15'}))
        old=self.host('reader').handle({'op':'resolve','function':'record.visible','date':'2026-11-15','cursor':view.cursor})
        self.assertEqual(old['event_ref'],self.pins['correction_event']);self.save({'fault':fault,'historical_resolution':old})
    def test_scope_roles_and_report_fields_are_host_owned(self):
        self.fault('out_of_scope',lambda:self.host('reader',scope='other').handle({'op':'resolve','function':'record.visible','date':'2026-11-15'}))
        self.fault('invalid_request',lambda:self.host('caller').handle({'op':'describe','function':'record.visible','date':'2026-11-15','scope':'demo'}))
        self.fault('invalid_request',lambda:self.host('runner').handle(self.request('qualify',snapshot_ref=self.pins['correction_snapshot'],plan_ref='forged',status='passed')))
        self.fault('forbidden',lambda:self.host('author').handle(self.request('activate',decision_ref=self.pins['decision'],targets=[self.pins['correction_event']])))
        self.fault('unknown_cursor',lambda:self.host('reader').handle({'op':'resolve','function':'record.visible','date':'2026-11-15','cursor':'f'*40}))
    def test_restatement_requires_later_cursor_and_replay_exact_bytes(self):
        host=self.host('reader');old=host.handle({'op':'receipt','receipt_ref':self.pins['old_receipt']})
        self.fault('invalid_cursor',lambda:host.handle(self.request('restate',receipt_ref=self.pins['old_receipt'],cursor=old['selection']['registry_cursor'],reason='not a later root')))
        # Same values with different bytes are also a resupplied-input mismatch.
        raw='{"state":"reviewed", "archived":false}'
        self.fault('input_mismatch',lambda:host.handle(self.request('replay',receipt_ref=self.pins['old_receipt'],inputs_json=raw,reason='byte identity test')))
    def test_snapshots_do_not_mix_new_objects_with_old_state(self):
        old=self.store.read();result=self.store.transact(old.cursor,'new-blob',{'op':'marker'},lambda v:{'ref':v.put('marker',{'message':'new'})})
        ref=result['result']['ref'];self.fault('artifact_unavailable',lambda:old.get(ref,'marker'))
        self.assertEqual(self.store.read().get(ref,'marker'),{'message':'new'})
        self.assertNotIn('new-blob',old.state['requests']);self.assertEqual(self.store.read(old.cursor).state,old.state)
    def test_wire_and_interval_errors_publish_no_state(self):
        before=self.store.root()
        self.fault('invalid_record',lambda:decode(b'{"op":"describe","op":"activate"}'))
        self.fault('invalid_record',lambda:decode(b'{"value":NaN}'))
        self.fault('invalid_record',lambda:decode(b'{"value":"\\ud800"}'))
        self.fault('invalid_interval',lambda:self.host().handle(self.request('source',function='record.visible',**{'from':'2026-13-01','until':'2027-01-01'},text='bad',reason='bad',supersedes=[])))
        self.assertEqual(self.store.root(),before)
    def test_storage_failure_is_not_a_stale_writer(self):
        import subprocess
        class DiskFailure(GitStore):
            def git(self,*args,**kwargs):
                if args and args[0]=='update-ref':return subprocess.CompletedProcess(args,1,b'',b'injected storage write failure')
                return super().git(*args,**kwargs)
        before=self.store.root()
        fault=self.fault('store_error',lambda:DiskFailure(self.repo).transact(before,'storage-fault',{'op':'marker'},lambda v:{'ref':v.put('marker',{'message':'not accepted'})}))
        self.assertEqual(before,self.store.root());self.save(fault)
    def test_host_context_rejects_a_role_string(self):
        context=dict(self.context,roles='reviewer')
        self.fault('configuration_error',lambda:Host(self.store,CELRuntime(BINARY,self.directory/'tmp'),context))
    def test_git_environment_cannot_redirect_objects(self):
        bad=self.directory/'wrong-object-directory';before=os.environ.get('GIT_OBJECT_DIRECTORY')
        try:
            os.environ['GIT_OBJECT_DIRECTORY']=str(bad)
            store=GitStore(self.directory/'isolated.git')
            self.assertEqual(store.root(),'')
            result=store.transact('','first',{'op':'marker'},lambda v:{'ref':v.put('marker',{'message':'local'})})
            self.assertFalse(bad.exists());self.assertEqual(store.read().get(result['result']['ref'],'marker'),{'message':'local'})
        finally:
            if before is None:os.environ.pop('GIT_OBJECT_DIRECTORY',None)
            else:os.environ['GIT_OBJECT_DIRECTORY']=before
    def test_malformed_bundle_is_an_explicit_error(self):
        view=self.store.read();snapshot=view.get(self.pins['correction_snapshot'],'snapshot')
        request=self.request('propose',function='record.visible',**{'from':snapshot['from'],'until':snapshot['until']},
                             base_snapshot=self.pins['correction_snapshot'],source_ref=snapshot['source_ref'],
                             bundle=['manifest','environment','source','fixed'],reason='malformed author request')
        self.fault('invalid_bundle',lambda:self.host('author').handle(request))
    def test_answers_require_the_qualified_lifecycle_build(self):
        host=self.host('worker');host.build_ref='sha256:'+'0'*64
        self.fault('implementation_unavailable',lambda:host.handle(self.request('load',function='record.visible',date='2026-11-15')))
        host=self.host('caller');host.build_ref='sha256:'+'0'*64
        selected=self.host('reader').handle({'op':'resolve','function':'record.visible','date':'2026-11-15'})
        before=self.store.root()
        self.fault('implementation_unavailable',lambda:host.handle(self.request('invoke',function='record.visible',date='2026-11-15',expected_bundle_ref=selected['bundle_ref'],inputs={'state':'ready','archived':False})))
        self.assertEqual(self.store.root(),before)
    def test_reviewer_must_differ_from_producer_and_runner(self):
        report_ref=self.store.read().get(self.pins['decision'],'decision')['report_ref']
        for actor in ('author','fixture-runner'):
            host=self.host('reviewer');host.context=host.context|{'actor':actor}
            self.fault('separation_required',lambda:host.handle(self.request('decide',report_ref=report_ref,reason='self approval')))
    def test_replay_records_whether_the_answer_matches(self):
        old=self.host('reader').handle({'op':'receipt','receipt_ref':self.pins['old_receipt']})
        value=self.host('reader').handle(self.request('replay',receipt_ref=self.pins['old_receipt'],reason='match test'))['result']
        self.assertIs(value['receipt']['matches_original'],True);self.assertEqual(value['receipt']['response_ref'],old['response_ref'])
    def test_wrong_json_types_and_deep_nesting_are_faults(self):
        before=self.store.root()
        for raw in (5,None,{}):
            self.fault('invalid_request',lambda:self.host('reader').handle(self.request('replay',receipt_ref=self.pins['old_receipt'],inputs_json=raw,reason='type test')))
        self.fault('invalid_record',lambda:decode(b'['*400000+b']'*400000))
        self.assertEqual(self.store.root(),before)
    def test_restatement_cannot_use_a_null_cursor(self):
        self.fault('invalid_cursor',lambda:self.host('reader').handle(self.request('restate',receipt_ref=self.pins['old_receipt'],cursor=None,reason='must be explicit')))
    def test_git_object_integrity_and_restart(self):
        self.store.git('fsck','--full','--no-dangling')
        fresh=self.host('reader',store=GitStore(self.repo))
        old=fresh.handle({'op':'receipt','receipt_ref':self.pins['old_receipt']})
        self.assertEqual(old['mode'],'invoke')
        selected=fresh.handle({'op':'resolve','function':'record.visible','date':'2026-11-15'})
        self.assertEqual(selected['snapshot_ref'],self.pins['correction_snapshot'])
        self.save({'old_receipt':old,'current_selection':selected,'fsck':'passed'})

if __name__=='__main__':
    (WORK/'baseline').mkdir(parents=True,exist_ok=True)
    class Results(unittest.TextTestResult):
        def __init__(self,*args,**kwargs):super().__init__(*args,**kwargs);self.passed=[]
        def addSuccess(self,test):self.passed.append(test.id().split('.')[-1]);super().addSuccess(test)
    result=unittest.TextTestRunner(verbosity=2,resultclass=Results).run(unittest.defaultTestLoader.loadTestsFromTestCase(Conformance))
    record={'tests_run':result.testsRun,'passed':result.passed,'failures':[{'test':x.id(),'traceback':s} for x,s in result.failures],
            'errors':[{'test':x.id(),'traceback':s} for x,s in result.errors],'successful':result.wasSuccessful()}
    (WORK/'test-results.json').write_bytes(encode(record))
    raise SystemExit(not result.wasSuccessful())
