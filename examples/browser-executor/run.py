#!/usr/bin/env python3
"""A browser executor answers away from the Host, and the Host ingests its log.

The Host runs in sidecars with the WebAssembly module as the engine, so qualification binds the
module build. The example then builds a pin from the Host records, runs the module in a headless
browser with a receipt log in IndexedDB, and ingests that log twice through a sidecar whose
worker is the browser executor. The expected answers come from the fixture and the source table.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
EXECUTOR = 'browser-1'


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


sidecar = module('sidecar_client', ROOT / 'examples/sidecar-client/run.py')
browser = module('browser_check', ROOT / 'src/cel-handoff/web/browser_check.py')


def input_ref(inputs):
    """The record codec digest of inputs, computed here without lifecycle or executor code."""
    body = (json.dumps(inputs, sort_keys=True, ensure_ascii=False, separators=(',', ':')) + '\n').encode()
    return 'sha256:' + hashlib.sha256(b'application/json\0' + body).hexdigest()


def session(run, clients, fixture, record_bundle, summary):
    """The browser job: a pin from the Host records, the bundle files and the invocations."""
    reviewer, table = clients['reviewer'], fixture['initial']
    event = run.send(reviewer, 'read-event', 'record', {'ref': summary['event_ref'], 'kind': 'event'}, write=False)
    snapshot = run.send(reviewer, 'read-snapshot', 'record', {'ref': summary['snapshot_ref'], 'kind': 'snapshot'}, write=False)
    pin = {'function': event['function'], 'scope': event['scope'], 'snapshot_ref': event['snapshot_ref'], 'from': event['from'],
           'until': event['until'], 'bundle_ref': snapshot['bundle_ref'], 'engine_build': snapshot['engine_build'],
           'stale_mode': snapshot['stale_mode'], 'revoked': False}
    visible, closed = {'state': table['allowed_state'], 'archived': False}, {'state': 'closed', 'archived': False}
    return pin, {
        'log_name': 'entelos-example-' + EXECUTOR, 'pin': pin, 'files': sidecar.bundle(record_bundle, table['allowed_state']),
        'include_inputs': False,
        'invokes': [{'name': 'visible', 'date': '2026-10-15', 'inputs': visible},
                    {'name': 'other-state', 'date': '2026-10-16', 'inputs': closed},
                    {'name': 'before-from', 'date': '2000-01-01', 'inputs': visible},
                    {'name': 'at-until', 'date': pin['until'], 'inputs': visible}],
        'concurrent': [{'name': 'concurrent-a', 'date': '2026-10-17', 'inputs': visible},
                       {'name': 'concurrent-b', 'date': '2026-10-17', 'inputs': closed}]}


def judge(run, page, pin, fixture, job):
    """Check the browser observations against the stated expectations."""
    s = page['session']
    run.check('browser-pin-loads', s['loaded'].get('ok', {}).get('bundle_ref') == pin['bundle_ref'], s['loaded'])
    by_name = {x['name']: x for x in s['invokes'] + s['concurrent']}
    expected = {'visible': fixture['expected']['initial_october_ready'], 'other-state': False, 'concurrent-a': True, 'concurrent-b': False}
    for name, value in expected.items():
        entry = by_name[name].get('ok', {})
        run.check('browser-answer-' + name, entry.get('response', {}).get('value') is value and entry.get('stale_reason') == ''
                  and entry.get('snapshot_ref') == pin['snapshot_ref'] and entry.get('response', {}).get('implementation_ref') == pin['engine_build'],
                  by_name[name])
    # The pin is fail_closed, so these dates give a refusal and no log entry.
    run.check('browser-refuses-before-from', by_name['before-from'].get('diagnostic', {}).get('code') == 'out_of_scope', by_name['before-from'])
    run.check('browser-refuses-at-until', by_name['at-until'].get('diagnostic', {}).get('code') == 'activation_expired', by_name['at-until'])
    cursors = [e['cursor'] for e in s['entries']]
    run.check('browser-log-cursors', cursors == [1, 2, 3, 4] and {by_name['concurrent-a']['ok']['cursor'], by_name['concurrent-b']['ok']['cursor']} == {3, 4},
              cursors)
    run.check('browser-log-persists', s['reopened_equal'], s['reopened_equal'])
    steps = {x['name']: x for x in job['invokes'] + job['concurrent']}
    run.check('browser-inputs-stay', all('inputs' not in e for e in s['entries']), [sorted(e) for e in s['entries']])
    run.check('browser-input-digests', all(by_name[n]['ok']['inputs_ref'] == input_ref(steps[n]['inputs']) for n in expected), list(expected))
    return s['entries']


def ingest(run, clients, entries, pin):
    """Ingest the browser log twice. The second ingest adds nothing."""
    client = clients['browser']
    first = run.send(client, 'ingest', 'ingest', {'entries': entries})
    again = run.send(client, 'ingest-again', 'ingest', {'entries': entries})
    run.check('ingest-adds-each-entry', first['added'] == 4 and first['log_cursor'] == 4 and first['executor_id'] == EXECUTOR, first)
    run.check('ingest-is-idempotent', again['added'] == 0 and again['receipts'] == first['receipts'], again)
    receipts = [run.send(client, 'receipt-%d' % i, 'receipt', {'receipt_ref': ref}, write=False) for i, ref in enumerate(first['receipts'], 1)]
    run.check('ingested-receipts', all(r['executor_id'] == EXECUTOR and r['log_cursor'] == i and r['mode'] == 'ingest' and r['stale'] is False
                                       and r['host_stale_reason'] == '' and r['selection']['snapshot_ref'] == pin['snapshot_ref']
                                       and r['selection']['engine_build'] == pin['engine_build'] and r['inputs_ref'] == entries[i - 1]['inputs_ref']
                                       for i, r in enumerate(receipts, 1)), receipts)
    return first


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work', type=Path, required=True, help='new directory outside this source tree')
    p.add_argument('--sidecar', type=Path, required=True, help='entelos-sidecar executable')
    p.add_argument('--cel-binary', type=Path, required=True, help='command that runs the WebAssembly module under Node')
    p.add_argument('--record-bundle', type=Path, required=True, help='bundle that prepare.py bound to the WebAssembly build')
    p.add_argument('--module', type=Path, required=True, help='the WebAssembly module')
    p.add_argument('--wasm-exec', type=Path, required=True, help='wasm_exec.js of the Go release that built the module')
    p.add_argument('--browser', help='Chromium executable; else ENTELOS_BROWSER or a known name')
    a = p.parse_args()
    w = a.work.resolve()
    if w == ROOT or ROOT in w.parents:
        p.error('work must be outside this source tree')
    if w.exists():
        p.error('%s already exists; choose a new path' % w)
    (w / 'sockets').mkdir(parents=True)
    os.chmod(w / 'sockets', 0o700)
    fixture = json.loads(sidecar.FIXTURE.read_text())
    run, sidecars, clients, summary, error, page, record = sidecar.Run(), {}, {}, {}, None, None, None
    contexts = sidecar.contexts(fixture)
    contexts['browser'] = dict(contexts['author'], actor=EXECUTOR, roles=['worker', 'reader'], worker=EXECUTOR)
    try:
        for name, context in contexts.items():
            sidecars[name] = sidecar.Sidecar(a.sidecar.resolve(), a.cel_binary.resolve(), w, name, context)
            clients[name] = sidecar.Client(sidecars[name])
        summary = sidecar.loop(run, clients, fixture, a.record_bundle.resolve())
        pin, job = session(run, clients, fixture, a.record_bundle.resolve(), summary)
        found = browser.find_browser(a.browser)
        run.check('browser-found', found is not None, found)
        (w / 'browser').mkdir()
        page, record = browser.run(a.module.resolve(), a.wasm_exec.resolve(), {'session': job}, w / 'browser', found, 180)
        run.check('browser-page', page is not None and not page['errors'] and page['vectors']['errors'] == []
                  and page['implementation_ref'] == pin['engine_build'], {'errors': (page or {}).get('errors'), 'vectors': (page or {}).get('vectors', {}).get('errors')})
        summary['ingest'] = ingest(run, clients, judge(run, page, pin, fixture, job), pin)
    except Exception as e:
        error = '%s: %s' % (type(e).__name__, e)
    finally:
        for client in clients.values():
            client.close()
        for name, process in sidecars.items():
            code, stays = process.stop()
            run.checks.append({'name': name + '-shutdown', 'passed': code == 0 and not stays, 'actual': {'exit_code': code, 'socket_stays': stays}})
    passed = error is None and bool(run.checks) and all(x['passed'] for x in run.checks)
    result = {'kind': 'BrowserExecutorExample', 'status': 'passed' if passed else 'failed', 'error': error, 'checks': run.checks,
              'browser': record, 'timing': (page or {}).get('timing'), 'user_agent': (page or {}).get('user_agent'),
              'page': page, 'summary': summary, 'steps': run.steps, 'work': str(w)}
    (w / 'results.json').write_text(json.dumps(result, indent=2, ensure_ascii=False) + '\n')
    print(json.dumps({k: result[k] for k in ['status', 'error', 'timing']} | {'checks': len(run.checks)}))
    return 0 if passed else 1


if __name__ == '__main__':
    sys.exit(main())
