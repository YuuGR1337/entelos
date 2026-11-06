#!/usr/bin/env python3
"""Run the executor module in a headless Chromium browser and collect the page result.

The script serves browser-check.html, its scripts, the module and a job on 127.0.0.1, starts the
browser with a new profile and waits for the page to post its result. It exits 1 if the page
gives no result, reports an error or fails an executor vector.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import threading

WEB = Path(__file__).resolve().parent
ROOT = WEB.parents[2]
VECTORS = ROOT / 'src/cel-lifecycle/conformance/executor'
TYPES = {'.html': 'text/html; charset=utf-8', '.mjs': 'text/javascript', '.js': 'text/javascript',
         '.wasm': 'application/wasm', '.json': 'application/json'}
CANDIDATES = ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser', 'chrome',
              '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome']
MAX_RESULT = 16 * 1024 * 1024


def content_ref(media, raw):
    return 'sha256:' + hashlib.sha256(media.encode() + b'\0' + raw).hexdigest()


def find_browser(name):
    for candidate in [name or os.environ.get('ENTELOS_BROWSER')] + ([] if name else CANDIDATES):
        if candidate and (path := shutil.which(candidate) or (candidate if os.access(candidate, os.X_OK) else None)):
            return path
    return None


def serve(files, received):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            name = self.path.lstrip('/') or 'browser-check.html'
            if name not in files:
                self.send_error(404)
                return
            body = files[name]
            self.send_response(200)
            self.send_header('Content-Type', TYPES[Path(name).suffix])
            self.send_header('Content-Length', str(len(body)))
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            size = int(self.headers.get('Content-Length', '0'))
            if self.path != '/result' or not 0 < size <= MAX_RESULT or 'result' in received:
                self.send_error(400)
                return
            received['result'] = json.loads(self.rfile.read(size))
            self.send_response(204)
            self.end_headers()
            received['event'].set()

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def run(module, wasm_exec, job, work, browser, timeout):
    """Give (result, record). result is the page result or None."""
    raw = module.read_bytes()
    job = dict(job, module_ref=content_ref('application/wasm', raw),
               vectors=[json.loads(p.read_text()) for p in sorted(VECTORS.glob('*.json'))])
    files = {name: (WEB / name).read_bytes() for name in ['browser-check.html', 'browser-check.mjs', 'entelos-executor.mjs', 'executor-vectors.mjs']}
    files |= {'wasm_exec.js': wasm_exec.read_bytes(), 'module.wasm': raw, 'job.json': json.dumps(job).encode()}
    received = {'event': threading.Event()}
    server = serve(files, received)
    url = 'http://127.0.0.1:%d/' % server.server_address[1]
    version = subprocess.run([browser, '--version'], capture_output=True, text=True, timeout=60).stdout.strip()
    args = [browser, '--headless=new', '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--disable-gpu',
            '--user-data-dir=%s' % (work / 'profile'), url]
    # An unprivileged Linux runner can lack the namespaces of the Chromium sandbox. The caller decides.
    if os.environ.get('ENTELOS_BROWSER_NO_SANDBOX') == '1':
        args.insert(1, '--no-sandbox')
    with open(work / 'browser.log', 'wb') as log:
        process = subprocess.Popen(args, stdout=log, stderr=log, start_new_session=True)
        try:
            received['event'].wait(timeout)
        finally:
            try:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=30)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            server.shutdown()
    record = {'browser': browser, 'version': version, 'args': args, 'url': url, 'module_ref': job['module_ref'],
              'module_bytes': len(raw), 'no_sandbox': '--no-sandbox' in args, 'exit_code': process.returncode}
    return received.get('result'), record


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--module', type=Path, required=True)
    p.add_argument('--wasm-exec', type=Path, required=True, help='wasm_exec.js of the Go release that built the module')
    p.add_argument('--job', type=Path, help='JSON object with an optional session; the script adds module_ref and the vectors')
    p.add_argument('--work', type=Path, required=True, help='new directory')
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--browser', help='Chromium executable; else ENTELOS_BROWSER or a known name')
    p.add_argument('--timeout', type=float, default=180)
    a = p.parse_args()
    a.work.mkdir(parents=True)
    browser = find_browser(a.browser)
    if not browser:
        a.out.write_text(json.dumps({'status': 'failed', 'error': 'no Chromium browser found; set ENTELOS_BROWSER'}) + '\n')
        return 1
    job = json.loads(a.job.read_text()) if a.job else {}
    result, record = run(a.module.resolve(), a.wasm_exec.resolve(), job, a.work.resolve(), browser, a.timeout)
    errors = ['the page posted no result in %ss' % a.timeout] if result is None else result['errors'] + result.get('vectors', {}).get('errors', ['no vector result'])
    out = {'status': 'failed' if errors else 'passed', 'errors': errors, 'browser': record, 'result': result}
    a.out.write_text(json.dumps(out, indent=1, ensure_ascii=False) + '\n')
    print(json.dumps({'status': out['status'], 'errors': errors[:5], 'version': record['version'],
                      'timing': (result or {}).get('timing')}))
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
