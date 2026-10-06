#!/usr/bin/env python3
"""Stage a completed local client build without changing the running beta."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import shutil
import tempfile


def inventory(build: Path) -> dict:
    files = {}
    for path in sorted(build.rglob('*')):
        if path.is_symlink():
            raise ValueError(f'Build must not contain symlinks: {path}')
        if path.is_file():
            stat = path.stat()
            files[str(path.relative_to(build))] = (
                stat.st_size, stat.st_mtime_ns, stat.st_ino
            )
    return files


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        '--client-dir', type=Path,
        default=Path(__file__).resolve().parents[2] / 'tellbook-client'
    )
    parser.add_argument('--release-root', type=Path, default=Path('/private/tmp'))
    args = parser.parse_args()
    client = args.client_dir.resolve()
    build = client / 'build'
    for entry in ('index.js', 'handler.js', 'server/index.js', 'server/manifest.js'):
        if not (build / entry).is_file():
            parser.error(f'Completed adapter-node build required: missing {entry}')
    if not (client / 'node_modules').is_dir():
        parser.error('Install client dependencies before staging the beta')
    before = inventory(build)
    args.release_root.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    release = args.release_root.resolve() / f'tellbook-beta-client-{stamp}'
    stage = Path(tempfile.mkdtemp(prefix='.tellbook-beta-stage-', dir=args.release_root))
    try:
        shutil.copytree(build, stage / 'build')
        if inventory(build) != before:
            raise RuntimeError('Build changed during staging; finish the build and retry')
        staged = inventory(stage / 'build')
        if staged.keys() != before.keys() or any(
            staged[name][:2] != before[name][:2] for name in before
        ):
            raise RuntimeError('Staged build differs from the completed client build')
        (stage / 'package.json').write_text(json.dumps({
            'private': True, 'type': 'module'
        }) + '\n')
        (stage / 'node_modules').symlink_to(client / 'node_modules', target_is_directory=True)
        (stage / 'release.json').write_text(json.dumps({
            'created_at': datetime.now(timezone.utc).isoformat(),
            'client_directory': str(client),
            'build_files': len(before),
            'dependencies': 'shared local node_modules; restage/restart after dependency changes'
        }, indent=2) + '\n')
        stage.rename(release)
    finally:
        if stage.exists():
            shutil.rmtree(stage)
    print(release)


if __name__ == '__main__':
    main()
