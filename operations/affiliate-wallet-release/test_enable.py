import contextlib
import importlib.util
import io
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('enable', Path(__file__).with_name('enable.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class EnableTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'data/db').mkdir(parents=True)
        self.db = sqlite3.connect(self.root / 'data/db/dujiao.db')
        self.addCleanup(self.db.close)
        self.db.executescript('create table settings(key text primary key,value_json text); create table products(id integer,slug text,is_active integer,deleted_at text); create table affiliate_commissions(reward_order_amounts text,reward_order_totals text,commission_type text,status text); create table payment_channels(provider_type text,is_active integer,deleted_at text); insert into payment_channels values (\'manual_qr\',1,null);')
        self.db.executemany('insert into products values (?,?,1,null)', module.SLUGS)
        self.db.execute('insert into settings values (?,?)', ('unrelated', '{"enabled":false}'))
        self.db.commit()

    def run_enable(self, write, digest='a' * 64):
        with patch.object(module, 'APP', self.root), patch.object(module.subprocess, 'check_output', return_value='a' * 64 + ' /app/dujiao-next'), contextlib.redirect_stdout(io.StringIO()):
            module.enable(digest, write)

    def test_dry_run_no_write_then_enable_idempotent(self):
        self.run_enable(False)
        self.assertEqual(self.db.execute('select count(*) from settings').fetchone()[0], 1)
        self.run_enable(True)
        self.run_enable(True)
        self.assertEqual(self.db.execute('select count(*) from settings').fetchone()[0], 2)
        self.assertEqual(self.db.execute('select value_json from settings where key="unrelated"').fetchone()[0], '{"enabled":false}')
        self.assertEqual(len(list(self.root.glob('backups/*/*/dujiao.db'))), 1)

    def seed_previous(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', json.dumps(module.PREVIOUS_POLICY)))
        self.db.commit()

    def test_upgrade_seven_days_to_manual_receipt(self):
        self.seed_previous()
        self.run_enable(False)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0])['confirm_days'], 7)
        self.run_enable(True)
        self.run_enable(True)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0]), module.POLICY)
        self.assertEqual(len(list(self.root.glob('backups/*/*/dujiao.db'))), 1)

    def test_upgrade_rejects_pending_rewards(self):
        self.seed_previous()
        self.db.execute("insert into affiliate_commissions(commission_type,status) values ('gpt_reward','pending_confirm')")
        self.db.commit()
        with self.assertRaisesRegex(ValueError, 'pending rewards'): self.run_enable(True)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0]), module.PREVIOUS_POLICY)

    def test_upgrade_rejects_automatic_collection_channel(self):
        self.seed_previous()
        self.db.execute("update payment_channels set provider_type='epay'"); self.db.commit()
        with self.assertRaisesRegex(ValueError, 'channel assumptions'): self.run_enable(True)

    def test_binary_drift_rejected(self):
        with self.assertRaises(ValueError): self.run_enable(True, 'b' * 64)

    def test_catalog_drift_rejected(self):
        self.db.execute('update products set is_active=0 where id=1'); self.db.commit()
        with self.assertRaises(ValueError): self.run_enable(True)

    def test_existing_different_policy_rejected(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', '{}')); self.db.commit()
        with self.assertRaises(ValueError): self.run_enable(True)


if __name__ == '__main__': unittest.main()
