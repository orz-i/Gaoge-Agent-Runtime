from pathlib import Path
import gzip,hashlib,json,re,shutil,subprocess
root=Path('/tmp/gaoge-runtime-issue107');out=Path(__file__).parent
old=root/'docs/audits/evidence/issue107-ea1-final-coverage-initial/profiles';new=root/'coverage/go'
BASE='0a4009a8171f1726edaea895798d551051753400'
SHA='2d216518cfe48bc9e2ea6b02beb7733c246376a7'
assert subprocess.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()==SHA
changed=set(subprocess.check_output(['git','diff','--name-only',BASE,SHA,'--','*.go'],cwd=root,text=True).splitlines())
expected={'go/agent-runtime-postgres/kernel_managed_recovery.go','go/agent-runtime-postgres/kernel_owned_physical.go','go/agent-runtime-postgres/kernel_store.go','go/agent-runtime/conformance/managed_recovery.go'}
assert changed==expected, 'Unexpected Go source delta'
allowed=set()
for path in changed:
 p=Path(path); mod=p.parent
 while not (root/mod/'go.mod').exists(): mod=mod.parent
 name=re.search(r'^module\s+(\S+)',(root/mod/'go.mod').read_text(),re.M).group(1)
 allowed.add(name+'/'+p.relative_to(mod).as_posix())
def unchanged(blocks): return {k:v for k,v in blocks.items() if k.rsplit(':',1)[0] not in allowed}
archive=out/'profiles';archive.mkdir(exist_ok=False)
def parse(p):
 rows=p.read_text().splitlines();assert rows[0]=='mode: atomic'
 blocks={};covered=set();duplicates=0
 for line in rows[1:]:
  m=re.fullmatch(r'(.+:\d+\.\d+,\d+\.\d+) (\d+) (\d+)',line);assert m,line
  k,n,c=m.group(1),int(m.group(2)),int(m.group(3))
  if k in blocks:assert blocks[k]==n;duplicates+=1
  blocks[k]=n
  if c>0:covered.add(k)
 return blocks,covered,duplicates
names=sorted(p.name for p in old.glob('*.out'));assert len(names)==12
assert names==sorted(p.name for p in new.glob('*.out'))
report={};allblocks={};allcovered=set();hashes=[]
for n in names:
 a,ac,ad=parse(old/n);b,bc,bd=parse(new/n)
 raw=(new/n).read_bytes();(archive/(n+'.gz')).write_bytes(gzip.compress(raw,mtime=0));hashes.append(hashlib.sha256(raw).hexdigest()+'  '+n+' (decompressed '+n+'.gz)')
 report[n]={'same_unchanged_source_blocks_and_statement_counts':unchanged(a)==unchanged(b),'allowed_changed_sources':sorted(allowed),'old_blocks':len(a),'new_blocks':len(b),'old_statements':sum(a.values()),'new_statements':sum(b.values()),'old_covered':sum(a[x] for x in ac),'new_covered':sum(b[x] for x in bc),'duplicate_rows':bd,'removed_blocks':sorted(set(a)-set(b)),'added_blocks':sorted(set(b)-set(a))}
 for k,v in b.items():
  assert k not in allblocks or allblocks[k]==v
  allblocks[k]=v
 allcovered.update(bc)
summary=json.loads((new/'summary.json').read_text());shutil.copyfile(new/'summary.json',out/'coverage-summary.json')
(archive/'RAW_SHA256SUMS.txt').write_text('\n'.join(hashes)+'\n')
report['global']={'statements':sum(allblocks.values()),'covered':sum(allblocks[x] for x in allcovered),'summary':summary}
(out/'coverage-profile-comparison.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report,indent=2))
assert all(report[n]['same_unchanged_source_blocks_and_statement_counts'] for n in names),'Unchanged source/block denominator changed outside reviewed four-file delta'
assert summary['totalStatements']==sum(allblocks.values())
assert summary['coveredStatements']==sum(allblocks[x] for x in allcovered)
assert summary['minimum']==70 and summary['percent']>=70
