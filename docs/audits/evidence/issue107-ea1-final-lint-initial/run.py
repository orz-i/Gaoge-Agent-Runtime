import os,pathlib,subprocess,sys,json,re,shutil,time
root='/tmp/gaoge-runtime-issue107';out=pathlib.Path(__file__).parent
env=os.environ.copy();env['PATH']='/Users/mini/.gvm/go/bin:/Users/mini/.gvm/tools-bin:'+env.get('PATH','');env['TEST_POSTGRES_DSN']='postgres://phase1_test:phase1_disposable@127.0.0.1:57432/runtime_test?sslmode=disable'
env['TEST_REDIS_ADDR']='127.0.0.1:57379'
env['MAKEFLAGS']=''
sha=subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()
assert not subprocess.check_output(['git','status','--porcelain'],cwd=root,text=True).strip(), 'source must be frozen and clean'
assert not (out/'SOURCE_SHA').exists(), 'do not overwrite existing evidence'
(out/'SOURCE_SHA').write_text(sha)
def run(name,cmd):
 assert subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()==sha, 'source changed during final gates'
 if shutil.disk_usage(root).free < 2*1024**3:
  (out/'PAUSED_LOW_DISK').write_text(name+'\n');sys.exit(2)
 assert not (out/(name+'.log')).exists(), 'do not overwrite completed evidence'
 with (out/(name+'.log')).open('w') as f:
  f.write('SOURCE_SHA='+sha+'\nCOMMAND='+cmd+'\n');f.flush()
  r=subprocess.run(cmd,shell=True,cwd=root,env=env,stdout=f,stderr=subprocess.STDOUT)
  f.write('\nEXIT_CODE='+str(r.returncode)+'\n')
 (out/(name+'.exit')).write_text(str(r.returncode));print(name,r.returncode,flush=True);return r.returncode

# This runner is prepared in advance; it must not run before final source freeze.
run('environment',"go version; node --version; pnpm --version; golangci-lint version; df -h .; git status --short")
if run('preflight-lint','make go-lint') != 0:sys.exit(1)
check_rc=run('check-no-race','make check-no-race')
if check_rc:
 for target in ['metadata','agent-baseline-test','fmt-check','tidy-check','go-vet','go-test','go-lint','ts-lint','ts-typecheck','ts-test','ts-build','eval-smoke','benchmark-smoke','otel-e2e-smoke','a2a-product-check','release-check']:
  run('fallback-'+target,'make '+target)
mods=re.findall(r'"(go/[^"\n]+)"',pathlib.Path(root,'scripts/go-workspace.mjs').read_text().split('export const modules = [',1)[1].split('];',1)[0])
assert len(mods)==12
for m in mods:
 name='race-'+m.removeprefix('go/').replace('/','_')
 run(name,f'go -C {m} test -race -count=1 -v ./...')
selectors=json.loads(pathlib.Path('/tmp/ea1-final-prep/matrix-selectors.json').read_text())
for mod in ['go/agent-runtime-postgres','go/agent-runtime-harness-postgres']:
 for i,plan in enumerate(selectors['modules'][mod]['repeat_plan']):
  tests=list(plan['tests'])
  if mod.endswith('/agent-runtime-postgres') and i==0:tests.append('TestEA1RealPostgresMiddlewareNextLifetime')
  pattern='^('+'|'.join(sorted(set(tests)))+')$'
  count=plan.get('additional_count_after_verified_full_race',plan.get('additional_count_after_full_race_and_common_two_round_union'));assert count==2
  run('matrix-extra-'+mod.rsplit('/',1)[-1]+'-'+str(i),f"go -C {mod} test -race -count={count} -v ./... -run '{pattern}'")
run('matrix-attempt-extra',"go -C go/agent-runtime test -race -count=2 -v ./... -run '^TestEA1P0WorkflowAttemptLocalNoEffectAcrossRootAndChild$'")
for name,cmd in [('coverage','make coverage'),('security','make security'),('integration','make integration-test'),('otel','make integration-otel'),('beta-contract','node scripts/check-beta.mjs')]:run(name,cmd)
for name,mod,pattern in [
 ('core','go/agent-runtime','^TestManaged'),
 ('sqlite','go/agent-runtime-sqlite','^TestSQLiteManagedRecoveryStore$'),
 ('pg','go/agent-runtime-postgres','^TestRealPostgresManagedRecoveryStore$'),
 ('host','go/agent-runtime-harness','^TestManaged(HostMemoryRejectsUnissuedProofs|DispatchReceiptExactSchema)$'),
 ('hostpg','go/agent-runtime-harness-postgres','^TestManagedHostSQLRejectsUnissuedProofs$'),
 ('process','integration/continuation-e2e','^TestRealPostgres(ManagedAgentSessionRecoveryAcrossProcessCrash|ManagedHostReservationModesAfterProcessCrash|OpaqueManagedProcessCrashRemainsUnknown)$')]:
 prefix='GOWORK=off ' if name=='process' else ''
 run('managed-extra-'+name,prefix+f"go -C {mod} test -race -count=2 -v ./... -run '{pattern}'")
run('final-state','git status --short; df -h .')
print('All scheduled final checks finished. Inspect actual exits and suite coverage; this is not an acceptance verdict.',flush=True)
