"""Reference adapter: the versioned wire protocol in front of the local lifecycle Host."""
import json
from pathlib import Path
import struct
import sys

HERE=Path(__file__).resolve().parent
sys.path.insert(0,str(HERE.parent/'cel-lifecycle'))
from git_store import Fault  # noqa: E402

PROTOCOL='1.0'
MAJOR,MINOR=1,0
MAX_FRAME=1048576
MAX_MESSAGE=1024
SAFE_INT=2**53-1
SCHEMAS={p.name[:-len('.schema.json')]:json.loads(p.read_text()) for p in sorted((HERE/'schema').glob('*.schema.json'))}
ENVELOPE=SCHEMAS.pop('envelope')['$defs']
OPERATIONS=SCHEMAS

class ProtocolError(Exception):
    """A failure before the Host gets the request. close=True ends the connection."""
    def __init__(self,code,message,close=False):
        super().__init__(message);self.code,self.message,self.close=code,message,close

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
        keys=sorted(value,key=lambda k:k.encode('utf-16-be'))
        return '{'+','.join(jcs(k)+':'+jcs(value[k]) for k in keys)+'}'
    raise ValueError('value outside the protocol value domain: '+type(value).__name__)

def canonical(value):return jcs(value).encode('utf-8')

def parse(raw):
    """Strict JSON: UTF-8, one object, no duplicate keys, integers only."""
    def pairs(items):
        result={}
        for k,v in items:
            if k in result:raise ValueError('duplicate JSON key: '+k)
            result[k]=v
        return result
    def no_float(text):raise ValueError('numbers must be integers')
    def no_constant(text):raise ValueError('non-finite JSON number')
    try:
        value=json.loads(raw.decode('utf-8'),object_pairs_hook=pairs,parse_float=no_float,parse_constant=no_constant)
        canonical(value)
    except (ValueError,UnicodeError,RecursionError) as e:
        raise ProtocolError('invalid_request',str(e) or 'JSON nesting is too deep') from e
    if not isinstance(value,dict):raise ProtocolError('invalid_request','a request must be a JSON object')
    return value

def resolve_ref(ref,root):
    if not ref.startswith('#/$defs/'):raise ValueError('unsupported $ref: '+ref)
    return root[ref[len('#/$defs/'):]]

def validate(schema,value,where,root):
    """A small JSON Schema 2020-12 subset: the keywords that the files in schema/ use."""
    if '$ref' in schema:return validate(resolve_ref(schema['$ref'],root),value,where,root)
    kind=schema.get('type')
    checks={'object':lambda v:isinstance(v,dict),'array':lambda v:isinstance(v,list),'string':lambda v:isinstance(v,str),
            'boolean':lambda v:isinstance(v,bool),'integer':lambda v:isinstance(v,int) and not isinstance(v,bool)}
    if kind and not checks[kind](value):raise ProtocolError('invalid_request',where+' must be of type '+kind)
    if 'const' in schema and value!=schema['const']:raise ProtocolError('invalid_request',where+' must be '+json.dumps(schema['const']))
    if 'enum' in schema and value not in schema['enum']:raise ProtocolError('invalid_request',where+' is not an allowed value')
    if isinstance(value,str):
        if len(value)<schema.get('minLength',0):raise ProtocolError('invalid_request',where+' is too short')
        if 'maxLength' in schema and len(value)>schema['maxLength']:raise ProtocolError('invalid_request',where+' is too long')
        if 'pattern' in schema:
            import re
            if not re.search(schema['pattern'],value):raise ProtocolError('invalid_request',where+' does not match its pattern')
    if isinstance(value,list):
        if len(value)<schema.get('minItems',0):raise ProtocolError('invalid_request',where+' has too few items')
        for i,item in enumerate(value):
            if 'items' in schema:validate(schema['items'],item,where+'['+str(i)+']',root)
    if isinstance(value,dict):
        for name in schema.get('required',[]):
            if name not in value:raise ProtocolError('invalid_request','missing field: '+where+'.'+name)
        props=schema.get('properties',{});extra=schema.get('additionalProperties',True)
        for name in sorted(value):
            if name in props:validate(props[name],value[name],where+'.'+name,root)
            elif extra is False:raise ProtocolError('invalid_request','unexpected field: '+where+'.'+name)
            elif isinstance(extra,dict):validate(extra,value[name],where+'.'+name,root)

def check_version(value):
    if 'protocol' not in value:raise ProtocolError('invalid_request','missing field: protocol')
    version=value['protocol']
    validate(ENVELOPE['version'],version,'protocol',ENVELOPE)
    major,minor=(int(x) for x in version.split('.'))
    if major!=MAJOR or minor>MINOR:
        raise ProtocolError('unsupported_version','this receiver supports protocol '+PROTOCOL+' only')

class Adapter:
    def __init__(self,host):self.host=host
    def message(self,request):
        """Map one parsed request object to one response object."""
        rid=request.get('request_id')
        request_id=rid if isinstance(rid,str) and 1<=len(rid)<=128 else ''
        try:
            check_version(request)
            validate(ENVELOPE['request'],request,'request',ENVELOPE)
            op=request['operation']
            if op not in OPERATIONS:raise ProtocolError('invalid_request','unknown operation')
            schema=OPERATIONS[op]['$defs']
            validate(schema['request_body'],request['body'],'body',schema)
        except ProtocolError as e:return failure(request_id,e.code,'protocol',e.message)
        try:result=self.host.handle({'op':op,**request['body']})
        except Fault as e:return failure(request_id,e.code,e.stage,e.message)
        return {'protocol':PROTOCOL,'request_id':request_id,'status':'ok','result':result}
    def frame(self,payload):
        """Return (response object, close) for one frame payload."""
        if not payload:return failure('','invalid_frame','protocol','empty frame'),True
        try:request=parse(payload)
        except ProtocolError as e:return failure('',e.code,'protocol',e.message),e.close
        return limit(self.message(request)),False
    def serve(self,reader,writer):
        """Serve length-prefixed frames until end of stream or a closing error."""
        while True:
            header=read_exact(reader,4)
            if not header:return
            if len(header)<4:return write_frame(writer,failure('','invalid_frame','protocol','truncated frame header'))
            size=struct.unpack('>I',header)[0]
            if size>MAX_FRAME:return write_frame(writer,failure('','invalid_frame','protocol','frame exceeds the byte bound'))
            payload=read_exact(reader,size)
            if len(payload)<size:return write_frame(writer,failure('','invalid_frame','protocol','truncated frame payload'))
            response,close=self.frame(payload)
            write_frame(writer,response)
            if close:return

def read_exact(reader,size):
    """Read until size bytes or end of stream. A short read is not the end of the stream."""
    chunks=[];count=0
    while count<size:
        chunk=reader.read(size-count)
        if not chunk:break
        chunks.append(chunk);count+=len(chunk)
    return b''.join(chunks)

def failure(request_id,code,stage,message):
    if len(message)>MAX_MESSAGE:message=message[:MAX_MESSAGE-3]+'...'
    return {'protocol':PROTOCOL,'request_id':request_id,'status':code,'diagnostic':{'code':code,'stage':stage,'message':message}}

def limit(response):
    """If the canonical response exceeds the frame bound, return a response_too_large failure."""
    if len(canonical(response))<=MAX_FRAME:return response
    return failure(response.get('request_id',''),'response_too_large','protocol','the response exceeds the frame byte bound')

def write_frame(writer,response):
    body=canonical(limit(response));writer.write(struct.pack('>I',len(body))+body);writer.flush()
