#!/usr/bin/env python3
"""Run the session vectors of the wire protocol against the Go sidecar executable through its Unix socket.

Each session file gets a new sidecar process with the context of the file and a new store.
This runner has its own RFC 8785 encoder and a small JSON Schema check for the responses.
It runs the JCS and envelope schema vectors on them first, so that a runner fault is visible.
"""
import argparse
import io
import json
import re
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
WORK=Path(os.environ.get('PROTOCOL_TEST_WORK') or tempfile.mkdtemp())
MAX_FRAME=1048576
SAFE_INT=2**53-1
SCHEMAS={p.name[:-len('.schema.json')]:json.loads(p.read_text()) for p in sorted((HERE/'schema').glob('*.schema.json'))}
ENVELOPE=SCHEMAS.pop('envelope')['$defs']
OPERATIONS=SCHEMAS

class SchemaError(Exception):
    def __init__(self,message):super().__init__(message);self.message=message

def jcs(value):
    """RFC 8785 canonical form for the value domain of this protocol: no floats."""
    if value is None:return 'null'
    if value is True:return 'true'
    if value is False:return 'false'
    if isinstance(value,int):
        if abs(value)>SAFE_INT:raise ValueError('integer outside the I-JSON safe range')
        return str(value)
    if isinstance(value,str):
        value.encode('utf-8')  # A lone surrogate raises UnicodeEncodeError.
        return json.dumps(value,ensure_ascii=False)
    if isinstance(value,list):return '['+','.join(jcs(x) for x in value)+']'
    if isinstance(value,dict):
        if any(not isinstance(k,str) for k in value):raise ValueError('an object key must be a string')
        keys=sorted(value,key=lambda k:k.encode('utf-16-be'))
        return '{'+','.join(jcs(k)+':'+jcs(value[k]) for k in keys)+'}'
    raise ValueError('value outside the protocol value domain: '+type(value).__name__)

def canonical(value):return jcs(value).encode('utf-8')

def validate(schema,value,where,root):
    """A small JSON Schema 2020-12 subset: the keywords that the files in schema/ use."""
    if '$ref' in schema:
        if not schema['$ref'].startswith('#/$defs/'):raise ValueError('unsupported $ref: '+schema['$ref'])
        return validate(root[schema['$ref'][len('#/$defs/'):]],value,where,root)
    kind=schema.get('type')
    checks={'object':lambda v:isinstance(v,dict),'array':lambda v:isinstance(v,list),'string':lambda v:isinstance(v,str),
            'boolean':lambda v:isinstance(v,bool),'integer':lambda v:isinstance(v,int) and not isinstance(v,bool)}
    if kind and not checks[kind](value):raise SchemaError(where+' must be of type '+kind)
    if 'const' in schema and value!=schema['const']:raise SchemaError(where+' must be '+json.dumps(schema['const']))
    if 'enum' in schema and value not in schema['enum']:raise SchemaError(where+' is not an allowed value')
    if isinstance(value,str):
        if len(value)<schema.get('minLength',0):raise SchemaError(where+' is too short')
        if 'maxLength' in schema and len(value)>schema['maxLength']:raise SchemaError(where+' is too long')
        # Each schema pattern is anchored with ^ and $. In Python, $ also matches before a final newline,
        # so the check uses fullmatch, as $ does in ECMA-262.
        if 'pattern' in schema and not re.fullmatch(schema['pattern'],value):raise SchemaError(where+' does not match its pattern')
    if isinstance(value,int) and not isinstance(value,bool):
        if 'minimum' in schema and value<schema['minimum']:raise SchemaError(where+' is too small')
        if 'maximum' in schema and value>schema['maximum']:raise SchemaError(where+' is too large')
    if isinstance(value,list):
        if len(value)<schema.get('minItems',0):raise SchemaError(where+' has too few items')
        for i,item in enumerate(value):
            if 'items' in schema:validate(schema['items'],item,where+'['+str(i)+']',root)
    if isinstance(value,dict):
        for name in schema.get('required',[]):
            if name not in value:raise SchemaError('missing field: '+where+'.'+name)
        props=schema.get('properties',{});extra=schema.get('additionalProperties',True)
        for name in sorted(value):
            if name in props:validate(props[name],value[name],where+'.'+name,root)
            elif extra is False:raise SchemaError('unexpected field: '+where+'.'+name)
            elif isinstance(extra,dict):validate(extra,value[name],where+'.'+name,root)

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
            validate(ENVELOPE['success'],response,'response',ENVELOPE)
            if operation not in OPERATIONS:return ['response schema: a success needs a known operation for its result schema']
            schema=OPERATIONS[operation]['$defs'];validate(schema['result'],response['result'],'result',schema)
        else:validate(ENVELOPE['failure'],response,'response',ENVELOPE)
    except SchemaError as e:return ['response schema: '+e.message]
    return []

def padded(request,pad):
    """Add one unknown field so that the canonical payload is exactly pad['size'] bytes.
    The field name is pad['key_prefix'] and padding. pad['in'] is 'envelope' or 'body'."""
    def build(key):
        if pad['in']=='body':return dict(request,body=dict(request['body'],**{key:''}))
        return dict(request,**{key:''})
    room=pad['size']-len(canonical(build(pad['key_prefix'])))
    if room<0:raise ValueError('pad_to is smaller than the request')
    return canonical(build(pad['key_prefix']+'x'*room))

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

# A probe that an open connection answers without a Host call: the version check rejects it.
PROBE=canonical({'protocol':'9.0','request_id':'probe','operation':'inspect','body':{}})
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

