#!/usr/bin/env python3
"""Run the protocol vectors against the reference adapter and a fresh local Host for each vector file."""
import io
import json
import os
from pathlib import Path
import struct
import sys
import tempfile

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
    """Split the output into frames. A partial frame is an error, not a crash."""
    out=[];stream=io.BytesIO(raw)
    while True:
        header=stream.read(4)
        if not header:return out
        if len(header)<4:raise ValueError('output ends inside a frame header')
        size=struct.unpack('>I',header)[0];body=stream.read(size)
        if len(body)<size:raise ValueError('output ends inside a frame payload')
        out.append(json.loads(body))

def run_file(path):
    doc=json.loads(path.read_text());errors=[];bound={}
    with tempfile.TemporaryDirectory(dir=WORK) as directory:
        host=Host(GitStore(Path(directory)/'store.git'),NoRuntime(),doc['context']);adapter=ra.Adapter(host)
        for step in doc['steps']:
            name=path.name+':'+step['name']
            # An exception in one step is a vector error. The runner continues with the next step.
            try:errors+=run_step(adapter,step,bound,name)
            except Exception as e:errors.append(name+': raised '+type(e).__name__+': '+ascii(str(e))[:200])
    return len(doc['steps']),errors

def run_step(adapter,step,bound,name):
    errors=[];expect=step['expect']
    if 'stream_hex' in step:
        data=bytes.fromhex(step['stream_hex'])
        reader=Chunked(data,step['read_size']) if 'read_size' in step else io.BytesIO(data)
        out=ShortWriter(step['write_size']) if 'write_size' in step else io.BytesIO()
        adapter.serve(reader,out)
        raw=out.getvalue();got=frames(raw)
        errors+=match(expect['responses'],got,bound,name)
        errors+=[name+': response exceeds the frame bound' for r in got if len(ra.canonical(r))>ra.MAX_FRAME]
        for r in got:
            errors+=[name+': not canonical'] if ra.canonical(r) not in raw else []
            errors+=shape(r,step.get('operation'))
        return errors
    if 'pad_to' in step:
        payload=padded(substitute(step['request'],bound),step['pad_to'])
        if len(payload)!=step['pad_to']['size']:errors.append(name+': padded payload has the wrong size')
    elif 'request' in step:payload=ra.canonical(substitute(step['request'],bound))
    elif 'payload_text' in step:payload=step['payload_text'].encode('utf-8')
    else:payload=bytes.fromhex(step['payload_hex'])
    response,close=adapter.frame(payload)
    if len(ra.canonical(response))>ra.MAX_FRAME:errors.append(name+': response exceeds the frame bound')
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

def internal_errors():
    """An unexpected Host failure gives one internal_error frame with no exception text."""
    errors=[];request=ra.canonical({'protocol':'1.0','request_id':'int','operation':'inspect','body':{}})
    want={'protocol':'1.0','request_id':'int','status':'internal_error','diagnostic':{'code':'internal_error','stage':'host','message':{'$type':'string'}}}
    for host in (RaisingHost(),FloatHost()):
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

def main():
    WORK.mkdir(parents=True,exist_ok=True);total=0;errors=coverage()+bounds()+internal_errors()
    n,e=jcs_vectors();total+=n;errors+=e
    n,e=schema_vectors();total+=n;errors+=e
    for path in sorted((HERE/'vectors').glob('session-*.json')):
        n,e=run_file(path);total+=n;errors+=e
    result={'successful':not errors and total>0,'vectors':total,'errors':errors}
    (WORK/'test-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2));return 0 if result['successful'] else 1
if __name__=='__main__':raise SystemExit(main())
