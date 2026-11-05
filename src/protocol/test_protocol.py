#!/usr/bin/env python3
"""Run the protocol vectors against the reference adapter and a fresh local Host for each vector file.

With --sidecar, run the session vectors against the Go sidecar executable through its Unix socket.
Each session file gets a new sidecar process with the context of the file and a new store.
"""
import argparse
import io
import json
import os
from pathlib import Path
import shutil
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time
import traceback

HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE))
import reference_adapter as ra  # noqa: E402
from git_store import GitStore,Fault  # noqa: E402
from lifecycle import Host,SCHEMA,READS  # noqa: E402

WORK=Path(os.environ.get('PROTOCOL_TEST_WORK') or tempfile.mkdtemp())

class NoRuntime:
    """The vectors use no rule execution. A call to the runtime is a vector error."""
    def build(self):raise Fault('implementation_unavailable','no runtime in protocol vectors','runtime')
    def execute(self,*a,**k):raise Fault('implementation_unavailable','no runtime in protocol vectors','runtime')

def substitute(value,bound):
    if isinstance(value,dict):
        if set(value)=={'$use'}:return bound[value['$use']]
        return {k:substitute(v,bound) for k,v in value.items()}
    if isinstance(value,list):return [substitute(v,bound) for v in value]
    return value

def match(expected,actual,bound,where):
    if isinstance(expected,dict) and set(expected)=={'$capture'}:
        name=expected['$capture']
        if not isinstance(actual,str) or not actual:return [where+': capture needs a nonempty string']
        if name in bound and bound[name]!=actual:return [where+': differs from captured '+name]
        bound[name]=actual;return []
    if isinstance(expected,dict) and set(expected)=={'$type'}:
        return [] if expected['$type']=='string' and isinstance(actual,str) else [where+': expected a string']
    if isinstance(expected,dict):
        if not isinstance(actual,dict) or set(expected)!=set(actual):return [where+': keys %r != %r'%(sorted(expected),sorted(actual) if isinstance(actual,dict) else actual)]
        return [e for k in sorted(expected) for e in match(expected[k],actual[k],bound,where+'.'+k)]
    if isinstance(expected,list):
        if not isinstance(actual,list) or len(expected)!=len(actual):return [where+': list differs']
        return [e for i,(x,y) in enumerate(zip(expected,actual)) for e in match(x,y,bound,where+'['+str(i)+']')]
    if type(expected)!=type(actual) or expected!=actual:return [where+': %r != %r'%(expected,actual)]
    return []

def shape(response,operation):
    """Each response must also agree with the envelope and, on success, with the result schema."""
    try:
        if response['status']=='ok':
            ra.validate(ra.ENVELOPE['success'],response,'response',ra.ENVELOPE)
            if operation not in ra.OPERATIONS:return ['response schema: a success needs a known operation for its result schema']
            schema=ra.OPERATIONS[operation]['$defs'];ra.validate(schema['result'],response['result'],'result',schema)
        else:ra.validate(ra.ENVELOPE['failure'],response,'response',ra.ENVELOPE)
    except ra.ProtocolError as e:return ['response schema: '+e.message]
    return []

class ShortWriter(io.BytesIO):
    """A writer that accepts at most size bytes for each write, as a raw socket can."""
    def __init__(self,size):super().__init__();self.size=size
    def write(self,data):return super().write(bytes(data[:self.size]))

class Chunked(io.BytesIO):
    """A reader that returns at most size bytes for each read, as a raw socket can."""
    def __init__(self,data,size):super().__init__(data);self.size=size
    def read(self,n=-1):return super().read(self.size if n<0 else min(n,self.size))

def padded(request,pad):
    """Add one unknown field so that the canonical payload is exactly pad['size'] bytes.
    The field name is pad['key_prefix'] and padding. pad['in'] is 'envelope' or 'body'."""
    def build(key):
        if pad['in']=='body':return dict(request,body=dict(request['body'],**{key:''}))
        return dict(request,**{key:''})
    room=pad['size']-len(ra.canonical(build(pad['key_prefix'])))
    if room<0:raise ValueError('pad_to is smaller than the request')
    return ra.canonical(build(pad['key_prefix']+'x'*room))

def frames(raw):
    """Split the output into frame objects. A partial frame is an error, not a crash."""
    return [value for value,payload in framed(raw)]

