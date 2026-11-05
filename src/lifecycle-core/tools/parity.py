#!/usr/bin/env python3
"""Run the lifecycle scenario on the Python core and on the Go core, then compare.

Each core runs the same scenario in its own work directory. The two traces must
be equal after each content reference and each cursor is replaced by an alias
in order of first appearance. Records that do not contain the lifecycle build
(sources, plans and snapshots) must also have the same content references.
"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'cel-lifecycle'))
from git_store import encode, decode  # noqa: E402
from lifecycle import READS  # noqa: E402
from scenario import Driver, demonstrate  # noqa: E402

TOKEN = re.compile(r'^(sha256:[0-9a-f]{64}|[0-9a-f]{40})$')


class GoDriver(Driver):
    """The scenario driver with the Go JSONL host in place of cli.py."""

    def __init__(self, root, work, binary, scenario, host):
        super().__init__(root, work, binary, scenario)
        self.host = host

    def call(self, actor, op, name=None, expected='ok', **values):
        self.counter += 1
        q = {'op': op} | values
        if op not in READS:
            q.setdefault('expected_revision', self.cursor())
            q.setdefault('request_key', name or op + '-' + str(self.counter))
        p = subprocess.run([str(self.host), '--repo', str(self.repo), '--context', str(self.contexts[actor]),
                            '--runtime', str(self.binary), '--work', str(self.work)],
                           input=encode(q), capture_output=True, env=os.environ)
        try:
            r = decode(p.stdout)
        except Exception:
            r = {'status': 'process_error', 'stdout': p.stdout.decode(errors='replace'), 'stderr': p.stderr.decode(errors='replace')}
        self.trace.append({'name': name or op, 'actor': actor, 'request': q, 'process_exit': p.returncode,
                           'response': r, 'expected_status': expected})
        if r['status'] != expected:
            raise AssertionError(f'{name or op}: {r} expected {expected}; stderr={p.stderr!r}')
        return r.get('data', r)


def alias(value, names):
    """Replace each reference and cursor by its order of first appearance."""
    if isinstance(value, dict):
        return {alias(k, names): alias(value[k], names) for k in sorted(value)}
    if isinstance(value, list):
        return [alias(x, names) for x in value]
    if isinstance(value, str) and TOKEN.match(value):
        return names.setdefault(value, '<%d>' % len(names))
    return value


def run(driver, bundle):
    error, pins = None, {}
    try:
        pins = demonstrate(driver, bundle)
    except Exception as e:
        error = repr(e)
    return {'status': 'passed' if error is None else 'failed', 'error': error, 'pins': pins,
            'checks': driver.checks, 'trace': driver.trace}


def first_difference(a, b, path='$'):
    if type(a) is not type(b):
        return path
    if isinstance(a, dict):
        for k in sorted(set(a) | set(b)):
            if k not in a or k not in b:
                return path + '.' + str(k)
            found = first_difference(a[k], b[k], path + '.' + str(k))
            if found:
                return found
        return None
    if isinstance(a, list):
        if len(a) != len(b):
            return path + '.length'
        for i, (x, y) in enumerate(zip(a, b)):
            found = first_difference(x, y, '%s[%d]' % (path, i))
            if found:
                return found
        return None
    return None if a == b else path


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work', type=Path, required=True)
    p.add_argument('--runtime', type=Path, required=True, help='pinned CEL executable')
    p.add_argument('--bundle', type=Path, required=True)
    p.add_argument('--fixture', type=Path, required=True)
    p.add_argument('--host', type=Path, required=True, help='Go lifecycle-core executable')
    a = p.parse_args()
    a.work = a.work.resolve()
    a.work.mkdir(parents=True, exist_ok=True)
    fixture = decode(a.fixture.read_bytes())
    python = run(Driver(ROOT / 'cel-lifecycle', a.work / 'python', a.runtime, decode(a.fixture.read_bytes())), a.bundle)
    go = run(GoDriver(ROOT / 'cel-lifecycle', a.work / 'go', a.runtime, fixture, a.host.resolve()), a.bundle)
    left = alias({'checks': python['checks'], 'trace': python['trace']}, {})
    right = alias({'checks': go['checks'], 'trace': go['trace']}, {})
    difference = first_difference(left, right)
    shared = ['initial_snapshot', 'future_snapshot', 'correction_snapshot']
    same_refs = all(python['pins'].get(k) and python['pins'].get(k) == go['pins'].get(k) for k in shared)
    passed = python['status'] == 'passed' and go['status'] == 'passed' and difference is None and same_refs
    result = {'kind': 'GoCoreParity', 'status': 'passed' if passed else 'failed',
              'python': {'status': python['status'], 'error': python['error'], 'checks': len(python['checks'])},
              'go': {'status': go['status'], 'error': go['error'], 'checks': len(go['checks']), 'pins': go['pins'],
                     'repo': str(a.work / 'go' / 'state.git'), 'contexts': str(a.work / 'go')},
              'steps': len(go['trace']), 'first_difference': difference, 'same_snapshot_refs': same_refs,
              'scope': 'One scenario trace through each JSONL host. Content references of records that hold the lifecycle build differ by design.'}
    (a.work / 'parity.json').write_bytes(encode(result))
    (a.work / 'traces.json').write_bytes(encode({'python': left, 'go': right}))
    print(json.dumps({k: result[k] for k in ['status', 'steps', 'first_difference', 'same_snapshot_refs']}))
    return 0 if passed else 1


if __name__ == '__main__':
    raise SystemExit(main())
