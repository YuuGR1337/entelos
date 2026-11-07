#!/usr/bin/env python3
"""Classify changed paths as code, security-relevant or docs. CI and cross_review.py share these rules."""
import argparse
from fnmatch import fnmatch
import json
import subprocess
import sys

# A code change runs the full suite. A docs-only change runs the project check only.
CODE=['src/*','examples/*','tests/*','tools/*','verify.py','Makefile','.github/workflows/ci.yml']
# A security-relevant change needs a recorded security review before merge.
# Almost every runtime source runs commands, Git or CEL, so all executable sources count. Tests do not.
SOURCES=['src/*.go','src/*.py','examples/*.py']
NOT_SECURITY=['*_test.go','*/test_*.py','tests/*']
# These paths count at any directory, also under tests/. An agent loads instruction files and settings from any directory depth.
ALWAYS=['.github/*','tools/*','verify.py','Makefile','docs/STANDARD.md','*/go.mod','*/go.sum','docs/REVIEW.md','SECURITY.md','CONSTITUTION.md',
    'AGENTS.md','CLAUDE.md','GEMINI.md','.agents/*','.claude/*','.codex/*','.gemini/*',
    '*/AGENTS.md','*/CLAUDE.md','*/GEMINI.md','*/.agents/*','*/.claude/*','*/.codex/*','*/.gemini/*']

def changed(base,head,cwd=None):
    """List both ends of each move, so a move out of src/ still counts. -z keeps unusual names unquoted."""
    out=subprocess.run(['git','diff','--no-renames','--name-only','-z',base+'...'+head],cwd=cwd,capture_output=True,text=True)
    if out.returncode:sys.exit(out.stderr.strip() or 'git diff failed')
    return [x for x in out.stdout.split('\0') if x]

def match(path,patterns):return any(fnmatch(path,x) for x in patterns)
def classify(paths):
    code=[x for x in paths if match(x,CODE)]
    security=[x for x in paths if match(x,ALWAYS) or (match(x,SOURCES) and not match(x,NOT_SECURITY))]
    return {'code':bool(code),'security':bool(security),'docs_only':bool(paths) and not code and not security,'code_paths':code,'security_paths':security,'paths':paths}

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('base');p.add_argument('head',nargs='?',default='HEAD')
    p.add_argument('--github-output',help='append code=, security= and docs_only= lines to this file')
    a=p.parse_args()
    result=classify(changed(a.base,a.head))
    if a.github_output:
        with open(a.github_output,'a') as f:
            for key in ['code','security','docs_only']:f.write(key+'='+str(result[key]).lower()+'\n')
    print(json.dumps(result,indent=2))