def framed(raw):
    """Split the output into (object, exact payload bytes) pairs."""
    out=[];stream=io.BytesIO(raw)
    while True:
        header=stream.read(4)
        if not header:return out
        if len(header)<4:raise ValueError('output ends inside a frame header')
        size=struct.unpack('>I',header)[0];body=stream.read(size)
        if len(body)<size:raise ValueError('output ends inside a frame payload')
        out.append((json.loads(body),body))

class Local:
    """The reference adapter in this process."""
    def __init__(self,adapter):self.adapter=adapter
    def frame(self,payload):
        response,close=self.adapter.frame(payload);return response,close,None
    def stream(self,data,step):
        reader=Chunked(data,step['read_size']) if 'read_size' in step else io.BytesIO(data)
        out=ShortWriter(step['write_size']) if 'write_size' in step else io.BytesIO()
        self.adapter.serve(reader,out);return out.getvalue()

# A probe that an open connection answers without a Host call: the version check rejects it.
PROBE=ra.canonical({'protocol':'9.0','request_id':'probe','operation':'inspect','body':{}})
RESET=(BrokenPipeError,ConnectionResetError)

class Sidecar:
    """A sidecar process on a Unix socket. Each step uses a new connection.
    The socket has a relative path in the sidecar directory, because the full path can be longer than sun_path."""
    NAME='sidecar.sock'
    def __init__(self,binary,runtime,directory,context):
        self.directory=directory;(directory/'context.json').write_text(json.dumps(context))
        self.log=open(directory/'sidecar.log','wb')
        self.process=subprocess.Popen([str(binary),'--socket',self.NAME,'--repo',str(directory/'store.git'),'--context',str(directory/'context.json'),
                                       '--runtime',str(runtime),'--work',str(directory/'work'),'--schema',str(HERE/'schema')],
                                      cwd=directory,stdout=subprocess.PIPE,stderr=self.log)
        line=self.process.stdout.readline()
        if json.loads(line or b'null')!={'socket':self.NAME,'status':'listening'}:
            self.process.kill();self.process.wait();self.log.close()
            raise RuntimeError('the sidecar did not start: %r; log: %s'%(line,(directory/'sidecar.log').read_text()))
    def socket_mode(self):return stat.S_IMODE(os.lstat(self.directory/self.NAME).st_mode)
    def connect(self):
        s=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);s.settimeout(60);here=os.getcwd();os.chdir(self.directory)
        try:s.connect(self.NAME)
        finally:os.chdir(here)
        return s
    @staticmethod
    def read_frame(s):
        """Return the payload of one frame, or None at the end of the stream."""
        header=recv_exact(s,4)
        if not header:return None
        if len(header)<4:raise ValueError('output ends inside a frame header')
        size=struct.unpack('>I',header)[0];body=recv_exact(s,size)
        if len(body)<size:raise ValueError('output ends inside a frame payload')
        return body
    def frame(self,payload):
        s=self.connect()
        try:
            s.sendall(struct.pack('>I',len(payload))+payload)
            body=self.read_frame(s)
            if body is None:raise ValueError('the sidecar sent no response')
            # An open connection answers the probe. A closed connection ends the stream.
            try:s.sendall(struct.pack('>I',len(PROBE))+PROBE)
            except RESET:pass
            probe=self.read_frame(s)
            if probe is not None and json.loads(probe).get('request_id')!='probe':raise ValueError('unexpected probe response')
        finally:s.close()
        return json.loads(body),probe is None,body
    def stream(self,data,step):
        s=self.connect();size=step.get('read_size') or len(data);out=b''
        try:
            try:
                for i in range(0,len(data),size):
                    s.sendall(data[i:i+size])
                    # A pause makes separate segments, so the sidecar sees short reads.
                    if 'read_size' in step:time.sleep(0.001)
                s.shutdown(socket.SHUT_WR)
            except OSError:pass
            while True:
                try:chunk=s.recv(step.get('write_size') or 65536)
                except ConnectionResetError:break
                if not chunk:break
                out+=chunk
        finally:s.close()
        return out
    def stop(self):
        """Stop with SIGTERM. The sidecar must exit with 0 and remove its socket file."""
        errors=[]
        self.process.terminate()
        try:code=self.process.wait(timeout=30)
        except subprocess.TimeoutExpired:self.process.kill();code=self.process.wait();errors.append('sidecar: no exit after SIGTERM')
        self.log.close()
        if code!=0:errors.append('sidecar: exit code %r; log: %s'%(code,(self.directory/'sidecar.log').read_text()))
        if os.path.lexists(self.directory/self.NAME):errors.append('sidecar: the socket file stays after shutdown')
        return errors

