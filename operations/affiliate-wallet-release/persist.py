"""Persist the affiliate panel in the existing prerender source, without running refresh."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import time

ROOT = Path('/opt/uuuaigc-prerender')
ASSET = 'uuuaigc-affiliate-wallet.js'
PATHS = ('template.html', 'refresh.sh', 'runtime-assets/' + ASSET)
LOCK = Path('/var/lock/uuuaigc-production-write.lock')
REFRESH_LOCK = Path('/run/uuuaigc-prerender-refresh.lock')
BACKUPS = Path('/opt/dujiao-next/backups/affiliate-wallet-persistence')


def sha(data):
    return hashlib.sha256(data).hexdigest()


def regular(path, absent=False):
    for ancestor in (path, *path.parents):
        if ancestor.is_symlink():
            raise ValueError('Symbolic path refused: ' + str(path))
    if not path.exists() and absent:
        return
    if not path.is_file():
        raise ValueError('Expected regular file: ' + str(path))


def snapshot():
    state, content = {}, {}
    for name in PATHS:
        path = ROOT / name
        regular(path, absent=name == PATHS[2])
        if not path.exists():
            state[name] = None
            content[name] = None
            continue
        data = path.read_bytes()
        info = path.stat()
        content[name] = data
        state[name] = {'sha256': sha(data), 'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}
    return state, content


def digest(state):
    return sha(json.dumps(state, sort_keys=True).encode())


def write_file(path, data, metadata):
    temporary = path.with_name(path.name + '.affiliate-' + str(time.time_ns()))
    try:
        with temporary.open('xb') as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        temporary.chmod(metadata['mode'] if metadata else 0o644)
        if metadata:
            info = temporary.stat()
            if (info.st_uid, info.st_gid) != (metadata['uid'], metadata['gid']):
                os.chown(temporary, metadata['uid'], metadata['gid'])
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def candidate(content, script):
    template = content['template.html'].decode()
    refresh = content['refresh.sh'].decode()
    if template.count('</body>') != 1:
        raise ValueError('Expected one template body ending')
    tag_prefix = '<script defer src="/' + ASSET + '?v='
    old_tag = tag_prefix + sha(script)[:16] + '"></script>'
    tag = tag_prefix + sha(script)[:17] + '"></script>'
    # Only the actual persistent asset loop is eligible for this one-token edit.
    pattern = re.compile(r'(?m)^(for asset_name in [A-Za-z0-9_. -]+)(; do)$')
    loops = list(pattern.finditer(refresh))
    if len(loops) != 1 or '"$runtime_asset_dir/$asset_name"' not in refresh:
        raise ValueError('Expected one known runtime asset loop')
    if content[PATHS[2]] is not None:
        # This upgrade changes only a cached URL, never existing script bytes or the loop.
        if (content[PATHS[2]] != script or template.count(ASSET) != 1 or
                template.count(old_tag) != 1 or refresh.count(ASSET) != 1 or
                loops[0][1].split()[3:].count(ASSET) != 1):
            raise ValueError('Existing affiliate persistence differs from exact supported upgrade')
        template = template.replace(old_tag, tag)
    else:
        if ASSET in template or ASSET in refresh:
            raise ValueError('Incomplete affiliate persistence')
        template = template.replace('</body>', tag + '\n</body>')
        refresh = pattern.sub(lambda match: match[1] + ' ' + ASSET + match[2], refresh)
    subprocess.run(['sh', '-n'], input=refresh, text=True, check=True)
    return {'template.html': template.encode(), 'refresh.sh': refresh.encode(), PATHS[2]: script}


@contextlib.contextmanager
def refresh_guard():
    try:
        REFRESH_LOCK.mkdir()
    except FileExistsError:
        raise ValueError('Prerender refresh is running; retry after it finishes')
    try:
        yield
    finally:
        REFRESH_LOCK.rmdir()


def execute(args):
    regular(args.script)
    script = args.script.read_bytes()
    if not script or sha(script) != args.script_sha256:
        raise ValueError('Script digest mismatch')
    state, content = snapshot()
    state_hash = digest(state)
    if args.action == 'inventory':
        return {'state_sha256': state_hash, 'files': state, 'script_sha256': sha(script)}
    if state_hash != args.expected_state_sha256:
        raise ValueError('Persistent source drift')
    proposed = candidate(content, script)
    plan = {'state_sha256': state_hash, 'script_sha256': sha(script),
            'files': {name: sha(data) for name, data in proposed.items()}}
    plan_hash = digest(plan)
    if args.action == 'plan':
        return dict(plan, plan_sha256=plan_hash)
    if args.action != 'deploy' or not args.production_write or args.expected_plan_sha256 != plan_hash:
        raise ValueError('Exact approved plan required')
    with refresh_guard():
        if snapshot()[0] != state:
            raise ValueError('Persistent source drift before write')
        backup = BACKUPS / str(time.time_ns())
        backup.mkdir(parents=True, mode=0o700)
        (backup / 'inventory.json').write_text(json.dumps(state, sort_keys=True))
        for name, data in content.items():
            if data is not None:
                destination = backup / name
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(data)
        try:
            # Install the asset before either source starts referencing it.
            for name in (PATHS[2], 'template.html', 'refresh.sh'):
                write_file(ROOT / name, proposed[name], state[name])
            current, _ = snapshot()
            for name in PATHS:
                expected = state[name] or {'mode': 0o644, 'uid': os.getuid(), 'gid': os.getgid()}
                if current[name] != dict(expected, sha256=sha(proposed[name])):
                    raise ValueError('Persistence readback differs')
        except Exception:
            for name in reversed(PATHS):
                if content[name] is None:
                    (ROOT / name).unlink(missing_ok=True)
                else:
                    write_file(ROOT / name, content[name], state[name])
            if snapshot()[0] != state:
                raise ValueError('Persistence rollback differs; recover from ' + str(backup))
            raise
    return dict(plan, plan_sha256=plan_hash, backup=str(backup), deployed=True, refresh_executed=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['inventory', 'plan', 'deploy'])
    parser.add_argument('--script', type=Path, required=True)
    parser.add_argument('--script-sha256', required=True)
    parser.add_argument('--expected-state-sha256')
    parser.add_argument('--expected-plan-sha256')
    parser.add_argument('--production-write', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    with LOCK.open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        print(json.dumps(execute(args), indent=2))
