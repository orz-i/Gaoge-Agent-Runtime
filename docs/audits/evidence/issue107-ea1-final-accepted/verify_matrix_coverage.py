#!/usr/bin/env python3
"""Read-only exact-name final-SHA replay verifier, not a ledger verdict writer.
Run: python3 /tmp/ea1-final-coverage-corrected/verify_matrix_coverage.py > /tmp/report.json
Exit 0=mechanical coverage complete, 2=pending, 1=invalid/missing final evidence.
Never executes tests, changes the repository, or imports historical PASS as final.
"""
import collections, csv, hashlib, io, json, pathlib, re, subprocess, sys
ROOT = pathlib.Path('/tmp/gaoge-runtime-issue107')
OUT = pathlib.Path(__file__).resolve().parent
SHA = '0a4009a8171f1726edaea895798d551051753400'
PREP = pathlib.Path('/tmp/ea1-final-prep/matrix-selectors.json')
LEDGER = 'docs/issue107-execution-acceptance-ledger.csv'
errors, warnings = [], []
def frozen(path):
    return subprocess.check_output(['git','show',SHA+':'+path],cwd=ROOT)
def counts(text):
    r = {k:collections.Counter() for k in ('run','pass','skip','fail')}
    for line in text.splitlines():
        m = re.match(r'^\s*=== RUN\s+(\S+)\s*$',line)
        if m: r['run'][m[1]] += 1
        m = re.match(r'^\s*--- (PASS|FAIL|SKIP):\s+(\S+)\s+\(',line)
        if m: r[m[1].lower()][m[2]] += 1
    return r
x = json.loads(PREP.read_text())
ledger_bytes = frozen(LEDGER)
rows = {r['cell_id']:r for r in csv.DictReader(io.StringIO(ledger_bytes.decode()))}
old_ledger = subprocess.check_output(['git','show',x['source_sha']+':'+LEDGER],cwd=ROOT)
# Preparation hashed the CRLF worktree; git stores the exact same CSV as LF.
prep_hashes={hashlib.sha256(old_ledger).hexdigest(), hashlib.sha256(old_ledger.replace(b'\r\n',b'\n').replace(b'\n',b'\r\n')).hexdigest()}
if x['ledger_sha256'] not in prep_hashes:
    errors.append('Original preparatory ledger hash mismatch (including explicit CRLF representation)')
old_rows={r['cell_id']:r for r in csv.DictReader(io.StringIO(old_ledger.decode()))}
ledger_deltas=[]
for cid,row in rows.items():
    if row!=old_rows.get(cid):
        ledger_deltas.append({'cell_id':cid,'before':old_rows.get(cid),'frozen':row})
        # Reviewed intervening MW P2 failure is retained, not promoted. All old
        # 132 PASS row bindings and six frozen-Host rows must remain identical.
        if cid!='EA1-MW-NEXT-P2' or old_rows[cid]['acceptance_verdict']!='UNRUN' or row['acceptance_verdict']!='FAIL':
            errors.append('Unreconciled ledger delta '+cid)
if set(rows)!=set(old_rows): errors.append('Ledger cell set changed')
if (OUT/'SOURCE_SHA').read_text().strip() != SHA: errors.append('Final run SOURCE_SHA mismatch')
selectors = {s['id']:s for s in x['selectors']}
# Each cell carries only its original evidence-log observations, NOT later static
# names caught by its broad historical regex. Names are crosslinked to registry.
requirements = collections.defaultdict(dict)
metadata = {}
source_cache = {}
def add(cell,module,name,n,origin,registry):
    key = module+'::'+name
    d = requirements[cell].setdefault(key,dict(module=module,name=name,required=n,origins=[],registries=[]))
    d['required'] = max(d['required'],n)
    if origin not in d['origins']: d['origins'].append(origin)
    if registry not in d['registries']: d['registries'].append(registry)