def recv_exact(s,size):
    chunks=[];count=0
    while count<size:
        try:chunk=s.recv(size-count)
        except ConnectionResetError:break
        if not chunk:break
        chunks.append(chunk);count+=len(chunk)
    return b''.join(chunks)

def run_file(path,sidecar=None):
    """Run one session file on a fresh store. If a step fails, keep the store directory under WORK.
    sidecar is None for the reference adapter, or (binary, runtime) for the Go sidecar."""
    doc=json.loads(path.read_text());errors=[];bound={}
    directory=Path(tempfile.mkdtemp(prefix=path.stem+'-',dir=WORK))
    if sidecar:
        transport=Sidecar(sidecar[0],sidecar[1],directory,doc['context'])
        if transport.socket_mode()!=0o600:errors.append(path.name+': socket mode %o, expected 600'%transport.socket_mode())
    else:transport=Local(ra.Adapter(Host(GitStore(directory/'store.git'),NoRuntime(),doc['context'])))
    try:
        for step in doc['steps']:
            name=path.name+':'+step['name']
            # An exception in one step is a vector error. The record keeps the full text and the traceback.
            try:errors+=run_step(transport,step,bound,name)
            except Exception:errors.append(name+': raised\n'+traceback.format_exc())
    finally:
        if sidecar:errors+=[path.name+': '+e for e in transport.stop()]
    if errors:return len(doc['steps']),errors,str(directory)
    shutil.rmtree(directory);return len(doc['steps']),errors,None

def run_step(transport,step,bound,name):
    errors=[];expect=step['expect']
    if 'stream_hex' in step:
        pairs=framed(transport.stream(bytes.fromhex(step['stream_hex']),step));got=[r for r,payload in pairs]
        errors+=match(expect['responses'],got,bound,name)
        errors+=[name+': response exceeds the frame bound' for r in got if len(ra.canonical(r))>ra.MAX_FRAME]
        for i,(r,payload) in enumerate(pairs):
            # Each frame payload must be exactly the canonical bytes of its own object.
            errors+=[name+'[%d]: payload is not canonical'%i] if payload!=ra.canonical(r) else []
            errors+=shape(r,step.get('operation'))
        return errors
    if 'pad_to' in step:
        payload=padded(substitute(step['request'],bound),step['pad_to'])
        if len(payload)!=step['pad_to']['size']:errors.append(name+': padded payload has the wrong size')
    elif 'request' in step:payload=ra.canonical(substitute(step['request'],bound))
    elif 'payload_text' in step:payload=step['payload_text'].encode('utf-8')
    else:payload=bytes.fromhex(step['payload_hex'])
    response,close,raw=transport.frame(payload)
    if len(ra.canonical(response))>ra.MAX_FRAME:errors.append(name+': response exceeds the frame bound')
    # Through a socket, the exact payload bytes must also be the canonical bytes of the object.
    if raw is not None and raw!=ra.canonical(response):errors.append(name+': payload is not canonical')
    errors+=match(expect['response'],response,bound,name)
    errors+=shape(response,step.get('request',{}).get('operation'))
    if close!=expect.get('close',False):errors.append(name+': close=%r'%close)
    return errors

def coverage():
    """Each lifecycle operation has one schema file, and its fields agree with the Host table."""
    errors=[]
    if set(ra.OPERATIONS)!=set(SCHEMA):errors.append('schema files %r != operations %r'%(sorted(ra.OPERATIONS),sorted(SCHEMA)))
    for op,(role,fields) in SCHEMA.items():
        if op not in ra.OPERATIONS:continue
        body=ra.OPERATIONS[op]['$defs']['request_body']
        want=set(fields)|(set() if op in READS else {'expected_revision','request_key'})
        if set(body['properties'])!=want:errors.append(op+': schema fields differ from the Host table')
        if not set(body['required'])<=want:errors.append(op+': required field outside the Host table')
        if (ra.OPERATIONS[op]['x-entelos-access']=='read')!=(op in READS):errors.append(op+': access differs')
    def patterns(node,where):
        if isinstance(node,dict):
            for k,v in node.items():
                if k=='pattern' and not (v.startswith('^') and v.endswith('$')):errors.append(where+': pattern must start with ^ and end with $')
                patterns(v,where)
        elif isinstance(node,list):
            for v in node:patterns(v,where)
    for name,doc in [('envelope',{'$defs':ra.ENVELOPE})]+sorted(ra.OPERATIONS.items()):patterns(doc,name)
    return errors

