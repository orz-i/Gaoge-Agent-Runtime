from pathlib import Path
import hashlib,json,re,shutil,subprocess,csv,io,collections
root=Path('/tmp/gaoge-runtime-issue107');out=Path(__file__).parent;sha='2d216518cfe48bc9e2ea6b02beb7733c246376a7'
assert (out/'SOURCE_SHA').read_text().strip()==sha
assert subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()==sha
assert not subprocess.check_output(['git','status','--porcelain'],cwd=root,text=True).strip()
assert 'All scheduled final checks finished.' in (out/'runner.out').read_text()
modules=re.findall(r'"(go/[^"\n]+)"',subprocess.check_output(['git','show',sha+':scripts/go-workspace.mjs'],cwd=root,text=True).split('export const modules = [',1)[1].split('];',1)[0]);assert len(modules)==12
expected={'dependency-restore','coverage-reducer-unit','coverage-profile-identity','environment','schema-fixture-smoke','dependency-presence','preflight-lint','check-no-race','coverage','security','integration','otel','beta-contract','final-state','matrix-attempt-extra','matrix-extra-agent-runtime-postgres-0','matrix-extra-agent-runtime-postgres-1','matrix-extra-agent-runtime-harness-postgres-0'}
expected|={'race-'+m.removeprefix('go/').replace('/','_') for m in modules}
expected|={'managed-extra-'+n for n in ('core','sqlite','pg','host','hostpg','process')}
exits=sorted(out.glob('*.exit'));assert len(expected)==36 and {p.stem for p in exits}==expected

manifest=[]
for p in exits:
 assert p.read_text().strip()=='0',p.name
 log=p.with_suffix('.log');s=log.read_text();assert re.findall(r'^SOURCE_SHA=(.*)$',s,re.M)==[sha]
 assert re.findall(r'^EXIT_CODE=(-?\d+)\s*$',s,re.M)==['0']
 cmd=re.findall(r'^COMMAND=(.*)$',s,re.M);assert len(cmd)==1
 manifest.append({'name':p.stem,'command':cmd[0],'sha256':hashlib.sha256(log.read_bytes()).hexdigest(),'exit':0})
r=json.loads((out/'matrix-coverage-completed.json').read_text());assert r['source_sha']==sha and r['status']=='MECHANICAL_REPLAY_COMPLETE_REVIEW_REQUIRED' and not r['errors'] and not r['pending_logs']
assert sum(c['status']=='MECHANICALLY_COVERED' for c in r['cells'])==138
assert sum(c['status']=='INDEPENDENT_UNRUN' for c in r['cells'])==6
ledger=subprocess.check_output(['git','show',sha+':docs/issue107-execution-acceptance-ledger.csv'],cwd=root)
assert hashlib.sha256(ledger).hexdigest()==r['ledger_sha256']
rows={x['cell_id']:x for x in csv.DictReader(io.StringIO(ledger.decode()))}
assert len(rows)==144 and sum(x['acceptance_verdict']=='PASS' for x in rows.values())==138
assert len(r['cells'])==144 and {c['cell_id'] for c in r['cells']}==set(rows)
for c in r['cells']:
 if c['cell_id'].startswith('EA1-H-FROZEN-'):
  assert c['status']=='INDEPENDENT_UNRUN' and rows[c['cell_id']]['acceptance_verdict']=='UNRUN'
  assert c['reviewer_note']==rows[c['cell_id']]['reviewer_note']
for log in r['logs']:
 path=Path(log['log']);assert path.parent.resolve()==out.resolve() and log['status']=='VALID_COMPLETED'
 assert hashlib.sha256(path.read_bytes()).hexdigest()==log['sha256']
p=root/'docs/audits/evidence/issue107-pr108-final-accepted';p.mkdir(exist_ok=False)
for f in sorted(out.iterdir()):
 if f.is_file() and (f.suffix in ('.log','.exit') or f.name in ['SOURCE_SHA','runner.out','run.py','compare_profiles.py','archive_final.py','verify_matrix_coverage.py','prepare_ledger_candidate.py','matrix-coverage-completed.json','coverage-summary.json','coverage-profile-comparison.json']):shutil.copyfile(f,p/f.name)
shutil.copytree(out/'profiles',p/'profiles')
# Optional explicitly separated historic supplement has no source headers;
# never include these records in the 36 exact-SHA gate manifest.
supp=Path('/tmp/ea1-ci-managed-fixed');supp_manifest=[]
if supp.exists():
 target=p/'historical-directed-supplement';target.mkdir()
 for stem in ('memory','postgres','sqlite','repro','core-lint','pg-lint'):
  for suffix in ('.log','.exit'):
   f=supp/(stem+suffix);assert f.is_file()
   shutil.copyfile(f,target/f.name)
   supp_manifest.append({'path':str((target/f.name).relative_to(p)),'origin':str(f),'sha256':hashlib.sha256(f.read_bytes()).hexdigest(),'role':'historical supplement, source not mechanically attested, NOT a main gate'})
(target/'ORIGIN.txt').write_text('Copied verbatim from /tmp/ea1-ci-managed-fixed. Logs lack SOURCE_SHA headers. Historical directed supplement only, excluded from final same-SHA gate counts.\n') if supp.exists() else None
(p/'.gitattributes').write_text('*.log -text\n*.gz -text\n')
(p/'ORIGIN.txt').write_text('Source commit: '+sha+'\nMain evidence copied byte-for-byte from '+str(out)+' after all 36 gates and exact matrix replay completed. No ledger verdict is changed by this archive. Historical directed supplement, if present, is separately labelled and never counted as same-SHA evidence.\n')
(p/'COMMANDS.txt').write_text(''.join(x['name']+'\t'+x['command']+'\n' for x in manifest))
(p/'MANIFEST.json').write_text(json.dumps({'source_sha':sha,'gates':manifest,'status':'ACTUAL_GATES_PASS_INDEPENDENT_MATRIX_REVIEW_REQUIRED','runtime_cells':138,'host_frozen_unrun':6,'historical_supplement':supp_manifest,'manifest_scope':'All payload files before MANIFEST.json; SHA256SUMS.txt additionally hashes MANIFEST.json and excludes only itself','files':[{'path':str(f.relative_to(p)),'bytes':f.stat().st_size,'sha256':hashlib.sha256(f.read_bytes()).hexdigest()} for f in sorted(p.rglob('*')) if f.is_file()]},indent=2)+'\n')
(p/'SHA256SUMS.txt').write_text(''.join(hashlib.sha256(f.read_bytes()).hexdigest()+'  '+str(f.relative_to(p))+'\n' for f in sorted(p.rglob('*')) if f.is_file() and f.name!='SHA256SUMS.txt'))
print('Archived exact all-green evidence; no ledger change or independent acceptance implied:',p)
