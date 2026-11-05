"""Application-only PR payload projection and an explicit non-atomic audit sidecar."""
from dataclasses import dataclass
import sys
import re
from pathlib import Path
from support import LIFE,Fault,encode,decode,content_ref,envelope,fixture

PATHS={'action':('body','action'),'base_branch':('body','pull_request','base','ref'),'draft':('body','pull_request','draft')}
@dataclass(frozen=True)
class Ingress:
    verified:bool
    repository:str

def package():
    spec=fixture()
    return encode({'adapter_source':Path(__file__).read_text(),'support_source':Path(__file__).with_name('support.py').read_text(),
                   'codec_source':(LIFE/'lifecycle_client.py').read_text(),'python_version':sys.version,
                   'environment':spec['environment'],'caller_variables':spec['caller_variables']})
def build_ref():return content_ref(envelope('application/vnd.ci-projection.implementation',package()))

def project(raw,ingress):
    if ingress.verified is not True:raise Fault('unverified_ingress','host verification must precede projection','projection')
    if len(raw)>65536:raise Fault('invalid_envelope','envelope exceeds the local reference bound','projection')
    value=decode(raw)
    if not isinstance(value,dict) or set(value)!={'headers','body'}:raise Fault('invalid_envelope','expected headers/body only; verification is not an envelope field','projection')
    headers=value['headers']
    if not isinstance(headers,dict):raise Fault('invalid_envelope','headers must be an object','projection')
    canonical={}
    for key,values in headers.items():
        lower=key.lower()
        if lower in canonical:raise Fault('invalid_envelope','ambiguous case-variant header','projection')
        canonical[lower]=values
    event=canonical.get('x-github-event')
    if not isinstance(event,list) or len(event)!=1 or not isinstance(event[0],str):raise Fault('invalid_envelope','one event header value is required','projection')
    if event[0]!='pull_request':raise Fault('unsupported_event','only pull_request projection is installed','projection')
    try:
        if value['body']['repository']['full_name']!=ingress.repository:raise Fault('out_of_scope','host repository boundary differs','projection')
        projected={}
        spec=fixture();types={v['name']:v['type'] for v in spec['environment']['variables']}
        if set(PATHS)!=set(spec['caller_variables']):raise Fault('configuration_error','projection must cover the caller contract','projection')
        for name,path in PATHS.items():
            field=value
            for key in path:field=field[key]
            typ=types[name]
            if (typ=='bool' and type(field) is not bool) or (typ=='string' and not isinstance(field,str)):
                raise Fault('invalid_envelope','wrong type at '+'.'.join(path),'projection')
            projected[name]=field
        return projected
    except (KeyError,TypeError) as e:raise Fault('invalid_envelope','required nested payload field missing or malformed: '+str(e),'projection') from e

def link(raw,ingress,invocation,directory):
    inputs=project(raw,ingress);receipt=invocation['receipt']
    expected=content_ref(envelope('application/json',encode(inputs)))
    if receipt['inputs_ref']!=expected:raise Fault('audit_mismatch','receipt used different normalized inputs','application-audit')
    record={'kind':'ApplicationWebhookAudit','raw_ref':content_ref(envelope('application/vnd.ci-webhook+json',raw)),
            'adapter_ref':build_ref(),'ingress':'simulated host verification; no authentication implementation',
            'repository':ingress.repository,'normalized_inputs_ref':expected,'receipt_ref':invocation['receipt_ref']}
    directory=Path(directory);directory.mkdir(parents=True,exist_ok=True)
    body=encode(record);ref=content_ref(envelope('application/vnd.ci-audit+json',body))
    # A sidecar is application evidence. It is deliberately not claimed atomic with the Git receipt.
    (directory/(record['raw_ref'][7:]+'.json')).write_bytes(raw)
    (directory/(ref[7:]+'.json')).write_bytes(body)
    (directory/(record['adapter_ref'][7:]+'.json')).write_bytes(package())
    return {'audit_ref':ref,'audit':record}

def verify_link(linked,directory,ingress):
    if not isinstance(linked,dict) or not isinstance(linked.get('audit'),dict):raise Fault('invalid_record','audit link must be an object','application-audit')
    record=linked['audit'];directory=Path(directory)
    refs={'audit_ref':linked.get('audit_ref')}|{name:record.get(name) for name in ['raw_ref','adapter_ref','normalized_inputs_ref','receipt_ref']}
    if any(not isinstance(ref,str) or not re.fullmatch(r'sha256:[0-9a-f]{64}',ref) for ref in refs.values()):raise Fault('invalid_reference','audit dependencies require exact content references','application-audit')
    try:
        audit_body=(directory/(linked['audit_ref'][7:]+'.json')).read_bytes()
        if content_ref(envelope('application/vnd.ci-audit+json',audit_body))!=linked['audit_ref'] or audit_body!=encode(record):raise Fault('artifact_mismatch','audit record identity differs','application-audit')
        raw=(directory/(record['raw_ref'][7:]+'.json')).read_bytes()
        code=(directory/(record['adapter_ref'][7:]+'.json')).read_bytes()
    except OSError as e:raise Fault('artifact_unavailable','application audit dependency unavailable','application-audit') from e
    if content_ref(envelope('application/vnd.ci-webhook+json',raw))!=record['raw_ref'] or content_ref(envelope('application/vnd.ci-projection.implementation',code))!=record['adapter_ref']:
        raise Fault('artifact_mismatch','application audit bytes differ','application-audit')
    if record['adapter_ref']!=build_ref():raise Fault('implementation_unavailable','the exact projection implementation is not installed','application-audit')
    inputs=project(raw,ingress)
    if content_ref(envelope('application/json',encode(inputs)))!=record['normalized_inputs_ref']:raise Fault('audit_mismatch','reprojection differs','application-audit')
    return inputs
