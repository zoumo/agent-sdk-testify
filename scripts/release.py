#!/usr/bin/env python3
"""Prepare local artifacts, without publishing or downloading dependencies."""
import argparse, hashlib, json, os, pathlib, subprocess, tarfile, gzip, io
ROOT = pathlib.Path(__file__).resolve().parents[1]
def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()
def digest(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',required=True,type=pathlib.Path)
    parser.add_argument('--targets',nargs='+',default=['linux/amd64','linux/arm64','darwin/amd64','darwin/arm64'])
    args=parser.parse_args()
    output=args.output.resolve(); output.mkdir(parents=True,exist_ok=False)
    if run('go','env','GOVERSION')!='go1.26.9': raise SystemExit('Go 1.26.9 required')
    run('go','run','-tags','builtin_generate','.','--check')
    bundle=json.loads((ROOT/'data/builtin/manifest.json').read_text())
    version=bundle['toolVersion']
    revision=run('git','rev-parse','HEAD')
    epoch=run('git','show','-s','--format=%ct','HEAD')
    environment=dict(os.environ,CGO_ENABLED='0',GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',SOURCE_DATE_EPOCH=epoch)
    files=[]
    for target in args.targets:
        system,arch=target.split('/')
        if system not in ('linux','darwin') or arch not in ('amd64','arm64'): raise SystemExit('unsupported target')
        directory=output/(system+'-'+arch); directory.mkdir()
        binary=directory/'agent-sdk-testify'
        run('go','build','-trimpath','-buildvcs=false','-o',str(binary),'.',env=dict(environment,GOOS=system,GOARCH=arch))
        name=f'agent-sdk-testify_{version}_{system}_{arch}.tar.gz'
        with (output/name).open('wb') as stream, gzip.GzipFile(filename='',fileobj=stream,mode='wb',mtime=0) as gz, tarfile.open(fileobj=gz,mode='w') as tar:
            for source,member in [(binary,'agent-sdk-testify'),(ROOT/'data/builtin/manifest.json','builtin-manifest.json'),(ROOT/'LICENSE','LICENSE'),(ROOT/'README.md','README.md'),(ROOT/'docs/using-mock.md','docs/using-mock.md'),(ROOT/'docs/builtin.md','docs/builtin.md'),(ROOT/'docs/mock-catalog.schema.json','docs/mock-catalog.schema.json'),(ROOT/'drivers/README.md','drivers/README.md')]:
                data=source.read_bytes(); info=tarfile.TarInfo(member); info.size=len(data); info.mode=0o755 if member=='agent-sdk-testify' else 0o644
                tar.addfile(info,io.BytesIO(data))
        files.append({'path':name,'sha256':digest(output/name),'binarySHA256':digest(binary),'target':target})
        binary.unlink(); directory.rmdir()
    run(os.environ.get('PYTHON','python3.11'),'-m','pip','wheel','--no-deps','--no-build-isolation','--wheel-dir',str(output),str(ROOT/'drivers'),env=environment)
    for wheel in sorted(output.glob('*.whl')): files.append({'path':wheel.name,'sha256':digest(wheel)})
    manifest={'schemaVersion':1,'toolVersion':version,'sourceRevision':revision,'sourceModified':bool(run('git','status','--porcelain')),'bundleSourceRevision':bundle['sourceRevision'],'bundleManifestSHA256':digest(ROOT/'data/builtin/manifest.json'),'bundleArchiveSHA256':bundle['archiveSHA256'],'materials':len(bundle['materials']),'coverage':len(bundle['coverage']),'goVersion':'go1.26.9','publication':'local only; no release or SDK release pin','files':files}
    (output/'manifest.json').write_text(json.dumps(manifest,indent=2,sort_keys=True)+'\n')
    checks=sorted([*files,{'path':'manifest.json','sha256':digest(output/'manifest.json')}],key=lambda x:x['path'])
    (output/'SHA256SUMS').write_text(''.join(f"{entry['sha256']}  {entry['path']}\n" for entry in checks))
    print(json.dumps(manifest))
if __name__=='__main__': main()
