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


if run('smoke-memory',"go -C go/agent-runtime test -count=1 -v ./memory -run '^TestManagedMemoryStoreConformance$'") != 0: sys.exit(1)
print('Memory fixture smoke passed; awaiting remaining directed run.',flush=True)