class LargeHost:
    """A stub Host whose result is larger than the frame bound."""
    def handle(self,q):return {'cursor':'','state':{'blob':'x'*ra.MAX_FRAME}}

def bounds():
    """A success result that exceeds the frame bound becomes response_too_large."""
    request={'protocol':'1.0','request_id':'big','operation':'inspect','body':{}}
    response,close=ra.Adapter(LargeHost()).frame(ra.canonical(request))
    want={'protocol':'1.0','request_id':'big','status':'response_too_large','diagnostic':{'code':'response_too_large','stage':'protocol','message':{'$type':'string'}}}
    errors=match(want,response,{},'bounds:large-result')+shape(response,'inspect')
    out=io.BytesIO();ra.Adapter(LargeHost()).serve(io.BytesIO(struct.pack('>I',len(ra.canonical(request)))+ra.canonical(request)),out)
    if len(out.getvalue())-4>ra.MAX_FRAME:errors.append('bounds:serve wrote a frame over the bound')
    return errors+(['bounds:close'] if close else [])

class RaisingHost:
    """A stub Host that fails with an exception that is not a Fault. The text has a path."""
    def handle(self,q):raise RuntimeError('/private/store/secret.git: broken')

class FloatHost:
    """A stub Host whose result is outside the protocol value domain."""
    def handle(self,q):return {'cursor':'','state':{'ratio':0.5}}

class IntKeyHost:
    """A stub Host whose result has an object key that is not a string."""
    def handle(self,q):return {1:'x'}

def internal_errors():
    """An unexpected Host failure gives one internal_error frame with no exception text."""
    errors=[];request=ra.canonical({'protocol':'1.0','request_id':'int','operation':'inspect','body':{}})
    want={'protocol':'1.0','request_id':'int','status':'internal_error','diagnostic':{'code':'internal_error','stage':'host','message':{'$type':'string'}}}
    for host in (RaisingHost(),FloatHost(),IntKeyHost()):
        name='internal:'+type(host).__name__
        try:
            out=io.BytesIO();ra.Adapter(host).serve(io.BytesIO(struct.pack('>I',len(request))+request),out)
            got=frames(out.getvalue())
            errors+=match([want],got,{},name)+[e for r in got for e in shape(r,'inspect')]
            errors+=[name+': message has exception text' for r in got if 'secret' in json.dumps(r)]
        except Exception as e:errors.append(name+': raised '+type(e).__name__)
    return errors

def schema_vectors():
    """Each case must agree with its envelope definition. Invalid cases must be rejected."""
    errors=[];doc=json.loads((HERE/'vectors/envelope-schema.json').read_text())
    for case in doc['cases']:
        try:ra.validate(ra.ENVELOPE[case['def']],case['value'],case['def'],ra.ENVELOPE);valid=True
        except ra.ProtocolError:valid=False
        if valid!=case['valid']:errors.append('schema:'+case['name']+': valid=%r'%valid)
    return len(doc['cases']),errors

def jcs_vectors():
    errors=[];doc=json.loads((HERE/'vectors/jcs.json').read_text())
    for case in doc['cases']:
        got=ra.canonical(json.loads(case['input'])).decode('utf-8')
        if got!=case['canonical']:errors.append('jcs:'+case['name']+': '+got)
    return len(doc['cases']),errors

def runner_self_test():
    """A session file with one failed step and one step that raises. The store must stay, with a traceback."""
    errors=[];folder=WORK/'self-test';folder.mkdir(parents=True,exist_ok=True);path=folder/'session-self-test.json'
    context=json.loads((HERE/'vectors/session-envelope.json').read_text())['context']
    path.write_text(json.dumps({'context':context,'steps':[
        {'name':'wrong-expectation','request':{'protocol':'1.0','request_id':'t','operation':'inspect','body':{}},'expect':{'response':{}}},
        {'name':'bad-hex','payload_hex':'zz','expect':{'response':{}}}]}))
    n,found,kept=run_file(path)
    if len(found)!=2:errors.append('self-test: expected 2 step errors, got %d'%len(found))
    if not any('Traceback (most recent call last)' in e and 'ValueError' in e for e in found):errors.append('self-test: no traceback in the error record')
    if not kept or not (Path(kept)/'store.git').is_dir():errors.append('self-test: the failed store was not kept')
    # This failure is expected. Delete only the self-test files when the self-test passes.
    if not errors:
        shutil.rmtree(kept);shutil.rmtree(folder)
    return errors

