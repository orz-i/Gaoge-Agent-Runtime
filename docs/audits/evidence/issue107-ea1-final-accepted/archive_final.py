from pathlib import Path
import hashlib,json,re,shutil,subprocess
root=Path('/tmp/gaoge-runtime-issue107');out=Path(__file__).parent;sha='0a4009a8171f1726edaea895798d551051753400'
assert (out/'SOURCE_SHA').read_text().strip()==sha
assert subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()==sha
assert not subprocess.check_output(['git','status','--porcelain'],cwd=root,text=True).strip()
assert 'All scheduled final checks finished.' in (out/'runner.out').read_text()
exits=sorted(out.glob('*.exit'));assert len(exits)==35,len(exits)
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
p=root/'docs/audits/evidence/issue107-ea1-final-accepted';p.mkdir(exist_ok=False)
for f in sorted(out.iterdir()):
 if f.is_file() and (f.suffix in ('.log','.exit') or f.name in ['SOURCE_SHA','runner.out','run.py','compare_profiles.py','archive_final.py','verify_matrix_coverage.py','prepare_ledger_candidate.py','matrix-coverage-completed.json','mw-final-notes.json','coverage-summary.json','coverage-profile-comparison.json']):shutil.copyfile(f,p/f.name)
shutil.copytree(out/'profiles',p/'profiles')
(p/'MANIFEST.json').write_text(json.dumps({'source_sha':sha,'gates':manifest,'status':'ACTUAL_GATES_PASS_INDEPENDENT_MATRIX_REVIEW_REQUIRED','runtime_cells':138,'host_frozen_unrun':6},indent=2)+'\n')
(p/'COMMANDS.txt').write_text(''.join(x['name']+'\t'+x['command']+'\n' for x in manifest))
(p/'SHA256SUMS').write_text(''.join(hashlib.sha256(f.read_bytes()).hexdigest()+'  '+str(f.relative_to(p))+'\n' for f in sorted(p.rglob('*')) if f.is_file() and f.name!='SHA256SUMS'))
print('Archived exact all-green evidence; no ledger change or independent acceptance implied:',p)
