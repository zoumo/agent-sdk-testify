#!/usr/bin/env python3
"""Build a native distribution or assemble the four tested platform distributions."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64')
TOOL = 'agent-sdk-testify'
DRIVER = TOOL + '-codex'


def run(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def release_version(allow_untagged):
    tag = subprocess.run(['git', 'describe', '--tags', '--exact-match', 'HEAD'],
                         cwd=ROOT, capture_output=True, text=True)
    if tag.returncode == 0:
        return tag.stdout.strip()
    if allow_untagged:
        return 'devel'
    raise SystemExit('release requires an exact Git tag: ' + tag.stderr.strip())


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def native_distribution(output, version):
    system, arch = run('go', 'env', 'GOHOSTOS', 'GOHOSTARCH').splitlines()
    target = f'{system}-{arch}'
    if target not in TARGETS or run('go', 'env', 'GOOS', 'GOARCH').splitlines() != [system, arch]:
        raise SystemExit(f'expected a native build for one of {TARGETS}')
    archive = output / f'{TOOL}_{version}_{system}_{arch}.tar.gz'
    with tempfile.TemporaryDirectory(prefix='testify-bundle-') as tmp:
        bundle = Path(tmp).resolve()
        subprocess.run(['bash', ROOT / 'scripts/build.sh'], cwd=bundle, check=True,
                       env=dict(os.environ, AGENT_SDK_TESTIFY_BUILD_DIR=str(bundle), CGO_ENABLED='0'))
        members = {TOOL: bundle / TOOL, DRIVER: bundle / DRIVER,
                   'builtin-manifest.json': ROOT / 'data/builtin/manifest.json'}
        for name in ('LICENSE', 'README.md', 'docs/using-mock.md', 'drivers/README.md'):
            members[name] = ROOT / name
        with archive.open('wb') as stream, gzip.GzipFile(filename='', fileobj=stream, mode='wb', mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode='w') as tar:
                for name, path in members.items():
                    data = path.read_bytes()
                    info = tarfile.TarInfo(name)
                    info.size = len(data)
                    info.mode = 0o755 if name in (TOOL, DRIVER) else 0o644
                    tar.addfile(info, io.BytesIO(data))
        files = [{'path': archive.name, 'sha256': digest(archive), 'target': f'{system}/{arch}',
                  'binarySHA256': digest(bundle / TOOL), 'codexSHA256': digest(bundle / DRIVER)}]
        for provider in ('claude', 'pi'):
            driver, = bundle.glob(f'{TOOL}-{provider}-*.tgz')
            shutil.copyfile(driver, output / driver.name)
            files.append({'path': driver.name, 'sha256': digest(driver)})
        return files


def collect(output, inputs, metadata):
    files = []
    for target in TARGETS:
        root = inputs / target
        manifest = json.loads((root / 'manifest.json').read_text())
        if any(manifest[key] != value for key, value in metadata.items()):
            raise SystemExit(f'{target} release metadata differs from checkout')
        archive, claude, pi = manifest['files']
        expected = f"{TOOL}_{metadata['toolVersion']}_{target.replace('-', '_')}.tar.gz"
        if archive['target'] != target.replace('-', '/') or archive['path'] != expected:
            raise SystemExit(f'{target} archive does not match target')
        for entry in [archive, claude, pi] if target == 'linux-amd64' else [archive]:
            path = root / entry['path']
            if path.parent != root or digest(path) != entry['sha256']:
                raise SystemExit(f'{target} invalid artifact: {path}')
            shutil.copyfile(path, output / path.name)
            files.append(entry)
    return files


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--collect', type=Path, help='directory of verified OS-ARCH distributions')
    parser.add_argument('--allow-untagged', action='store_true')
    args = parser.parse_args()
    version = release_version(args.allow_untagged)
    manifest_path = ROOT / 'data/builtin/manifest.json'
    bundle = json.loads(manifest_path.read_text())
    metadata = dict(schemaVersion=1, toolVersion=version, sourceRevision=run('git', 'rev-parse', 'HEAD'),
                    sourceModified=bool(run('git', 'status', '--porcelain')), bundleSourceRevision=bundle['sourceRevision'],
                    bundleManifestSHA256=digest(manifest_path), bundleArchiveSHA256=bundle['archiveSHA256'],
                    materials=len(bundle['materials']), coverage=len(bundle['coverage']), goVersion=run('go', 'env', 'GOVERSION'),
                    codexRevision=(ROOT / 'drivers/codex/rust/upstream-revision').read_text().strip(),
                    publication='Git tag release; SDK release pin not updated')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    files = collect(output, args.collect, metadata) if args.collect else native_distribution(output, version)
    manifest = output / 'manifest.json'
    manifest.write_text(json.dumps(dict(metadata, files=files), indent=2, sort_keys=True) + '\n')
    checks = sorted([*files, {'path': manifest.name, 'sha256': digest(manifest)}], key=lambda item: item['path'])
    (output / 'SHA256SUMS').write_text(''.join(f"{item['sha256']}  {item['path']}\n" for item in checks))
    print(manifest.read_text(), end='')


if __name__ == '__main__':
    main()
