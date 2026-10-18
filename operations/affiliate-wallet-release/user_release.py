"""Publish only the affiliate compatibility panel, retaining every live storefront asset."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import yaml

APP = Path('/opt/dujiao-next')
COMPOSE = APP / 'docker-compose.sqlite.yml'
CONTAINER = 'dujiaonext-user'
ROOT = '/usr/share/nginx/html'
ASSET = 'uuuaigc-affiliate-wallet.js'


def run(*args):
    return subprocess.check_output(args, text=True)


def compose_prefix():
    try:
        run('docker', 'compose', 'version')
        return ('docker', 'compose')
    except subprocess.CalledProcessError:
        run('docker-compose', '--version')
        return ('docker-compose',)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def files(root):
    if any(p.is_symlink() for p in root.rglob('*')):
        raise ValueError('Unexpected symbolic link')
    return {str(p.relative_to(root)): sha(p.read_bytes()) for p in sorted(root.rglob('*')) if p.is_file()}


def nginx_files(root):
    return {str(p.relative_to(root)): ('link:' + os.readlink(p) if p.is_symlink() else sha(p.read_bytes()))
            for p in sorted(root.rglob('*')) if p.is_symlink() or p.is_file()}


def write_compose(data):
    temporary = COMPOSE.with_name(COMPOSE.name + '.affiliate.tmp')
    try:
        temporary.write_bytes(data)
        shutil.copystat(COMPOSE, temporary)
        os.replace(temporary, COMPOSE)
    finally:
        temporary.unlink(missing_ok=True)


def dockerfile(folder, image):
    (folder / 'Dockerfile').write_text('FROM ' + image + '\nRUN rm -rf /usr/share/nginx/html /etc/nginx\nCOPY html/ /usr/share/nginx/html/\nCOPY nginx/ /etc/nginx/\n')


def snapshot(directory):
    run('docker', 'cp', CONTAINER + ':' + ROOT, str(directory / 'html'))
    run('docker', 'cp', CONTAINER + ':/etc/nginx', str(directory / 'nginx'))
    mounts = json.loads(run('docker', 'inspect', '--format', '{{json .Mounts}}', CONTAINER))
    for mount in mounts:
        destination = mount['Destination'].rstrip('/')
        if any(destination == root or destination.startswith(root + '/') or root.startswith(destination + '/') for root in (ROOT, '/etc/nginx')):
            raise ValueError('Mounted storefront or nginx configuration cannot be published as an image')
    return {'container': run('docker', 'inspect', '--format', '{{.Id}}', CONTAINER).strip(),
            'image': run('docker', 'inspect', '--format', '{{.Image}}', CONTAINER).strip(),
            'compose': sha(COMPOSE.read_bytes()), 'nginx': nginx_files(directory / 'nginx'), 'mounts': mounts,
            'files': files(directory / 'html')}


def execute(args):
    previous_sha = getattr(args, 'previous_script_sha256', None)
    if previous_sha is not None and not re.fullmatch(r'[0-9a-f]{64}', previous_sha):
        raise ValueError('Previous script digest must be 64 lowercase hexadecimal characters')
    if args.script.is_symlink() or not args.script.is_file(): raise ValueError('Script must be regular')
    script = args.script.read_bytes()
    if sha(script) != args.script_sha256: raise ValueError('Script digest mismatch')
    with tempfile.TemporaryDirectory(prefix='affiliate-user-') as temporary:
        folder = Path(temporary)
        state = snapshot(folder)
        digest = sha(json.dumps(state, sort_keys=True).encode())
        if args.action == 'inventory':
            return {'state_sha256': digest, 'file_count': len(state['files']), 'image': state['image'], 'script_sha256': sha(script)}
        if digest != args.expected_state_sha256: raise ValueError('Live storefront drift')
        prefix = compose_prefix()
        before = COMPOSE.read_bytes()
        document = yaml.safe_load(before)
        old_image = document['services']['user']['image']
        image = 'uuuaigc-user:affiliate-' + sha(script)[:12] + '-' + digest[:12]
        old_line = re.compile(r'(?m)^(\s+image:\s*)' + re.escape(old_image) + r'\s*$')
        after, count = old_line.subn(lambda m: m[1] + image, before.decode())
        if count != 1: raise ValueError('Ambiguous user image declaration')
        expected = yaml.safe_load(before); expected['services']['user']['image'] = image
        if yaml.safe_load(after) != expected: raise ValueError('Unrelated compose change')
        htmlroot = folder / 'html'
        existing_asset = htmlroot / ASSET
        if previous_sha is not None:
            if not existing_asset.is_file() or sha(existing_asset.read_bytes()) != previous_sha:
                raise ValueError('Existing panel differs from reviewed previous digest')
        elif existing_asset.exists() and existing_asset.read_bytes() != script:
            raise ValueError('Different existing panel requires review')
        tag = '<script defer src="/' + ASSET + '?v=' + sha(script)[:17] + '"></script>'
        previous_tag = '<script defer src="/' + ASSET + '?v=' + (previous_sha[:17] if previous_sha else sha(script)[:16]) + '"></script>'
        changed = []
        for path in sorted(htmlroot.rglob('*.html')):
            # AppleDouble metadata is binary, not a storefront entry. Preserve it.
            if path.name.startswith('._'): continue
            content = path.read_text()
            if '</body>' not in content: continue
            if ASSET in content:
                if content.count(previous_tag) != 1 or content.count(ASSET) != 1:
                    raise ValueError('Unexpected existing panel reference')
                path.write_text(content.replace(previous_tag, tag))
            else:
                if previous_sha is not None:
                    # Independently generated payment pages do not host this panel.
                    continue
                path.write_text(content.replace('</body>', tag + '\n</body>'))
            changed.append(str(path.relative_to(htmlroot)))
        if not {'index.html', '_spa.html'} <= set(changed): raise ValueError('Missing storefront entries')
        (htmlroot / ASSET).write_bytes(script)
        (htmlroot / ASSET).chmod(0o644)
        # docker cp preserves directory modes; nginx must traverse the web root.
        htmlroot.chmod(0o755)
        dockerfile(folder, state['image'])
        plan = {'state_sha256': digest, 'image': image, 'script_sha256': sha(script), 'changed_html': changed, 'preserved_files': len(state['files']) - len(changed)}
        if previous_sha is not None:
            plan['previous_script_sha256'] = previous_sha
        plan_hash = sha(json.dumps(plan, sort_keys=True).encode())
        if args.action == 'plan': return dict(plan, plan_sha256=plan_hash)
        if not args.production_write or args.expected_plan_sha256 != plan_hash: raise ValueError('Exact approved plan required')
        backup = APP / 'backups/affiliate-wallet-20261017' / ('user-' + str(time.time_ns()))
        backup.mkdir(parents=True, mode=0o700)
        (backup / 'compose.yml').write_bytes(before)
        (backup / 'inventory.json').write_text(json.dumps(state))
        # Backup every file that changes; unchanged files stay in the candidate image.
        run('docker', 'cp', CONTAINER + ':' + ROOT, str(backup / 'html'))
        shutil.copytree(folder / 'nginx', backup / 'nginx', symlinks=True)
        # Build a persistent rollback image before changing the running service.
        rollback = image + '-rollback'
        rollbackdir = backup / 'rollback'; rollbackdir.mkdir()
        shutil.copytree(backup / 'html', rollbackdir / 'html', symlinks=True)
        shutil.copytree(backup / 'nginx', rollbackdir / 'nginx', symlinks=True)
        dockerfile(rollbackdir, state['image'])
        run('docker', 'build', '--pull=false', '--network=none', '-t', rollback, str(rollbackdir))
        run('docker', 'build', '--pull=false', '--network=none', '-t', image, str(folder))
        # Validate upstream DNS using the existing container network; nginx -t does not listen.
        run('docker', 'run', '--rm', '--network=container:' + CONTAINER, image, 'nginx', '-t')
        # Stop concurrent writers from silently changing the container during build.
        with tempfile.TemporaryDirectory(prefix='affiliate-check-') as check:
            if snapshot(Path(check)) != state: raise ValueError('Storefront changed during build')
        try:
            write_compose(after.encode())
            run(*prefix, '-f', str(COMPOSE), 'up', '-d', '--no-deps', 'user')
            for _ in range(20):
                try:
                    body = run('curl', '-fsS', 'http://127.0.0.1:8081/me/affiliate')
                    if tag in body:
                        served = run('curl', '-fsS', 'http://127.0.0.1:8081/' + ASSET)
                        if served.encode() != script: raise ValueError('Published script differs or is unreadable')
                        break
                except subprocess.CalledProcessError: pass
                time.sleep(1)
            else: raise ValueError('Published route lacks panel entry')
            with tempfile.TemporaryDirectory(prefix='affiliate-readback-') as check:
                actual = snapshot(Path(check))
                if actual['files'] != files(htmlroot) or actual['nginx'] != state['nginx']: raise ValueError('Published assets differ')
        except Exception:
            # Restore the exact captured live image, including preexisting overlays.
            write_compose(old_line.sub(lambda m: m[1] + rollback, before.decode()).encode())
            run(*prefix, '-f', str(COMPOSE), 'up', '-d', '--no-deps', 'user')
            with tempfile.TemporaryDirectory(prefix='affiliate-rollback-check-') as check:
                restored = snapshot(Path(check))
                if restored['files'] != state['files'] or restored['nginx'] != state['nginx']:
                    raise ValueError('Rollback files differ; manual recovery required at ' + str(backup))
            raise
        return dict(plan, plan_sha256=plan_hash, backup=str(backup), deployed=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['inventory', 'plan', 'deploy'])
    parser.add_argument('--script', type=Path, required=True)
    parser.add_argument('--script-sha256', required=True)
    parser.add_argument('--previous-script-sha256')
    parser.add_argument('--expected-state-sha256')
    parser.add_argument('--expected-plan-sha256')
    parser.add_argument('--production-write', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    with Path('/var/lock/uuuaigc-production-write.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        print(json.dumps(execute(args), indent=2))