for cell in x['cells']:
    cid=cell['cell_id']; row=rows[cid]
    metadata[cid] = {'reviewer_note':row['reviewer_note'],'old_tested_sha':row['tested_runtime_sha'],'entrypoint':row['entrypoint'],'phase':row['phase']}
    if row['acceptance_verdict'] != 'PASS': errors.append(cid+': expected frozen old PASS')
    for sid in cell['selectors']:
        s=selectors[sid]
        for reg in s['static_tests']:
            top=reg['name']; path=reg['file']
            if path not in source_cache: source_cache[path]=frozen(path).decode()
            if not re.search(r'func\s+'+re.escape(top)+r'\s*\(',source_cache[path]):
                errors.append('Missing frozen registry '+path+'::'+top); continue
            for ob in s['historical_observations'].get(top,[]):
                log=ob['log']
                if log not in cell['evidence_logs']: continue
                h=x['historical_logs'][log]
                raw=(ROOT/log).read_bytes()
                if hashlib.sha256(raw).hexdigest()!=h['sha256']:
                    errors.append('Historical crosslink changed: '+log); continue
                original=counts(raw.decode())
                # Historical leaf names must themselves have real RUN and PASS;
                # recorded subtests alone or a parent PASS are insufficient.
                names=[top]+ob['subtests']
                for name in names:
                    if not original['run'][name] or not original['pass'][name] or original['skip'][name] or original['fail'][name]:
                        errors.append(cid+': invalid original dynamic obligation '+log+'::'+name); continue
                    n=max(int(s['count']),original['run'][name],original['pass'][name])
                    add(cid,s['module'],name,n,log,path)
    if not requirements[cid]: errors.append(cid+': no historical exact RUN/PASS crosslink')
# Explicit MW-NEXT leaves read against frozen suite. These are new obligations,
# not borrowed from a top-level suite or the old 132 PASS observations.
mwfile='go/agent-runtime/conformance/ea1_middleware_next_lifetime.go'
mw=frozen(mwfile).decode()
for token in ['"create-denied", "stale-admission", "duplicate", "foreign-model"','"run", "model", "tool"','"async", "unknown", "panic"','"P0/preflight"','"P2/model/swallowed-inner"','"P3/durable-before-whole-return"','"P4/mark-ack-lost"','"P5/deadline-before-new-send"','"P1/default-owner"']:
    if token not in mw: errors.append('MW frozen source changed; manually reconcile leaves: '+token)
mwregs=[('go/agent-runtime','memory/ea1_middleware_next_lifetime_test.go','TestEA1MiddlewareNextLifetimeMemory',1),('go/agent-runtime-sqlite','ea1_middleware_next_lifetime_test.go','TestEA1MiddlewareNextLifetimeNativeSQLite',1),('go/agent-runtime-postgres','ea1_middleware_next_lifetime_test.go','TestEA1MiddlewareNextLifetimeGORM',1),('go/agent-runtime-postgres','ea1_middleware_next_lifetime_real_test.go','TestEA1RealPostgresMiddlewareNextLifetime',3)]
for phase in range(6):
    cid='EA1-MW-NEXT-P'+str(phase)
    metadata[cid]={'reviewer_note':rows[cid]['reviewer_note'],'frozen_verdict':rows[cid]['acceptance_verdict'],'scope':'actual Agent Root/Child, all three Next chains where defined; not arbitrary async completion or cross-process registry survival'}
    for mod,file,top,n in mwregs:
        reg=mod+'/'+file; text=frozen(reg).decode()
        if not re.search(r'func\s+'+top+r'\(',text) or 'RunEA1MiddlewareNextLifetime' not in text: errors.append('MW registry mismatch '+reg)
        add(cid,mod,top,n,'new frozen MW suite',reg)
        for route in ('root','child'):
            leaves={0:['create-denied','stale-admission','duplicate','foreign-model']+(['preflight'] if route=='child' else []),1:['run','model','tool','default-owner'],2:[c+'/'+mode for c in ('run','model','tool') for mode in ('async','unknown','panic')]+['model/swallowed-inner'],3:['durable-before-whole-return'],4:['mark-ack-lost'],5:[c+'/async-election' for c in ('run','model','tool')]+['deadline-before-new-send']}[phase]
            for leaf in leaves: add(cid,mod,top+'/'+route+'/P'+str(phase)+'/'+leaf,n,'new frozen MW phase leaf',reg)
# Derive final log whitelist from fixed execution plan, never count broad logs
# such as check-no-race, coverage or old batches toward repetition requirements.
modules=sorted({d['module'] for rs in requirements.values() for d in rs.values()})
expected={ 'race-'+m.removeprefix('go/').replace('/','_'):m for m in modules }
for m in ('go/agent-runtime-postgres','go/agent-runtime-harness-postgres'):
    for i,_ in enumerate(x['modules'][m]['repeat_plan']): expected['matrix-extra-'+m.split('/')[-1]+'-'+str(i)]=m