def sidecar_self_test(binary,runtime):
    """Through the socket, the runner must see a wrong response, a missed close and a missed open connection."""
    errors=[];folder=WORK/'sidecar-self-test';folder.mkdir(parents=True,exist_ok=True);path=folder/'session-sidecar-self-test.json'
    context=json.loads((HERE/'vectors/session-envelope.json').read_text())['context']
    failure=lambda rid,code:{'protocol':'1.0','request_id':rid,'status':code,'diagnostic':{'code':code,'stage':'protocol','message':{'$type':'string'}}}
    path.write_text(json.dumps({'context':context,'steps':[
        {'name':'wrong-expectation','request':{'protocol':'1.0','request_id':'t','operation':'inspect','body':{}},'expect':{'response':{}}},
        {'name':'close-not-expected','payload_hex':'','expect':{'response':failure('','invalid_frame')}},
        {'name':'close-expected','request':{'protocol':'2.0','request_id':'v','operation':'inspect','body':{}},
         'expect':{'response':failure('v','unsupported_version'),'close':True}}]}))
    n,found,kept=run_file(path,(binary,runtime))
    if len(found)!=3:errors.append('sidecar self-test: expected 3 step errors, got %d: %r'%(len(found),found))
    if not any(e.endswith('close-not-expected: close=True') for e in found):errors.append('sidecar self-test: a close was not seen')
    if not any(e.endswith('close-expected: close=False') for e in found):errors.append('sidecar self-test: an open connection was not seen')
    if not kept or not (Path(kept)/'store.git').is_dir():errors.append('sidecar self-test: the failed store was not kept')
    # These failures are expected. Delete only the self-test files when the self-test passes.
    if not errors:
        shutil.rmtree(kept);shutil.rmtree(folder)
    return errors

def sidecar_main(binary,runtime):
    """Run each session file against the Go sidecar. The JCS, schema and stub Host checks are Go tests."""
    WORK.mkdir(parents=True,exist_ok=True);total=0;kept=[];files=[]
    try:errors=sidecar_self_test(binary,runtime)
    except Exception:errors=['sidecar self-test: raised\n'+traceback.format_exc()]
    for path in sorted((HERE/'vectors').glob('session-*.json')):
        try:n,e,k=run_file(path,(binary,runtime))
        except Exception:n,e,k=0,[path.name+': raised\n'+traceback.format_exc()],None
        total+=n;errors+=e;files.append({'file':path.name,'steps':n,'errors':len(e)})
        if k:kept.append(k)
    result={'successful':not errors and total>0,'transport':'unix-socket','sidecar':str(binary),'vectors':total,'files':files,'errors':errors,'kept_stores':kept}
    (WORK/'test-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2));return 0 if result['successful'] else 1

def main():
    global WORK
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--sidecar',type=Path,help='Go sidecar executable; run the session vectors through its socket')
    p.add_argument('--runtime',type=Path,help='pinned CEL executable for the sidecar')
    p.add_argument('--work',type=Path,help='work directory; the default is PROTOCOL_TEST_WORK or a new temporary directory')
    a=p.parse_args()
    if a.work:WORK=a.work.resolve()
    if a.sidecar:
        if not a.runtime:p.error('--sidecar needs --runtime')
        return sidecar_main(a.sidecar.resolve(),a.runtime.resolve())
    WORK.mkdir(parents=True,exist_ok=True);total=0;kept=[];errors=coverage()+bounds()+internal_errors()+runner_self_test()
    n,e=jcs_vectors();total+=n;errors+=e
    n,e=schema_vectors();total+=n;errors+=e
    for path in sorted((HERE/'vectors').glob('session-*.json')):
        n,e,k=run_file(path);total+=n;errors+=e
        if k:kept.append(k)
    result={'successful':not errors and total>0,'vectors':total,'errors':errors,'kept_stores':kept}
    (WORK/'test-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2));return 0 if result['successful'] else 1
if __name__=='__main__':raise SystemExit(main())
