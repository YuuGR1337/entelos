#!/usr/bin/env python3
"""A consumer that talks to the Go sidecar only, through its Unix sockets.

The example starts three sidecars on one Git store. Each sidecar has one trusted host context:
author, runner and reviewer. The client imports no lifecycle code. It sends wire protocol 1.0
frames and does propose, qualify, decide, activate, load and invoke on the record-filter fixture.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import stat
import struct
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / 'src/cel-lifecycle/fixtures/lifecycle-scenario.json'
SCHEMA = ROOT / 'src/protocol/schema'
REF = re.compile(r'^sha256:[0-9a-f]{64}$')
MAX_FRAME = 1048576
FILES = {'manifest': 'manifest.json', 'environment': 'env.json', 'source': 'rule.cel', 'fixed': 'fixed.json'}
MIMES = {'manifest': 'application/vnd.cel-handoff.bundle+json', 'environment': 'application/json',
         'source': 'text/plain', 'fixed': 'application/json'}


def encode(value):
    """The record codec of the bundle files: sorted keys, compact separators and a final newline."""
    return (json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':')) + '\n').encode()


def content_ref(mime, raw):
    return 'sha256:' + hashlib.sha256(mime.encode() + b'\0' + raw).hexdigest()


def bundle(directory, allowed_state):
    """The prepared record bundle with the fixed allowed_state of one fixture table."""
    data = {key: (directory / name).read_bytes() for key, name in FILES.items()}
    data['fixed'] = encode({'allowed_state': allowed_state})
    manifest = json.loads(data['manifest'])
    for key, field in [('environment', 'environment_ref'), ('source', 'source_ref'), ('fixed', 'fixed_ref')]:
        manifest[field] = content_ref(MIMES[key], data[key])
    data['manifest'] = encode(manifest)
    return {key: value.decode() for key, value in data.items()}


class Sidecar:
    """One sidecar process. The socket path is relative to the socket directory, because a full
    path can be longer than the sun_path limit."""

    def __init__(self, binary, runtime, work, name, context):
        self.name, self.directory, self.socket = name, work / 'sockets', name + '.sock'
        self.log_path = work / (name + '-sidecar.log')
        (work / (name + '.json')).write_bytes(encode(context))
        self.log = open(self.log_path, 'wb')
        self.process = subprocess.Popen(
            [str(binary), '--socket', self.socket, '--repo', str(work / 'state.git'), '--context', str(work / (name + '.json')),
             '--runtime', str(runtime), '--work', str(work / ('sidecar-' + name)), '--schema', str(SCHEMA)],
            cwd=self.directory, stdout=subprocess.PIPE, stderr=self.log)
        line = self.process.stdout.readline()
        if json.loads(line or b'null') != {'socket': self.socket, 'status': 'listening'}:
            self.process.kill()
            self.process.wait()
            raise RuntimeError('%s sidecar did not start: %s' % (name, self.log_path.read_text()))

    def mode(self):
        return stat.S_IMODE(os.lstat(self.directory / self.socket).st_mode)

    def stop(self):
        """Stop with SIGTERM. Return the exit code and whether the socket file stays."""
        self.process.terminate()
        try:
            code = self.process.wait(timeout=30)
        except subprocess.TimeoutExpired:
            self.process.kill()
            code = self.process.wait()
        self.log.close()
        return code, os.path.lexists(self.directory / self.socket)


class Client:
    """A wire protocol 1.0 client on one connection."""

    def __init__(self, sidecar):
        self.sidecar, self.counter = sidecar, 0
        self.conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.conn.settimeout(60)
        here = os.getcwd()
        os.chdir(sidecar.directory)
        try:
            self.conn.connect(sidecar.socket)
        finally:
            os.chdir(here)

    def read(self, size):
        data = b''
        while len(data) < size:
            chunk = self.conn.recv(size - len(data))
            if not chunk:
                raise ConnectionError('the sidecar closed the connection')
            data += chunk
        return data

    def call(self, operation, body):
        self.counter += 1
        request = {'protocol': '1.0', 'request_id': '%s-%d' % (self.sidecar.name, self.counter), 'operation': operation, 'body': body}
        payload = json.dumps(request, separators=(',', ':'), ensure_ascii=False).encode()
        self.conn.sendall(struct.pack('>I', len(payload)) + payload)
        size = struct.unpack('>I', self.read(4))[0]
        if not 1 <= size <= MAX_FRAME:
            raise ValueError('response frame size %d is outside the bound' % size)
        response = json.loads(self.read(size))
        if response.get('request_id') != request['request_id'] or response.get('protocol') != '1.0':
            raise ValueError('the response does not echo the request: %r' % response)
        return request, response

    def close(self):
        self.conn.close()


class Run:
    def __init__(self):
        self.steps, self.checks, self.cursor = [], [], ''

    def check(self, name, passed, actual):
        self.checks.append({'name': name, 'passed': bool(passed), 'actual': actual})
        if not passed:
            raise AssertionError(name)

    def send(self, client, name, operation, body, expected='ok', write=True):
        if write:
            body = dict(body, expected_revision=self.cursor, request_key=name)
        request, response = client.call(operation, body)
        self.steps.append({'name': name, 'socket': client.sidecar.socket, 'request': request, 'response': response})
        self.check(name, response['status'] == expected, {'expected': expected, 'status': response['status'],
                                                         'diagnostic': response.get('diagnostic')})
        if response['status'] != 'ok':
            return response
        if write:
            self.cursor = response['result']['cursor']
            return response['result']['result']
        return response['result']


def contexts(fixture):
    common = {'scope': fixture['scope'], 'runner_actor': 'fixture-runner', 'worker': 'local-worker', 'functions': [fixture['function']]}
    return {'author': dict(common, actor='author', roles=['author']),
            'runner': dict(common, actor='fixture-runner', roles=['runner']),
            'reviewer': dict(common, actor='reviewer', roles=['reviewer', 'worker', 'caller'])}


def loop(run, clients, fixture, record_bundle):
    """Propose, qualify, decide, activate, load and invoke. The expected values come from the fixture."""
    author, runner, reviewer = clients['author'], clients['runner'], clients['reviewer']
    table, function, date = fixture['initial'], fixture['function'], '2026-10-15'
    interval = {'from': table['from'], 'until': table['until']}
    source = run.send(reviewer, 'source', 'source', dict(interval, function=function, supersedes=[], reason='initial requirement',
                      text='Invented source table: visible state %s; archived records are invisible.' % table['allowed_state']))
    # The cases state the expected values from the source table. They are not outputs of the rule.
    cases = [{'case': 'visible', 'inputs': {'state': table['allowed_state'], 'archived': False}, 'status': 'ok', 'value': True},
             {'case': 'other-state', 'inputs': {'state': 'closed', 'archived': False}, 'status': 'ok', 'value': False},
             {'case': 'archived', 'inputs': {'state': table['allowed_state'], 'archived': True}, 'status': 'ok', 'value': False}]
    plan = run.send(reviewer, 'plan', 'plan', {'source_ref': source['source_ref'], 'cases': cases, 'supersedes': [],
                                               'origin': 'Hand-written cases from the invented source table'})
    proposal = run.send(author, 'propose', 'propose', dict(interval, function=function, base_snapshot='', source_ref=source['source_ref'],
                        bundle=bundle(record_bundle, table['allowed_state']), reason='initial proposal'))
    report = run.send(runner, 'qualify', 'qualify', {'snapshot_ref': proposal['snapshot_ref'], 'plan_ref': plan['plan_ref']})
    run.check('qualification-passed', report['report']['status'] == 'passed', report['report']['status'])
    # The author socket has no reviewer role, so it cannot approve its own proposal.
    denied = run.send(author, 'author-decide', 'decide', {'report_ref': report['report_ref'], 'reason': 'self-approval'}, expected='forbidden')
    run.check('author-decide-stage', denied['diagnostic']['stage'] == 'authority', denied['diagnostic'])
    decision = run.send(reviewer, 'decide', 'decide', {'report_ref': report['report_ref'], 'reason': 'reviewed the passed report'})
    event = run.send(reviewer, 'activate', 'activate', {'decision_ref': decision['decision_ref'], 'targets': []})
    described = run.send(reviewer, 'describe', 'describe', {'function': function, 'date': date}, write=False)
    loaded = run.send(reviewer, 'load', 'load', {'function': function, 'date': date})
    inputs = {'state': table['allowed_state'], 'archived': False}
    answer = run.send(reviewer, 'invoke', 'invoke', {'function': function, 'date': date, 'expected_bundle_ref': proposal['bundle_ref'], 'inputs': inputs})
    run.check('answer-value', answer['actual'].get('value') is fixture['expected']['initial_october_ready'],
              {'actual': answer['actual'], 'expected': fixture['expected']['initial_october_ready']})
    selection = answer['receipt'].get('selection', {})
    run.check('receipt-binds-activated-revision', REF.match(answer['receipt_ref']) and selection.get('snapshot_ref') == proposal['snapshot_ref']
              and selection.get('event_ref') == event['event_ref'] and loaded['snapshot_ref'] == proposal['snapshot_ref'],
              {'receipt_ref': answer['receipt_ref'], 'selection': selection, 'loaded': loaded})
    return {'source_ref': source['source_ref'], 'plan_ref': plan['plan_ref'], 'snapshot_ref': proposal['snapshot_ref'],
            'bundle_ref': proposal['bundle_ref'], 'report_ref': report['report_ref'], 'decision_ref': decision['decision_ref'],
            'event_ref': event['event_ref'], 'receipt_ref': answer['receipt_ref'], 'answer': answer['actual'], 'descriptor': described}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work', type=Path, required=True, help='new directory outside this source tree')
    p.add_argument('--sidecar', type=Path, required=True, help='entelos-sidecar executable')
    p.add_argument('--cel-binary', type=Path, required=True, help='pinned cel-handoff executable')
    p.add_argument('--record-bundle', type=Path, required=True, help='bundle from src/cel-handoff/tools/prepare.py')
    a = p.parse_args()
    w = a.work.resolve()
    if w == ROOT or ROOT in w.parents:
        p.error('work must be outside this source tree')
    if w.exists():
        p.error('%s already exists; choose a new path' % w)
    (w / 'sockets').mkdir(parents=True)
    # Only this user can open the socket directory.
    os.chmod(w / 'sockets', 0o700)
    fixture = json.loads(FIXTURE.read_text())
    run, sidecars, clients, summary, error = Run(), {}, {}, {}, None
    try:
        # Start the sidecars one at a time, so that only the first one makes the Git store.
        for name, context in contexts(fixture).items():
            sidecars[name] = Sidecar(a.sidecar.resolve(), a.cel_binary.resolve(), w, name, context)
            run.check(name + '-socket-mode', sidecars[name].mode() == 0o600, '%o' % sidecars[name].mode())
            clients[name] = Client(sidecars[name])
        summary = loop(run, clients, fixture, a.record_bundle.resolve())
    except Exception as e:
        error = '%s: %s' % (type(e).__name__, e)
    finally:
        for client in clients.values():
            client.close()
        for name, sidecar in sidecars.items():
            code, stays = sidecar.stop()
            run.checks.append({'name': name + '-shutdown', 'passed': code == 0 and not stays, 'actual': {'exit_code': code, 'socket_stays': stays}})
    # The client must not load lifecycle code. It reaches the lifecycle only through the sockets.
    loaded = sorted(set(sys.modules) & {'lifecycle', 'git_store', 'memory_store', 'cel_runtime', 'reference_adapter'})
    run.checks.append({'name': 'no-lifecycle-import', 'passed': not loaded, 'actual': loaded})
    passed = error is None and bool(run.checks) and all(x['passed'] for x in run.checks)
    result = {'kind': 'SidecarClientExample', 'status': 'passed' if passed else 'failed', 'error': error,
              'checks': run.checks, 'summary': summary, 'steps': run.steps, 'work': str(w)}
    (w / 'results.json').write_text(json.dumps(result, indent=2, ensure_ascii=False) + '\n')
    print(json.dumps({k: result[k] for k in ['status', 'error']} | {'checks': len(run.checks), 'steps': len(run.steps)}))
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
