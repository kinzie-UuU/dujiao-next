"""Enable the approved GPT wallet policy after the reviewed API deployment."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time

APP = Path('/opt/dujiao-next')
POLICY = {'enabled': True, 'reward_mode': 'gpt_random_wallet',
          'eligible_product_ids': [1, 2, 3, 19, 22, 23], 'commission_rate': 0,
          'confirm_days': 0, 'min_withdraw_amount': 0, 'withdraw_channels': []}
PREVIOUS_POLICY = dict(POLICY, confirm_days=7)
SLUGS = [(1, 'gpt-plus-ios-us'), (2, 'gpt-pro-5x-ios-us'), (3, 'gpt-pro-20x-ph'),
         (19, 'chatgpt-go-recharge'), (22, 'gpt-pro-500'), (23, 'gpt-credits-250')]


def enable(expected_binary, write):
    actual = subprocess.check_output(['docker', 'exec', 'dujiaonext-api', 'sha256sum', '/app/dujiao-next'], text=True).split()[0]
    if len(expected_binary) != 64 or actual != expected_binary:
        raise ValueError('API binary drift')
    dbpath = APP / 'data/db/dujiao.db'
    db = sqlite3.connect('file:' + str(dbpath) + ('?mode=rw' if write else '?mode=ro'), uri=True)
    columns = {row[1] for row in db.execute('pragma table_info(affiliate_commissions)')}
    if not {'reward_order_amounts', 'reward_order_totals'} <= columns:
        raise ValueError('Reward migration missing')
    placeholders = ','.join('?' for _ in SLUGS)
    rows = db.execute('select id,slug from products where deleted_at is null and is_active=1 and id in (' + placeholders + ') order by id', [row[0] for row in SLUGS]).fetchall()
    if rows != SLUGS:
        raise ValueError('Approved product catalog drift')
    prior = db.execute('select value_json from settings where key=?', ('affiliate_config',)).fetchone()
    previous = json.loads(prior[0]) if prior else None
    if previous is not None and previous not in (POLICY, PREVIOUS_POLICY):
        raise ValueError('Existing affiliate policy requires review')
    if previous == PREVIOUS_POLICY:
        # All current collection channels require an administrator to confirm receipt.
        channels = db.execute('select provider_type from payment_channels where deleted_at is null and is_active=1').fetchall()
        if not channels or any(row[0] != 'manual_qr' for row in channels):
            raise ValueError('Manual receipt channel assumptions changed')
        pending = db.execute("select count(*) from affiliate_commissions where commission_type='gpt_reward' and status='pending_confirm'").fetchone()[0]
        if pending:
            raise ValueError('Existing pending rewards require reviewed reconciliation')
    if write and previous != POLICY:
        backup = APP / 'backups/affiliate-wallet-20261017' / ('enable-' + str(time.time_ns()))
        backup.mkdir(parents=True, mode=0o700)
        with sqlite3.connect(backup / 'dujiao.db') as target:
            db.backup(target)
        with db:
            db.execute('BEGIN IMMEDIATE')
            current = db.execute('select value_json from settings where key=?', ('affiliate_config',)).fetchone()
            if current != prior:
                raise ValueError('Affiliate policy changed before write')
            if previous == PREVIOUS_POLICY:
                if db.execute("select count(*) from affiliate_commissions where commission_type='gpt_reward' and status='pending_confirm'").fetchone()[0]:
                    raise ValueError('Pending rewards changed before write')
                channels = db.execute('select provider_type from payment_channels where deleted_at is null and is_active=1').fetchall()
                if not channels or any(row[0] != 'manual_qr' for row in channels):
                    raise ValueError('Manual receipt channels changed before write')
                db.execute('update settings set value_json=? where key=?', (json.dumps(POLICY), 'affiliate_config'))
            else:
                db.execute('insert into settings(key,value_json) values (?,?)', ('affiliate_config', json.dumps(POLICY)))
    final = db.execute('select value_json from settings where key=?', ('affiliate_config',)).fetchone()
    print(json.dumps({'write': write, 'policy': json.loads(final[0]) if final else None, 'eligible_products': rows}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary-sha256', required=True)
    parser.add_argument('--production-write', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    with Path('/var/lock/uuuaigc-production-write.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        enable(args.binary_sha256, args.production_write)
