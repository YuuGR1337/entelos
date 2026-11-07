#!/usr/bin/env python3
"""Offline shared-harness layout, local-link and regular-source inventory check."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import sys

ROOT=Path(__file__).resolve().parents[1]
p=argparse.ArgumentParser(description=__doc__);p.add_argument('--write-checksums',action='store_true');a=p.parse_args()
required=['CONSTITUTION.md','AGENTS.md','CLAUDE.md','GEMINI.md','README.md','docs/SUPPORT.md','SECURITY.md',
    'docs/STATUS.md','docs/BACKLOG.md','docs/CONTRACTS.md','docs/STANDARD.md','Makefile','.agents/skills/decision-workflow/SKILL.md',
    'docs/REVIEW.md','.github/CODEOWNERS','.github/workflows/ci.yml','.github/workflows/pr-policy.yml','.github/workflows/pr-policy-rerun.yml','.github/workflows/claude-review.yml','.github/ISSUE_TEMPLATE/bug.yml','.github/ISSUE_TEMPLATE/workflow.yml','.github/ISSUE_TEMPLATE/config.yml']
errors=[];inventory={};links=0
for path in sorted(ROOT.rglob('*')):
    relative=path.relative_to(ROOT)
    if relative.parts[0]=='.git':continue
    name=relative.as_posix()
    if path.is_symlink():
        if name!='.claude/skills' or path.readlink().as_posix()!='../.agents/skills' or path.resolve()!=(ROOT/'.agents/skills').resolve():
            errors.append('unexpected or invalid symlink: '+name)
        continue
    if not path.is_file():continue
    if set(relative.parts)&{'__pycache__','.venv','node_modules'}:errors.append('generated source-side directory: '+name)
    if name!='checksums.json':inventory[name]=hashlib.sha256(path.read_bytes()).hexdigest()
    if path.suffix=='.md':
        for target in re.findall(r'\]\(([^)]+)\)',path.read_text()):
            if target.startswith(('https:','http:','#','mailto:')):continue
            links+=1
            if not (path.parent/target.split('#')[0]).exists():errors.append('broken local link: '+name+' -> '+target)
for name in required:
    if not (ROOT/name).is_file():errors.append('missing: '+name)
alias=ROOT/'.claude/skills'
if not alias.is_symlink():errors.append('missing shared skill link')
claude=(ROOT/'CLAUDE.md').read_text() if (ROOT/'CLAUDE.md').exists() else ''
if '@CONSTITUTION.md' not in claude or '@AGENTS.md' not in claude:errors.append('Claude entry point does not import shared rules')
gemini=(ROOT/'GEMINI.md').read_text() if (ROOT/'GEMINI.md').exists() else ''
if '@CONSTITUTION.md' not in gemini or '@AGENTS.md' not in gemini:errors.append('GEMINI.md does not import shared rules')
# Classification regressions: each path must keep its CI and security-review class.
sys.path.insert(0,str(ROOT/'tools'))
from classify_change import classify
for path,code,security in [('README.md',False,False),('src/lifecycle-core/host.go',True,True),
        ('src/lifecycle-core/gitstore.go',True,True),('src/lifecycle-core/client/lifecycle_client.py',True,True),('src/cel-handoff/handoff_test.go',True,False),('src/cel-handoff/go.sum',True,True),
        ('tools/classify_change.py',True,True),('Makefile',True,True),('docs/STANDARD.md',False,True),('.github/workflows/ci.yml',True,True),('.codex/config.toml',False,True),('GEMINI.md',False,True),
        ('docs/AGENTS.md',False,True),('src/cel-handoff/tools/prepare.py',True,True),('examples/ci-trigger/run.py',True,True),
        ('src/protocol/test_protocol.py',True,False),('src/lifecycle-core/scenario_test.go',True,False),('tests/test_export_boundaries.py',True,False),('docs/STATUS.md',False,False),('docs/REVIEW.md',False,True),('src/cel-lifecycle/.claude/settings.json',True,True),
        ('tests/AGENTS.md',True,True),('tests/CLAUDE.md',True,True),('tests/.claude/settings.json',True,True),('tests/go.mod',True,True),('src/cel-handoff/x_test.go',True,False)]:
    got=classify([path])
    if (got['code'],got['security'])!=(code,security):errors.append('classification changed for %s: code=%s security=%s'%(path,got['code'],got['security']))
# Review events default to the PR merge ref, so the Claude review must check out the default branch.
# The Claude review workflow keeps the design in docs/REVIEW.md. tools/claude_review.py owns the list of checks.
from claude_review import workflow_errors
errors+=['claude-review.yml '+x for x in workflow_errors((ROOT/'.github/workflows/claude-review.yml').read_text())]
# The licence record of the downloaded CLI must name the pinned version.
cli=re.search(r'CLI_VERSION: (\S+)\n',(ROOT/'.github/workflows/claude-review.yml').read_text())
if not cli or '| @anthropic-ai/claude-code-linux-x64 | %s |'%cli.group(1) not in (ROOT/'licenses/THIRD_PARTY.md').read_text():errors.append('licenses/THIRD_PARTY.md must record the Claude CLI version of claude-review.yml')
# The agy fallback must not load the maintainer's global allow rules or MCP servers.
review=(ROOT/'tools/cross_review.py').read_text()
for need in ["env['HOME']=home","'--sandbox'","'allow':[]"]:
    if need not in review:errors.append('cross_review.py must keep '+need)
if "'HOME'," in review:errors.append('cross_review.py must not pass the real HOME to agy')
# PR gate regressions: a fork needs acceptance, and a label counts only with an approval record for the current head and base.
from pr_policy import CLEAN,REVIEWED,decide
def event(labels,head='o/r',sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',base='main'):return {'action':'edited','repository':{'full_name':'o/r'},'pull_request':{'labels':[{'name':x} for x in labels],'head':{'sha':sha,'repo':{'full_name':head}},'base':{'ref':base}}}
ok={'accepted':{('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','main')},'security-reviewed':{('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','main')}}
for check,case,paths,complete,found,want in [('acceptance',event([]),[],True,{},(True,None)),('acceptance',event([],'f/r'),[],True,{},(False,None)),
        ('acceptance',event(['accepted'],'f/r'),[],True,ok,(True,None)),('acceptance',event(['accepted'],'f/r'),[],True,{},(False,'accepted')),
        ('acceptance',event(['accepted'],'f/r','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'),[],True,ok,(False,'accepted')),('acceptance',event(['accepted'],'f/r',base='dev'),[],True,ok,(False,'accepted')),
        ('security',event([]),['docs/STATUS.md'],True,{},(True,None)),('security',event([]),['docs/STATUS.md'],False,{},(False,None)),
        ('security',event([]),['tools/pr_policy.py'],True,ok,(False,None)),('security',event(['security-reviewed']),['tools/pr_policy.py'],True,ok,(True,None)),
        ('security',event(['security-reviewed'],sha='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'),['tools/pr_policy.py'],True,ok,(False,'security-reviewed')),
        ('security',event(['security-reviewed'],base='dev'),['tools/pr_policy.py'],True,ok,(False,'security-reviewed')),
        ('security',event(['security-reviewed']),['tools/pr_policy.py'],True,{'accepted':{('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','main')}},(False,'security-reviewed'))]:
    passed,_,drop=decide(check,case,paths,complete,found)
    if (passed,drop)!=want:errors.append('PR gate changed: %s %s %s'%(check,case['pull_request']['head']['sha'][:1],paths))
# A Codex security review counts only if it names the current head and found no security issues.
for case,reviews,want in [(event([]),[('aaaaaaaaaa',True)],(True,None)),(event([]),[('bbbbbbbbbb',True)],(False,None)),(event([]),[('aaaaaaaaaa',False)],(False,None)),
        (event([]),[('aaaaaaaaaa',True),('aaaaaaa',False)],(False,None)),(event([]),[('aaaaaaaaaa',False),('aaaaaaaaaa',True)],(True,None)),
        (event(['security-reviewed'],sha='a'*39+'b'),[('aaaaaaaaaa',True)],(True,'security-reviewed'))]:
    passed,_,drop=decide('security',case,['tools/pr_policy.py'],True,ok,reviews)
    if (passed,drop)!=want:errors.append('PR gate changed for a Codex security review: %s'%reviews)
# The clean verdict must be a full line, and the commit must have at least 10 hex digits.
for body,want in [('Security review completed. No security issues were found in this pull request.',True),('> quote: Security review completed. No security issues were found in this pull request.',False),('No security issues were found',False)]:
    if bool(CLEAN.search(body))!=want:errors.append('PR gate changed for the Codex clean verdict: %s'%body)
if REVIEWED.search('**Reviewed commit:** `aaaaaaa`') or not REVIEWED.search('**Reviewed commit:** `aaaaaaaaaa`'):errors.append('PR gate changed for the Codex commit prefix length.')
checksum=ROOT/'checksums.json'
if a.write_checksums:
    if errors:raise SystemExit(json.dumps({'passed':False,'errors':errors}))
    checksum.write_text(json.dumps(inventory,indent=2,sort_keys=True)+'\n')
elif not checksum.exists() or json.loads(checksum.read_text())!=inventory:
    errors.append('source checksum inventory differs; review intended edits, then regenerate')
result={'passed':not errors,'regular_source_files':len(inventory),'local_links_checked':links,
    'shared_skill_link':alias.is_symlink() and alias.resolve()==(ROOT/'.agents/skills').resolve(),
    'checksums_updated':a.write_checksums,'errors':errors,'scope':'Source/layout integrity only; no model, runtime, platform-compatibility or production result.'}
print(json.dumps(result,indent=2));raise SystemExit(bool(errors))
