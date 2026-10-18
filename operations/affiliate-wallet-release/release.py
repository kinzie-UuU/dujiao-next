"""Pinned reuse of the storefront's guarded API/admin publisher."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path

PINS = {
    'admin-payment-security-release/release.py': 'a59d6d2f40bcc6cea67f599d080d1435ccdb4d8efb0d3da7f18a0dc7043a0eb8',
    'admin-repair/deploy.py': '3fae0c4fd2ce89b2d602f798353bea1c987ee7da6e3350d5e35eb277aec47282',
}
BASE_REFERENCE = 'dujiao-next:payment-security-179775134a41-27bc44a2ade5'
BASE_BINARY = '27bc44a2ade595c76b90fcb3298ef4f689e21974062bda6890f5581fb0b1989f'


def load_publisher(controls):
    for name, expected in PINS.items():
        path = controls / name
        if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise ValueError('Unreviewed deployment helper: ' + name)
    spec = importlib.util.spec_from_file_location('guarded_security_publisher', controls / 'admin-payment-security-release/release.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.BASE_REFERENCE = BASE_REFERENCE
    module.BASE_BINARY = BASE_BINARY
    control = module.load_control()
    control.BACKUPS = control.APP / 'backups/affiliate-wallet-20261017'
    return module, control


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('inventory', 'plan', 'deploy'))
    parser.add_argument('--controls', type=Path, required=True)
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--admin', type=Path)
    parser.add_argument('--revision')
    parser.add_argument('--binary-sha256')
    parser.add_argument('--expected-state-sha256')
    parser.add_argument('--expected-plan-sha256')
    parser.add_argument('--production-write', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    module, control = load_publisher(args.controls)
    import fcntl
    with module.LOCK.open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        print(json.dumps(module.execute(control, args), indent=2))


if __name__ == '__main__':
    main()