def run_file(path,sidecar):
    """Run one session file on a fresh sidecar and store. If a step fails, keep the store directory under WORK.
    sidecar is (binary, runtime) for the Go sidecar."""
    doc=json.loads(path.read_text());errors=[];bound={}
    directory=Path(tempfile.mkdtemp(prefix=path.stem+'-',dir=WORK))
    # If the sidecar does not start, keep the directory with its log as evidence.
    try:transport=Sidecar(sidecar[0],sidecar[1],directory,doc['context'])
    except Exception:return len(doc['steps']),[path.name+': sidecar did not start\n'+traceback.format_exc()],str(directory)
    if transport.socket_mode()!=0o600:errors.append(path.name+': socket mode %o, expected 600'%transport.socket_mode())
    try:
        for step in doc['steps']:
            name=path.name+':'+step['name']
            # An exception in one step is a vector error. The record keeps the full text and the traceback.
            try:errors+=run_step(transport,step,bound,name)
            except Exception:errors.append(name+': raised\n'+traceback.format_exc())
    finally:errors+=[path.name+': '+e for e in transport.stop()]
    if errors:return len(doc['steps']),errors,str(directory)
    shutil.rmtree(directory);return len(doc['steps']),errors,None

def run_step(transport,step,bound,name):
    errors=[];expect=step['expect']
    if 'stream_hex' in step:
        pairs=framed(transport.stream(bytes.fromhex(step['stream_hex']),step));got=[r for r,payload in pairs]
        errors+=match(expect['responses'],got,bound,name)
        errors+=[name+': response exceeds the frame bound' for r in got if len(canonical(r))>MAX_FRAME]
        for i,(r,payload) in enumerate(pairs):
            # Each frame payload must be exactly the canonical bytes of its own object.
            errors+=[name+'[%d]: payload is not canonical'%i] if payload!=canonical(r) else []
            errors+=shape(r,step.get('operation'))
        return errors
    if 'pad_to' in step:
        payload=padded(substitute(step['request'],bound),step['pad_to'])
        if len(payload)!=step['pad_to']['size']:errors.append(name+': padded payload has the wrong size')
    elif 'request' in step:payload=canonical(substitute(step['request'],bound))
    elif 'payload_text' in step:payload=step['payload_text'].encode('utf-8')
    else:payload=bytes.fromhex(step['payload_hex'])
    response,close,raw=transport.frame(payload)
    if len(canonical(response))>MAX_FRAME:errors.append(name+': response exceeds the frame bound')
    # Through a socket, the exact payload bytes must also be the canonical bytes of the object.
    if raw is not None and raw!=canonical(response):errors.append(name+': payload is not canonical')
    errors+=match(expect['response'],response,bound,name)
    errors+=shape(response,step.get('request',{}).get('operation'))
    if close!=expect.get('close',False):errors.append(name+': close=%r'%close)
    return errors

def schema_vectors():
    """Each case must agree with its envelope definition. Invalid cases must be rejected."""
    errors=[];doc=json.loads((HERE/'vectors/envelope-schema.json').read_text())
    for case in doc['cases']:
        try:validate(ENVELOPE[case['def']],case['value'],case['def'],ENVELOPE);valid=True
        except SchemaError:valid=False
        if valid!=case['valid']:errors.append('schema:'+case['name']+': valid=%r'%valid)
    return len(doc['cases']),errors

def jcs_vectors():
    errors=[];doc=json.loads((HERE/'vectors/jcs.json').read_text())
    for case in doc['cases']:
        got=canonical(json.loads(case['input'])).decode('utf-8')
        if got!=case['canonical']:errors.append('jcs:'+case['name']+': '+got)
    return len(doc['cases']),errors

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
    """Run each session file against the Go sidecar. The stub Host and schema table checks are Go tests."""
    WORK.mkdir(parents=True,exist_ok=True);total=0;kept=[];files=[]
    # The runner checks its own encoder and schema check on the vectors, then its socket transport.
    jcs_count,errors=jcs_vectors();schema_count,found=schema_vectors();errors+=found
    try:errors+=sidecar_self_test(binary,runtime)
    except Exception:errors.append('sidecar self-test: raised\n'+traceback.format_exc())
    for path in sorted((HERE/'vectors').glob('session-*.json')):
        try:n,e,k=run_file(path,(binary,runtime))
        except Exception:n,e,k=0,[path.name+': raised\n'+traceback.format_exc()],None
        total+=n;errors+=e;files.append({'file':path.name,'steps':n,'errors':len(e)})
        if k:kept.append(k)
    result={'successful':not errors and total>0,'transport':'unix-socket','sidecar':str(binary),'vectors':total,'files':files,
            'runner_checks':{'jcs':jcs_count,'envelope_schema':schema_count},'errors':errors,'kept_stores':kept}
    (WORK/'test-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2));return 0 if result['successful'] else 1

def main():
    global WORK
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--sidecar',type=Path,required=True,help='Go sidecar executable; run the session vectors through its socket')
    p.add_argument('--runtime',type=Path,required=True,help='pinned CEL executable for the sidecar')
    p.add_argument('--work',type=Path,help='work directory; the default is PROTOCOL_TEST_WORK or a new temporary directory')
    a=p.parse_args()
    if a.work:WORK=a.work.resolve()
    return sidecar_main(a.sidecar.resolve(),a.runtime.resolve())
if __name__=='__main__':raise SystemExit(main())
