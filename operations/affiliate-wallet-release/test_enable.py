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
        self.db.execute("insert into products values (99,'non-gpt-product',1,null)")
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

    def test_upgrade_six_products_to_all_products(self):
        self.seed_previous()
        self.run_enable(False)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0])['eligible_product_ids'], [1, 2, 3, 19, 22, 23])
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

    def test_policy_drift_during_backup_is_not_overwritten(self):
        self.seed_previous()
        changed = dict(module.PREVIOUS_POLICY, enabled=False)
        def concurrent_change():
            self.db.execute("update settings set value_json=? where key='affiliate_config'", (json.dumps(changed),))
            self.db.commit()
            return 123
        with patch.object(module.time, 'time_ns', side_effect=concurrent_change):
            with self.assertRaisesRegex(ValueError, 'policy changed before write'):
                self.run_enable(True)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0]), changed)

    def test_pending_reward_during_backup_stops_upgrade(self):
        self.seed_previous()
        def concurrent_change():
            self.db.execute("insert into affiliate_commissions(commission_type,status) values ('gpt_reward','pending_confirm')")
            self.db.commit()
            return 124
        with patch.object(module.time, 'time_ns', side_effect=concurrent_change):
            with self.assertRaisesRegex(ValueError, 'Pending rewards changed before write'):
                self.run_enable(True)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0]), module.PREVIOUS_POLICY)

    def test_binary_drift_rejected(self):
        with self.assertRaises(ValueError): self.run_enable(True, 'b' * 64)

    def test_all_products_does_not_require_catalog_snapshot(self):
        self.seed_previous()
        self.db.execute('delete from products')
        self.db.execute("insert into products values (100,'future-product',1,null)")
        self.db.commit()
        self.run_enable(True)
        policy = json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0])
        self.assertTrue(policy['all_products'])
        self.assertEqual(policy['eligible_product_ids'], [])
        self.assertEqual(self.db.execute('select id,slug from products').fetchall(), [(100, 'future-product')])

    def test_explicit_false_scope_is_reviewed_previous_policy(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', json.dumps(dict(module.PREVIOUS_POLICY, all_products=False))))
        self.db.commit()
        self.run_enable(True)
        self.assertEqual(json.loads(self.db.execute("select value_json from settings where key='affiliate_config'").fetchone()[0]), module.POLICY)

    def test_seven_day_policy_requires_review(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', json.dumps(dict(module.PREVIOUS_POLICY, confirm_days=7))))
        self.db.commit()
        with self.assertRaisesRegex(ValueError, 'requires review'):
            self.run_enable(True)

    def test_different_scope_requires_review(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', json.dumps(dict(module.PREVIOUS_POLICY, eligible_product_ids=[1, 2]))))
        self.db.commit()
        with self.assertRaisesRegex(ValueError, 'requires review'):
            self.run_enable(True)

    def test_approved_config_matches_publisher(self):
        self.assertEqual(json.loads(Path(__file__).with_name('approved-config.json').read_text()), module.POLICY)

    def test_existing_different_policy_rejected(self):
        self.db.execute('insert into settings values (?,?)', ('affiliate_config', '{}')); self.db.commit()
        with self.assertRaises(ValueError): self.run_enable(True)


if __name__ == '__main__': unittest.main()
