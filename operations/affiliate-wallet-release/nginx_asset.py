"""Allow the one reviewed affiliate script through the storefront's root asset allowlist."""
import argparse
import fcntl
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import time

CONFIG = Path('/etc/nginx/sites-available/card-site')


def candidate(data):
    old = b'uuuaigc-runtime-fixes\\.js|'
    new = old + b'uuuaigc-affiliate-wallet\\.js|'
    if data.count(old) != 2 or b'uuuaigc-affiliate-wallet' in data:
        raise ValueError('Unexpected allowlist baseline')
    changed = data.replace(old, new)
    assert changed.replace(new, old) == data
    return changed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-sha256', required=True)
    parser.add_argument('--production-write', action='store_true')
    args = parser.parse_args()
    with open('/var/lock/uuuaigc-production-write.lock','a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        if CONFIG.is_symlink(): raise ValueError('Expected actual configuration path')
        before=CONFIG.read_bytes()
        if hashlib.sha256(before).hexdigest()!=args.expected_sha256: raise ValueError('Nginx drift')
        after=candidate(before)
        if not args.production_write:
            print('Plan: add only uuuaigc-affiliate-wallet.js to two existing root allowlists')
            return
        backup=Path('/opt/dujiao-next/backups/affiliate-wallet-20261017')/('nginx-'+str(time.time_ns()))
        backup.mkdir(mode=0o700)
        shutil.copy2(CONFIG,backup/'card-site')
        temporary=CONFIG.with_name(CONFIG.name+'.affiliate.tmp')
        def write(data):
            temporary.write_bytes(data);shutil.copystat(CONFIG,temporary);os.replace(temporary,CONFIG)
        try:
            write(after)
            subprocess.run(['nginx','-t'],check=True)
            subprocess.run(['systemctl','reload','nginx'],check=True)
        except Exception:
            write(before)
            subprocess.run(['nginx','-t'],check=True)
            subprocess.run(['systemctl','reload','nginx'],check=True)
            raise
        print('Published exact script allowlist; backup='+str(backup))


if __name__=='__main__': main()