expected['matrix-attempt-extra']='go/agent-runtime'
observed={m:{k:collections.Counter() for k in ('run','pass','skip','fail')} for m in modules}
sources=collections.defaultdict(list); log_reports=[]; pending=[]
for stem,mod in expected.items():
    log=OUT/(stem+'.log'); ex=OUT/(stem+'.exit')
    if not log.exists() or not ex.exists():
        pending.append(stem);log_reports.append({'log':str(log),'status':'PENDING'});continue
    body=log.read_text(); problems=[]
    if re.findall(r'^SOURCE_SHA=(.*)$',body,re.M)!=[SHA]: problems.append('source SHA')
    if ex.read_text().strip()!='0' or re.findall(r'^EXIT_CODE=(-?\d+)\s*$',body,re.M)!=['0']: problems.append('exit')
    cmd=re.findall(r'^COMMAND=(.*)$',body,re.M)
    if len(cmd)!=1 or not re.search(r'go -C '+re.escape(mod)+r' test -race -count=[1235] -v ',cmd[0]): problems.append('race/module command')
    if re.search(r'^\s*--- FAIL:|^FAIL(?:\s|$)|WARNING: DATA RACE',body,re.M): problems.append('FAIL/race')
    c=counts(body)
    if not c['run']: problems.append('no dynamic RUN')
    # Full module log must contain a package-success footer, not just test lines.
    if not re.search(r'^ok\s+\S+\s+\d',body,re.M): problems.append('missing package completion')
    if problems:
        errors.append(stem+': '+', '.join(problems));log_reports.append({'log':str(log),'status':'INVALID','reasons':problems});continue
    for k in observed[mod]: observed[mod][k].update(c[k])
    for name in c['run']: sources[(mod,name)].append({'log':str(log),'run':c['run'][name],'pass':c['pass'][name],'skip':c['skip'][name],'fail':c['fail'][name]})
    log_reports.append({'log':str(log),'status':'VALID_COMPLETED','sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'skip_names_not_counted_as_coverage':list(c['skip'])})
cell_reports=[]
for cid,rs in sorted(requirements.items()):
    details=[]; bad=False; absent=False
    for d in rs.values():
        c=observed[d['module']];name=d['name'];n=d['required']
        hit={k:c[k][name] for k in c}
        status='COVERED'
        if hit['skip'] or hit['fail']: status='FAIL';bad=True
        elif hit['run']<n or hit['pass']<n or hit['run']!=hit['pass']:
            status='PENDING' if pending else 'MISSING';absent=True
        details.append(dict(d,observed=hit,status=status,final_evidence=sources[(d['module'],name)]))
    status='FAIL' if bad else ('PENDING' if absent and pending else 'MISSING' if absent else 'MECHANICALLY_COVERED')
    cell_reports.append({'cell_id':cid,'status':status,'limitations':metadata[cid],'requirements':details})
for cid,r in rows.items():
    if cid.startswith('EA1-H-FROZEN-'):
        if r['acceptance_verdict']!='UNRUN': errors.append(cid+': frozen Host verdict changed')
        cell_reports.append({'cell_id':cid,'status':'INDEPENDENT_UNRUN','reviewer_note':r['reviewer_note']})
if len(requirements)!=138: errors.append('Expected 132 old plus six MW cells, got '+str(len(requirements)))
status='INVALID' if errors or any(c['status'] in ('FAIL','MISSING') for c in cell_reports) else 'PENDING' if pending else 'MECHANICAL_REPLAY_COMPLETE_REVIEW_REQUIRED'
report={'status':status,'source_sha':SHA,'ledger_sha256':hashlib.sha256(ledger_bytes).hexdigest(),'selector_preparation_sha':x['source_sha'],'errors':sorted(set(errors)),'explicit_ledger_deltas':ledger_deltas,'pending_logs':pending,'logs':log_reports,'cells':cell_reports,'limits':['No ledger or acceptance verdict is changed. Historical batch/source PASS only supplies exact names, never final evidence.','Conservative per-cell historical batch obligations include recorded companion tests; exact semantic phase mapping remains original independent review responsibility.','Dynamic RUN/PASS does not prove actual backend identity, original source/receipt assertions, crash/reopen semantics, callback authenticity or negative branch quality; retain frozen source review and per-cell reviewer_note.','Six H-FROZEN cells, remote PR-head and non-Mac platform gates remain independently unexecuted; this is not full GO.','Only whitelisted final race/repetition logs count. Mandatory other local gates must be checked separately, including managed recovery/process suites.']}
print(json.dumps(report,indent=2))
sys.exit(1 if status=='INVALID' else 2 if status=='PENDING' else 0)
