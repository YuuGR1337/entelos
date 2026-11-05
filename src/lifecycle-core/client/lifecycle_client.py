"""A Python client of the Go JSONL host cmd/lifecycle-core.

This module has no Host and no store. It encodes requests, starts the Go host
for each request, and decodes the responses. The codec gives the same bytes
as the Go record codec (canon.go): sorted keys, no spaces, UTF-8 and a final
newline. read_store reads a store for evidence only. It never writes.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ROOT_REF = 'refs/lifecycle/state'
# The record format that the Go core writes. See RecordFormat in store.go.
RECORD_FORMAT = 'entelos.record/1'
# The operations that publish no state. They are the Reads of host.go.
READS = {'describe', 'resolve', 'inspect', 'record', 'receipt'}
FILENAMES = {'manifest': 'manifest.json', 'environment': 'env.json', 'source': 'rule.cel', 'fixed': 'fixed.json'}
MIMES = {'manifest': 'application/vnd.cel-handoff.bundle+json', 'environment': 'application/json',
         'source': 'text/plain', 'fixed': 'application/json'}


class Fault(Exception):
    def __init__(self, code, message, stage='lifecycle'):
        self.code, self.message, self.stage = code, message, stage
        super().__init__(message)

    def record(self):
        return {'status': self.code, 'diagnostic': {'code': self.code, 'stage': self.stage, 'message': self.message}}


def encode(value):
    return (json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False) + '\n').encode('utf-8')


def decode(raw):
    def pairs(items):
        result = {}
        for k, v in items:
            if k in result:
                raise Fault('invalid_record', 'duplicate JSON key: ' + k, 'codec')
            result[k] = v
        return result
    try:
        value = json.loads(raw.decode('utf-8'), object_pairs_hook=pairs,
                           parse_constant=lambda s: (_ for _ in ()).throw(ValueError('non-finite JSON')))
        encode(value)  # Refuse an unpaired Unicode surrogate too.
        return value
    except (ValueError, UnicodeError, TypeError, RecursionError) as e:
        raise Fault('invalid_record', str(e) or 'JSON nesting is too deep', 'codec') from e


def media(kind):
    return 'application/vnd.cel-lifecycle.' + kind + '+json'


def content_ref(body):
    return 'sha256:' + hashlib.sha256(body).hexdigest()


def envelope(mime, raw):
    return mime.encode() + b'\0' + raw


def git(repo, *args):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')} | {
        'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_TERMINAL_PROMPT': '0'}
    return subprocess.run(['git', '--git-dir=' + str(repo), *args], env=env, capture_output=True)


def root(repo):
    """Give the current root cursor, or '' before the first write."""
    p = git(repo, 'rev-parse', '--verify', ROOT_REF)
    return p.stdout.decode().strip() if p.returncode == 0 else ''


def read_store(repo):
    """Read the current root for evidence: the cursor, the state and each object as (media type, bytes)."""
    cursor = root(repo)
    if not cursor:
        return {'cursor': '', 'state': None, 'objects': {}}
    entries = {}
    for row in git(repo, 'ls-tree', '-z', cursor).stdout.split(b'\0'):
        if row:
            meta, name = row.split(b'\t', 1)
            entries[name.decode()] = meta.decode().split()[2]
    state = decode(git(repo, 'cat-file', 'blob', entries['state.json']).stdout)
    objects = {}
    for name, oid in sorted(entries.items()):
        if name.startswith('o-'):
            mime, raw = git(repo, 'cat-file', 'blob', oid).stdout.split(b'\0', 1)
            objects['sha256:' + name[2:]] = (mime.decode(), raw)
    return {'cursor': cursor, 'state': state, 'objects': objects}


def record(store, ref, kind):
    """Give the decoded record of kind from a read_store result. Use the record format rule of View.Get in store.go."""
    mime, raw = store['objects'][ref]
    if mime != media(kind):
        raise Fault('invalid_record', 'unexpected media type', 'client')
    value = decode(raw)
    if not isinstance(value, dict):
        raise Fault('invalid_record', 'record must be an object', 'client')
    if 'format' not in value:
        raise Fault('unsupported_record_format', 'record has no format field; this client reads only ' + RECORD_FORMAT, 'client')
    match = re.fullmatch(r'entelos\.record/([1-9][0-9]{0,8})', value['format']) if isinstance(value['format'], str) else None
    if match is None:
        raise Fault('invalid_record', 'record format is malformed', 'client')
    if value['format'] != RECORD_FORMAT:
        raise Fault('unsupported_record_format', 'record format major version ' + match.group(1) + ' is unknown', 'client')
    return value


def handle(host, repo, context, runtime, work, request):
    """Send one request to a new Go host process. Give the decoded response record and the process result."""
    p = subprocess.run([str(host), '--repo', str(repo), '--context', str(context), '--runtime', str(runtime), '--work', str(work)],
                       input=encode(request), capture_output=True, env=os.environ)
    try:
        response = decode(p.stdout)
    except Fault:
        response = {'status': 'process_error', 'stdout': p.stdout.decode(errors='replace'), 'stderr': p.stderr.decode(errors='replace')}
    return response, p


class Driver:
    """Send each request as a named actor to a new Go host process. Keep a trace of each request and response."""

    def __init__(self, work, binary, scenario, host):
        self.work, self.binary, self.fixture, self.host = work, binary, scenario, host
        self.work.mkdir(parents=True, exist_ok=True)
        self.repo = work / 'state.git'
        self.trace = []
        self.checks = []
        self.counter = 0
        self.contexts = {}
        roles = {'reviewer': ['reviewer', 'reader'], 'author': ['author'], 'runner': ['runner'], 'caller': ['caller'],
                 'worker': ['worker'], 'reader': ['reader'], 'observer': ['observer'], 'maintainer': ['maintainer'],
                 'other-runner': ['runner'], 'other-maintainer': ['maintainer']}
        for name, permissions in roles.items():
            context = {'actor': 'fixture-runner' if name == 'runner' else name, 'roles': permissions, 'scope': scenario['scope'],
                       'runner_actor': 'fixture-runner', 'worker': 'local-worker', 'functions': [scenario['function']]}
            path = work / (name + '.json')
            path.write_bytes(encode(context))
            self.contexts[name] = path

    def cursor(self):
        return root(self.repo)

    def call(self, actor, op, name=None, expected='ok', **values):
        self.counter += 1
        q = {'op': op} | values
        if op not in READS:
            q.setdefault('expected_revision', self.cursor())
            q.setdefault('request_key', name or op + '-' + str(self.counter))
        r, p = handle(self.host, self.repo, self.contexts[actor], self.binary, self.work, q)
        self.trace.append({'name': name or op, 'actor': actor, 'request': q, 'process_exit': p.returncode, 'response': r, 'expected_status': expected})
        if r['status'] != expected:
            raise AssertionError(f'{name or op}: {r} expected {expected}; stderr={p.stderr!r}')
        return r.get('data', r)

    def check(self, name, question, passed, actual):
        self.checks.append({'name': name, 'question': question, 'passed': bool(passed), 'actual': actual})
        if not passed:
            raise AssertionError(name)

    def result(self, value):
        return value['result']


def host_sources():
    """The core Go sources of the lifecycle host, relative to src/lifecycle-core. Tests are not in the list."""
    core = Path(__file__).resolve().parents[1]
    names = sorted(p.name for p in core.glob('*.go') if not p.name.endswith('_test.go'))
    return names + ['cmd/lifecycle-core/main.go', 'go.mod']
