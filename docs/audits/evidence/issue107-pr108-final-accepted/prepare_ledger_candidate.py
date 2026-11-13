#!/usr/bin/env python3
"""Prepare ONLY a temporary, unapproved ledger candidate AFTER evidence archive.
Usage: python3 prepare_ledger_candidate.py /absolute/path/final-coverage-report.json
Does not execute tests or change repository/actual ledger. Never copies candidate
back. Independent review is still required even when all mechanical gates pass.
"""
import collections,csv,hashlib,io,json,pathlib,re,subprocess,sys
SHA='2d216518cfe48bc9e2ea6b02beb7733c246376a7'
ROOT=pathlib.Path('/tmp/gaoge-runtime-issue107')
OUT=pathlib.Path('/tmp/ea1-pr108-final').resolve()
REL='docs/audits/evidence/issue107-pr108-final-accepted'
ARCHIVE=ROOT/REL
LEDGER='docs/issue107-execution-acceptance-ledger.csv'
def frozen(path):return subprocess.check_output(['git','show',SHA+':'+path],cwd=ROOT)
def digest(b):return hashlib.sha256(b).hexdigest()
def require(ok,msg):
    if not ok:raise SystemExit('REFUSED: '+msg)
require(len(sys.argv)==2,'supply completed final verifier JSON')
report_path=pathlib.Path(sys.argv[1]).resolve()
report=json.loads(report_path.read_text())
require(report['source_sha']==SHA,'coverage source SHA')
require(report['status']=='MECHANICAL_REPLAY_COMPLETE_REVIEW_REQUIRED','coverage not complete')
require(not report['errors'] and not report['pending_logs'],'coverage errors/pending')
counts=collections.Counter(c['status'] for c in report['cells'])
require(counts=={'MECHANICALLY_COVERED':138,'INDEPENDENT_UNRUN':6},'need exactly 138 covered plus six independent UNRUN')
require((OUT/'SOURCE_SHA').read_text().strip()==SHA,'run source identity')
require(ARCHIVE.is_dir() and (ARCHIVE/'SOURCE_SHA').read_text().strip()==SHA,'archive must already exist with final source')
raw=frozen(LEDGER);text=raw.decode();rows=list(csv.DictReader(io.StringIO(text)))
fields=list(rows[0]);require(digest(raw)==report['ledger_sha256'],'frozen ledger/report mismatch')
byid={r['cell_id']:r for r in rows};coverage={c['cell_id']:c for c in report['cells']}
require(len(byid)==144 and set(byid)==set(coverage),'exact frozen 144-cell set')
require(sum(r['acceptance_verdict']=='PASS' for r in rows)==138,'current 138 PASS required')
# All main-runner scheduled gates, not merely whatever exits happen to exist.
module_source=frozen('scripts/go-workspace.mjs').decode()
modules=re.findall(r'"(go/[^"\n]+)"',module_source.split('export const modules = [',1)[1].split('];',1)[0])
require(len(modules)==12,'exact twelve modules')
expected={'dependency-restore','coverage-reducer-unit','coverage-profile-identity','environment','schema-fixture-smoke','dependency-presence','preflight-lint','check-no-race','coverage','security','integration','otel','beta-contract','final-state','matrix-attempt-extra','matrix-extra-agent-runtime-postgres-0','matrix-extra-agent-runtime-postgres-1','matrix-extra-agent-runtime-harness-postgres-0'}
expected|={'race-'+m.removeprefix('go/').replace('/','_') for m in modules}
expected|={'managed-extra-'+n for n in ('core','sqlite','pg','host','hostpg','process')}
actual={p.stem for p in OUT.glob('*.exit')}
require(len(expected)==36 and actual==expected,'exact 36 main-runner gates required: missing='+str(sorted(expected-actual))+' extra='+str(sorted(actual-expected)))
headers={};gate_evidence=[]
for stem in sorted(actual):
    exit_path=OUT/(stem+'.exit');log=OUT/(stem+'.log')
    require(exit_path.read_text().strip()=='0',stem+' nonzero exit')
    require(log.exists(),stem+' missing log')
    body=log.read_text();binary=log.read_bytes()
    require(re.findall(r'^SOURCE_SHA=(.*)$',body,re.M)==[SHA],stem+' source header')
    require(re.findall(r'^EXIT_CODE=(-?\d+)\s*$',body,re.M)==['0'],stem+' final exit footer')
    cmd=re.findall(r'^COMMAND=(.*)$',body,re.M);require(len(cmd)==1,stem+' command header')
    headers[stem]=cmd[0]
    for path in (log,exit_path):
        archived=ARCHIVE/path.name
        require(archived.exists() and archived.read_bytes()==path.read_bytes(),'archive missing or differs: '+path.name)
    gate_evidence.append({'log':REL+'/'+log.name,'sha256':digest(binary),'command':cmd[0]})
