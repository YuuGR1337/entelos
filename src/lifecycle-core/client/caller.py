#!/usr/bin/env python3
"""A caller needs only discovery, ordinary inputs and a retry key, not CEL or Git data.

The caller sends describe, then invoke, to the Go JSONL host cmd/lifecycle-core.
"""
import argparse
import sys
from lifecycle_client import encode,decode,handle

p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--host',required=True,help='Go lifecycle-core executable')
p.add_argument('--repo',required=True);p.add_argument('--context',required=True)
p.add_argument('--runtime',required=True);p.add_argument('--work',required=True)
p.add_argument('--key',required=True);p.add_argument('--function',default='record.visible')
p.add_argument('--date',default='2026-11-15')
p.add_argument('--inputs',default='{"state":"reviewed","archived":false}')
a=p.parse_args()
def call(request):return handle(a.host,a.repo,a.context,a.runtime,a.work,request)[0]
described=call({'op':'describe','function':a.function,'date':a.date})
if described['status']!='ok':print(encode(described).decode(),end='');raise SystemExit(1)
descriptor=described['data']
request={'op':'invoke','function':a.function,'date':a.date,'inputs':decode(a.inputs.encode()),
         'expected_bundle_ref':descriptor['callable']['bundle_ref'],'expected_revision':descriptor['cursor'],'request_key':a.key}
response=call(request)
sys.stdout.buffer.write(encode(response));raise SystemExit(response['status']!='ok')
