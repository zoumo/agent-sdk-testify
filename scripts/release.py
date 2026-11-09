#!/usr/bin/env python3
"""Prepare local artifacts, without publishing or downloading dependencies."""
import argparse, hashlib, json, os, pathlib, subprocess, tarfile, gzip, io
ROOT = pathlib.Path(__file__).resolve().parents[1]
def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True).strip()
def release_version(allow_untagged):
    result=subprocess.run(['git','describe','--tags','--exact-match','HEAD'], cwd=ROOT, capture_output=True, text=True)
    if result.returncode == 0:
        return result.stdout.strip(), True
    if allow_untagged:
        return 'devel', False
    raise SystemExit('release requires an exact Git tag: '+result.stderr.strip())
def digest(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',required=True,type=pathlib.Path)
    parser.add_argument('--targets',nargs='+',default=['linux/amd64','linux/arm64','darwin/amd64','darwin/arm64'])
    parser.add_argument('--allow-untagged',action='store_true')
    args=parser.parse_args()
    output=args.output.resolve(); output.mkdir(parents=True,exist_ok=False)
    version,tagged=release_version(args.allow_untagged)
    bundle=json.loads((ROOT/'data/builtin/manifest.json').read_text())
    if tagged and bundle['toolVersion'] != version:
        raise SystemExit(f"builtin toolVersion {bundle['toolVersion']} does not match exact tag {version}")
    revision=run('git','rev-parse','HEAD')
    epoch=run('git','show','-s','--format=%ct','HEAD')
    environment=dict(os.environ,CGO_ENABLED='0',GOTOOLCHAIN='local',GOPROXY='off',GOSUMDB='off',SOURCE_DATE_EPOCH=epoch,AGENT_SDK_TESTIFY_VERSION=version)
    ldflags='-X=github.com/zoumo/agent-sdk-testify/internal/version.buildVersion='+version
    run('go','run','-tags','builtin_generate','-ldflags',ldflags,'./cmd/agent-sdk-testify','--check')
    files=[]
    for target in args.targets:
        system,arch=target.split('/')
        if system not in ('linux','darwin') or arch not in ('amd64','arm64'): raise SystemExit('unsupported target')
        directory=output/(system+'-'+arch); directory.mkdir()
        binary=directory/'agent-sdk-testify'
        run('go','build','-trimpath','-buildvcs=false','-ldflags',ldflags,'-o',str(binary),'./cmd/agent-sdk-testify',env=dict(environment,GOOS=system,GOARCH=arch))
        name=f'agent-sdk-testify_{version}_{system}_{arch}.tar.gz'
        with (output/name).open('wb') as stream, gzip.GzipFile(filename='',fileobj=stream,mode='wb',mtime=0) as gz, tarfile.open(fileobj=gz,mode='w') as tar:
            for source,member in [(binary,'agent-sdk-testify'),(ROOT/'data/builtin/manifest.json','builtin-manifest.json'),(ROOT/'LICENSE','LICENSE'),(ROOT/'README.md','README.md'),(ROOT/'docs/using-mock.md','docs/using-mock.md'),(ROOT/'drivers/README.md','drivers/README.md')]:
                data=source.read_bytes(); info=tarfile.TarInfo(member); info.size=len(data); info.mode=0o755 if member=='agent-sdk-testify' else 0o644
                tar.addfile(info,io.BytesIO(data))
        files.append({'path':name,'sha256':digest(output/name),'binarySHA256':digest(binary),'target':target})
        binary.unlink(); directory.rmdir()
    run(os.environ.get('PYTHON','python3'),'-m','pip','wheel','--no-deps','--no-build-isolation','--wheel-dir',str(output),str(ROOT/'drivers'),env=environment)
    for wheel in sorted(output.glob('*.whl')): files.append({'path':wheel.name,'sha256':digest(wheel)})
    manifest={'schemaVersion':1,'toolVersion':version,'sourceRevision':revision,'sourceModified':bool(run('git','status','--porcelain')),'bundleSourceRevision':bundle['sourceRevision'],'bundleManifestSHA256':digest(ROOT/'data/builtin/manifest.json'),'bundleArchiveSHA256':bundle['archiveSHA256'],'materials':len(bundle['materials']),'coverage':len(bundle['coverage']),'goVersion':run('go','env','GOVERSION'),'publication':'Git tag release; SDK release pin not updated','files':files}
    (output/'manifest.json').write_text(json.dumps(manifest,indent=2,sort_keys=True)+'\n')
    checks=sorted([*files,{'path':'manifest.json','sha256':digest(output/'manifest.json')}],key=lambda x:x['path'])
    (output/'SHA256SUMS').write_text(''.join(f"{entry['sha256']}  {entry['path']}\n" for entry in checks))
    print(json.dumps(manifest))
if __name__=='__main__': main()