require('All scheduled final checks finished.' in (OUT/'runner.out').read_text(),'runner final completion missing')
# Check verifier's own admitted logs against exact archived final bytes.
for log in report['logs']:
    require(log['status']=='VALID_COMPLETED','verifier log not valid completed')
    path=pathlib.Path(log['log']);require(path.parent.resolve()==OUT,'foreign evidence directory')
    require(digest(path.read_bytes())==log['sha256'],'verifier log content changed: '+path.name)
new=[];diff=[]
for original in rows:
    r=dict(original);cid=r['cell_id'];c=coverage[cid]
    if cid.startswith('EA1-H-FROZEN-'):
        require(c['status']=='INDEPENDENT_UNRUN' and r['acceptance_verdict']=='UNRUN',cid+' frozen boundary')
        new.append(r);continue
    require(c['status']=='MECHANICALLY_COVERED',cid+' not covered')
    logs=set()
    for req in c['requirements']:
        require(req['status']=='COVERED',cid+' uncovered exact test')
        for ev in req['final_evidence']:
            require(ev['run']>0 and ev['pass']>0 and ev['skip']==0 and ev['fail']==0,cid+' invalid test evidence')
            p=pathlib.Path(ev['log']);require(p.parent.resolve()==OUT,'foreign exact-test log')
            require(p.stem in headers,'test log not main completed gate')
            logs.add(p.name)
    require(logs,cid+' no exact final evidence')
    ordered=sorted(logs)
    r['tested_runtime_sha']=SHA
    if cid.startswith('EA1-H-AC-'):r['tested_host_sha']=SHA
    r['command']=' ; '.join(dict.fromkeys(headers[pathlib.Path(n).stem] for n in ordered))
    r['evidence_log']=' ; '.join(REL+'/'+n for n in ordered)
    allowed={'tested_runtime_sha','evidence_log','command'}
    if cid.startswith('EA1-H-AC-'):allowed.add('tested_host_sha')
    require(original['acceptance_verdict']=='PASS',cid+' original verdict')
    for f in fields:
        if f not in allowed:require(r[f]==original[f],cid+' unauthorized field change: '+f)
    diff.append({'cell_id':cid,'changes':{f:{'old':original[f],'candidate':r[f]} for f in fields if original[f]!=r[f]}})
    new.append(r)
# Preserve H-FROZEN source rows byte-for-byte (not only parsed field equality).
raw_lines=text.splitlines(keepends=True)
frozen_lines={line.split(',',1)[0]:line for line in raw_lines if line.startswith('EA1-H-FROZEN-')}
require(len(frozen_lines)==6,'expected six single-line frozen source rows')
chunks=[raw_lines[0]]
for r in new:
    if r['cell_id'] in frozen_lines:chunks.append(frozen_lines[r['cell_id']]);continue
    b=io.StringIO(newline='');w=csv.DictWriter(b,fieldnames=fields,lineterminator='\n');w.writerow(r);chunks.append(b.getvalue())
candidate=''.join(chunks)
parsed=list(csv.DictReader(io.StringIO(candidate)))
require(len(parsed)==144 and all(a==b for a,b in zip(new,parsed)),'candidate roundtrip mismatch')
require(all(next(r for r in parsed if r['cell_id']==cid)==byid[cid] for cid in frozen_lines),'frozen row mutation')
output=OUT/'ledger-candidate.csv';summary=OUT/'ledger-candidate-summary.json'
require(not output.exists() and not summary.exists(),'do not overwrite an existing candidate/review artifact')
result={'status':'CANDIDATE_ONLY_AWAITING_INDEPENDENT_REVIEW_DO_NOT_COPY_TO_LEDGER','source_sha':SHA,'coverage_report':str(report_path),'coverage_report_sha256':digest(report_path.read_bytes()),'original_ledger_sha256':digest(raw),'candidate_sha256':digest(candidate.encode()),'candidate_counts':{'PASS':138,'UNRUN':6},'old_pass_count':138,'new_mw_candidates':0,'sqlite_version_label_changes':0,'host_sha_scope':'H-AC tested_host_sha identifies same-repository Runtime Harness/HostPG, NOT Gaoge. Other original host SHA fields unchanged.','historical_failures':'All original FAIL logs and records remain; no evidence deletion/rewrite. Previously recorded failures remain in their original archives; no verdict migration is performed.','independent_boundaries':['H-FROZEN rows unchanged','No remote PR-head checks or cross-platform GO inferred','Source/semantic review still required beyond mechanical exact-leaf replay'],'completed_gate_evidence':gate_evidence,'row_changes':diff}
with output.open('x') as f:f.write(candidate)
with summary.open('x') as f:json.dump(result,f,indent=2);f.write('\n')
print(json.dumps({'status':result['status'],'candidate':str(output),'summary':str(summary),'source_sha':SHA}))
