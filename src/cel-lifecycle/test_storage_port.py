#!/usr/bin/env python3
"""Run the JSON storage port vectors against the Git adapter and the in-memory adapter."""
import argparse
import json
from pathlib import Path
import sys
from git_store import GitStore,Fault,encode
from memory_store import MemoryStore

ROOT=Path(__file__).resolve().parent
VECTORS=ROOT/'conformance/storage'
INVARIANTS={'atomic_publication','stale_cas_rejection','idempotent_success','stable_historical_read',
            'no_partial_state_after_failure','immutable_content_identity'}

class Run:
    """Run the steps of one vector on one fresh store. Collect each mismatch; do not stop at the first."""
    def __init__(self,store):self.store,self.names,self.mismatches=store,{},[]
    def value(self,expr):
        # A string that starts with "$" names a cursor that an earlier step bound.
        if isinstance(expr,str) and expr.startswith('$'):
            if expr[1:] not in self.names:raise KeyError('unbound cursor name '+expr)
            return self.names[expr[1:]]
        return expr
    def same(self,where,actual,expected):
        if actual!=expected:self.mismatches.append({'at':where,'expected':expected,'actual':actual})
    def error(self,where,expect,error):
        code=error.code if isinstance(error,Fault) else 'unexpected:'+repr(error)
        if 'error' not in expect:self.mismatches.append({'at':where,'expected':expect,'actual':{'error':code}})
        else:self.same(where+'.error',code,expect['error'])
    def bind(self,where,name,cursor):
        if not isinstance(cursor,str) or not cursor or cursor in self.names.values():
            self.mismatches.append({'at':where,'expected':'a new nonempty cursor','actual':cursor})
        self.names[name]=cursor
    def actions(self,where,view,actions):
        for i,action in enumerate(actions):
            at='%s.actions[%d]'%(where,i)
            if 'raise' in action:raise Fault(action['raise'],'vector apply failure','vector')
            if 'cursor_is' in action:self.same(at+'.cursor',view.cursor,self.value(action['cursor_is']))
            elif 'set' in action:
                target=view.state
                for name in action['set'][:-1]:target=target.setdefault(name,{})
                target[action['set'][-1]]=action['value']
            elif 'put' in action:self.same(at+'.ref',view.put(action['put'],action['value']),action['expect_ref'])
            elif 'put_raw' in action:self.same(at+'.ref',view.put_raw(action['put_raw'],action['text'].encode()),action['expect_ref'])
            elif 'get' in action or 'get_raw' in action:
                try:
                    if 'get' in action:actual={'value':view.get(action['get'],action['kind'])}
                    else:actual={'text':view.get_raw(action['get_raw'],action.get('mime')).decode()}
                except Fault as e:self.error(at,action['expect'],e)
                else:self.same(at,actual,action['expect'])
            elif 'transact' in action:self.step(at+'.transact',action['transact'])
            else:raise KeyError('unknown action: '+','.join(action))
    def check_view(self,where,view,expect):
        if 'cursor' in expect:self.same(where+'.cursor',view.cursor,self.value(expect['cursor']))
        if 'state' in expect:self.same(where+'.state',view.state,expect['state'])
        for name,value in expect.get('state_includes',{}).items():self.same(where+'.state.'+name,view.state.get(name),value)
    def step(self,where,step):
        expect=step['expect']
        if step['op']=='read':
            try:view=self.store.read(self.value(step['cursor'])) if 'cursor' in step else self.store.read()
            except Fault as e:return self.error(where,expect,e)
            if 'error' in expect:return self.mismatches.append({'at':where,'expected':expect,'actual':{'cursor':view.cursor}})
            self.check_view(where,view,expect);self.actions(where,view,step.get('actions',[]))
        elif step['op']=='history':
            self.same(where+'.cursors',self.store.history(self.value(step['cursor'])),[self.value(x) for x in expect['cursors']])
        elif step['op']=='transact':
            calls=[0]
            def apply(view):
                calls[0]+=1;self.actions(where+'.apply',view,step['apply']['actions']);return step['apply']['result']
            try:outcome=self.store.transact(self.value(step['expected']),step['key'],step['payload'],apply)
            except Fault as e:self.error(where,expect,e)
            else:
                if 'error' in expect:self.mismatches.append({'at':where,'expected':expect,'actual':outcome})
                else:
                    if 'bind' in expect:self.bind(where+'.cursor',expect['bind'],outcome['cursor'])
                    if 'cursor' in expect:self.same(where+'.cursor',outcome['cursor'],self.value(expect['cursor']))
                    self.same(where+'.result',outcome['result'],expect['result'])
                    self.same(where+'.reused',outcome['reused'],expect['reused'])
            if 'apply_calls' in expect:self.same(where+'.apply_calls',calls[0],expect['apply_calls'])
        else:raise KeyError('unknown step op: '+step['op'])

def run_vector(store,vector):
    run=Run(store)
    try:
        for i,step in enumerate(vector['steps']):run.step('steps[%d]'%i,step)
    except Exception as e:run.mismatches.append({'at':'harness','error':repr(e)})
    return {'id':vector['id'],'passed':not run.mismatches,'mismatches':run.mismatches}

def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--work',type=Path,required=True)
    a=p.parse_args();a.work.mkdir(parents=True,exist_ok=True)
    vectors=[json.loads(path.read_text()) for path in sorted(VECTORS.glob('*.json'))]
    problems=[]
    if not vectors:problems.append('no vectors found in '+str(VECTORS))
    covered=set().union(*[set(v['invariants']) for v in vectors]) if vectors else set()
    if covered!=INVARIANTS:problems.append('vectors cover %s; the spec names %s'%(sorted(covered),sorted(INVARIANTS)))
    for v in vectors:
        if v.get('format')!='storage-port-vector/v1':problems.append(v.get('id','?')+': unknown vector format')
    adapters={'git':lambda id:GitStore(a.work/'git'/(id+'.git')),'memory':lambda id:MemoryStore()}
    results={name:[run_vector(make(v['id']),v) for v in vectors] for name,make in adapters.items()}
    record={'vectors':[v['id'] for v in vectors],'problems':problems,'adapters':results,
            'summary':{name:{'passed':sum(x['passed'] for x in r),'failed':sum(not x['passed'] for x in r)} for name,r in results.items()},
            'limits':'Vectors check the port contract only. They do not show durability, performance or concurrency under load.'}
    record['successful']=not problems and all(x['passed'] for r in results.values() for x in r)
    (a.work/'test-results.json').write_bytes(encode(record))
    print(json.dumps(record['summary']))
    for name,r in results.items():
        for x in r:
            if not x['passed']:print(name,x['id'],json.dumps(x['mismatches']),file=sys.stderr)
    for problem in problems:print(problem,file=sys.stderr)
    raise SystemExit(not record['successful'])

if __name__=='__main__':main()
