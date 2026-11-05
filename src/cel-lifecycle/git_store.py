"""Single local bare repository; publish accepted state through one root ref."""
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ROOT_REF = 'refs/lifecycle/state'

class Fault(Exception):
    def __init__(self, code, message, stage='lifecycle'):
        self.code, self.message, self.stage = code, message, stage
        super().__init__(message)
    def record(self):
        return {'status':self.code,'diagnostic':{'code':self.code,'stage':self.stage,'message':self.message}}

def encode(value):
    return (json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False)+'\n').encode('utf-8')

def decode(raw):
    def pairs(items):
        result={}
        for k,v in items:
            if k in result: raise Fault('invalid_record','duplicate JSON key: '+k,'codec')
            result[k]=v
        return result
    try:
        value=json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
                         parse_constant=lambda s: (_ for _ in ()).throw(ValueError('non-finite JSON')))
        encode(value)  # Reject unpaired Unicode surrogates as well.
        return value
    except (ValueError, UnicodeError, TypeError, RecursionError) as e:
        raise Fault('invalid_record',str(e) or 'JSON nesting is too deep','codec') from e

def media(kind): return 'application/vnd.cel-lifecycle.'+kind+'+json'
def content_ref(envelope): return 'sha256:'+hashlib.sha256(envelope).hexdigest()
def envelope(mime, raw): return mime.encode()+b'\0'+raw

def empty_state():
    return {'schema':'local-lifecycle/v1','heads':{},'sources':[],'plans':[],'events':[],
            'requests':{},'workers':{},'receipts':[],'tasks':{},'outbox':{}}

@dataclass
class View:
    store: object
    cursor: str
    state: dict
    entries: dict
    pending: dict
    def put_raw(self,mime,raw):
        body=envelope(mime,raw);ref=content_ref(body)
        self.pending[ref]=body
        return ref
    def put(self,kind,value): return self.put_raw(media(kind),encode(value))
    def get_raw(self,ref,mime=None):
        if not isinstance(ref,str) or not re.fullmatch(r'sha256:[0-9a-f]{64}',ref):
            raise Fault('invalid_reference','expected an exact content reference','store')
        if ref in self.pending: body=self.pending[ref]
        else:
            oid=self.entries.get('o-'+ref[7:])
            if not oid: raise Fault('artifact_unavailable',ref,'store')
            body=self.store.blob(oid)
        if content_ref(body)!=ref or b'\0' not in body:
            raise Fault('artifact_mismatch','content bytes differ from reference','store')
        actual,raw=body.split(b'\0',1)
        if mime and actual.decode()!=mime: raise Fault('invalid_record','unexpected media type','store')
        return raw
    def get(self,ref,kind):
        value=decode(self.get_raw(ref,media(kind)))
        if not isinstance(value,dict): raise Fault('invalid_record','record must be an object','store')
        return value

class GitStore:
    def __init__(self,path):
        self.path=Path(path).resolve()
        self.env={k:v for k,v in os.environ.items() if not k.startswith('GIT_')}|{'GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':os.devnull,
                             'GIT_TERMINAL_PROMPT':'0','GIT_AUTHOR_NAME':'Local lifecycle',
                             'GIT_AUTHOR_EMAIL':'lifecycle@example.invalid','GIT_COMMITTER_NAME':'Local lifecycle',
                             'GIT_COMMITTER_EMAIL':'lifecycle@example.invalid'}
        if not self.path.exists():
            self.path.parent.mkdir(parents=True,exist_ok=True)
            subprocess.run(['git','init','--bare','--object-format=sha1',str(self.path)],env=self.env,
                           capture_output=True,check=True)
        if self.git('rev-parse','--is-bare-repository').strip()!=b'true':
            raise Fault('invalid_store','use a dedicated bare local repository','store')
    def git(self,*args,data=None,allow_fail=False):
        proc=subprocess.run(['git','--git-dir='+str(self.path),'-c','core.hooksPath='+os.devnull,
                             '-c','core.fsmonitor=false',*args],env=self.env,input=data,capture_output=True)
        if proc.returncode and not allow_fail:
            raise Fault('store_error',proc.stderr.decode(errors='replace'),'store')
        return proc if allow_fail else proc.stdout
    def blob(self,oid):return self.git('cat-file','blob',oid)
    def root(self):
        p=self.git('rev-parse','--verify',ROOT_REF,allow_fail=True)
        return '' if p.returncode else p.stdout.decode().strip()
    def history(self,cursor):
        return self.git('rev-list','--first-parent',cursor).decode().splitlines() if cursor else []
    def read(self,cursor=None):
        root=self.root()
        if cursor is None: cursor=root
        if not isinstance(cursor,str): raise Fault('unknown_cursor','cursor is not in accepted history','store')
        if cursor:
            if not re.fullmatch('[0-9a-f]{40}',cursor) or cursor not in self.history(root):
                raise Fault('unknown_cursor','cursor is not in accepted history','store')
        if not cursor: return View(self,'',empty_state(),{}, {})
        entries={}
        for row in self.git('ls-tree','-z',cursor).split(b'\0'):
            if not row: continue
            meta,name=row.split(b'\t',1);mode,typ,oid=meta.decode().split()
            if mode!='100644' or typ!='blob': raise Fault('invalid_store','unexpected tree entry','store')
            entries[name.decode()]=oid
        if 'state.json' not in entries: raise Fault('invalid_store','missing state','store')
        state=decode(self.git('cat-file','blob',entries['state.json']))
        if not isinstance(state,dict) or state.get('schema')!='local-lifecycle/v1':
            raise Fault('invalid_store','unknown state schema','store')
        return View(self,cursor,state,entries,{})
    def transact(self,expected,key,payload,apply):
        if not isinstance(key,str) or not key or len(key)>128: raise Fault('invalid_request','bounded request key required')
        view=self.read();digest=content_ref(envelope(media('request'),encode(payload)))
        previous=view.state['requests'].get(key)
        if previous:
            if previous['payload_ref']!=digest: raise Fault('idempotency_conflict','key was used for another payload')
            # A root cannot contain its own ID. Find the first accepted commit containing this success.
            origin=view.cursor
            for cursor in self.history(view.cursor):
                if key not in self.read(cursor).state['requests']: break
                origin=cursor
            return {'cursor':origin,'result':previous['result'],'reused':True}
        if expected!=view.cursor: raise Fault('stale_revision','capture the current root and re-evaluate')
        result=apply(view)
        view.state['requests'][key]={'payload_ref':digest,'result':result}
        entries=dict(view.entries)
        entries['state.json']=self.git('hash-object','-w','--stdin',data=encode(view.state)).decode().strip()
        for ref,body in view.pending.items():
            entries['o-'+ref[7:]]=self.git('hash-object','-w','--stdin',data=body).decode().strip()
        rows=''.join('100644 blob '+oid+'\t'+name+'\n' for name,oid in sorted(entries.items()))
        tree=self.git('mktree',data=rows.encode()).decode().strip()
        args=['commit-tree',tree]
        if expected: args+=['-p',expected]
        commit=self.git(*args,data=('local lifecycle '+key+'\n').encode()).decode().strip()
        # This is the sole state publication point. No automatic retry after a lost CAS.
        p=self.git('update-ref',ROOT_REF,commit,expected or '0'*40,allow_fail=True)
        if p.returncode:
            if self.root()!=expected:raise Fault('stale_revision','root CAS lost; no state/outbox was published')
            raise Fault('store_error',p.stderr.decode(errors='replace'),'store')
        return {'cursor':commit,'result':result,'reused':False}
