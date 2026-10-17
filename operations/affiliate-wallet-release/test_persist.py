import argparse
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('persist', Path(__file__).with_name('persist.py'))
persist = importlib.util.module_from_spec(spec)
spec.loader.exec_module(persist)


class PersistTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        (self.root / 'runtime-assets').mkdir()
        (self.root / 'template.html').write_text('<body>existing overlay</body>\n')
        (self.root / 'refresh.sh').write_text('#!/bin/sh\nset -eu\nfor asset_name in old.js other.css; do\n  test -s "$runtime_asset_dir/$asset_name"\ndone\n')
        (self.root / 'refresh.sh').chmod(0o755)
        (self.root / 'runtime-assets' / 'old.js').write_text('existing')
        self.script = self.root / 'input.js'
        self.script.write_text('window.referral = true;\n')
        for name, value in [('ROOT', self.root), ('BACKUPS', self.root / 'backups'), ('REFRESH_LOCK', self.root / 'refresh.lock')]:
            p = patch.object(persist, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.args = argparse.Namespace(action='inventory', script=self.script, script_sha256=persist.sha(self.script.read_bytes()),
            expected_state_sha256=None, expected_plan_sha256=None, production_write=True)
        self.original = persist.snapshot()

    def prepare(self):
        self.args.expected_state_sha256 = persist.execute(self.args)['state_sha256']
        self.args.action = 'plan'
        self.args.expected_plan_sha256 = persist.execute(self.args)['plan_sha256']
        self.args.action = 'deploy'

    def test_persists_exact_tag_asset_loop_and_modes(self):
        self.prepare()
        old = os.umask(0o077)
        try:
            result = persist.execute(self.args)
        finally:
            os.umask(old)
        self.assertTrue(result['deployed'])
        self.assertFalse(result['refresh_executed'])
        template = (self.root / 'template.html').read_text()
        tag = '<script defer src="/uuuaigc-affiliate-wallet.js?v=' + self.args.script_sha256[:17] + '"></script>'
        self.assertEqual(template, '<body>existing overlay' + tag + '\n</body>\n')
        self.assertIn('old.js other.css uuuaigc-affiliate-wallet.js; do', (self.root / 'refresh.sh').read_text())
        self.assertEqual((self.root / 'refresh.sh').stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.root / persist.PATHS[2]).stat().st_mode & 0o777, 0o644)
        self.assertEqual((self.root / 'runtime-assets' / 'old.js').read_text(), 'existing')
        self.assertFalse(persist.REFRESH_LOCK.exists())
        self.assertEqual((Path(result['backup']) / 'template.html').read_bytes(), self.original[1]['template.html'])

    def test_upgrades_exact_old_query_without_duplicating_asset(self):
        first = persist.candidate(self.original[1], self.script.read_bytes())
        new_query = self.args.script_sha256[:17]
        old_query = self.args.script_sha256[:16]
        first['template.html'] = first['template.html'].replace(new_query.encode(), old_query.encode())
        for name, data in first.items():
            (self.root / name).write_bytes(data)
        old_refresh = (self.root / 'refresh.sh').read_bytes()
        self.prepare()
        persist.execute(self.args)
        self.assertIn('?v=' + new_query, (self.root / 'template.html').read_text())
        self.assertEqual((self.root / 'template.html').read_text().count(persist.ASSET), 1)
        self.assertEqual((self.root / 'refresh.sh').read_bytes(), old_refresh)
        self.assertEqual((self.root / persist.PATHS[2]).read_bytes(), self.script.read_bytes())
        for name, bad in [('script', dict(first, **{persist.PATHS[2]: b'drift'})),
                          ('duplicate', dict(first, **{'template.html': first['template.html'] + first['template.html']})),
                          ('loop', dict(first, **{'refresh.sh': first['refresh.sh'].replace(persist.ASSET.encode(), b'other.js')}))]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                persist.candidate(bad, self.script.read_bytes())

    def test_write_failure_restores_contents_modes_and_absence(self):
        self.prepare()
        real_write = persist.write_file
        attempts = 0
        def fail_once(path, data, metadata):
            nonlocal attempts
            attempts += 1
            if attempts == 3:
                raise OSError('injected failure')
            return real_write(path, data, metadata)
        with patch.object(persist, 'write_file', fail_once), self.assertRaisesRegex(OSError, 'injected failure'):
            persist.execute(self.args)
        self.assertEqual(persist.snapshot(), self.original)
        self.assertFalse(persist.REFRESH_LOCK.exists())

    def test_state_drift_or_plan_mismatch_refuses_write(self):
        self.prepare()
        self.args.expected_plan_sha256 = 'invalid'
        with self.assertRaisesRegex(ValueError, 'approved plan'):
            persist.execute(self.args)
        self.assertEqual(persist.snapshot(), self.original)
        (self.root / 'template.html').write_text('changed')
        with self.assertRaisesRegex(ValueError, 'drift'):
            persist.execute(self.args)
        self.assertFalse((self.root / 'backups').exists())

    def test_running_refresh_refuses_write_without_removing_its_lock(self):
        self.prepare()
        persist.REFRESH_LOCK.mkdir()
        with self.assertRaisesRegex(ValueError, 'refresh is running'):
            persist.execute(self.args)
        self.assertEqual(persist.snapshot(), self.original)
        self.assertTrue(persist.REFRESH_LOCK.exists())

    def test_malformed_loop_and_symlink_refused(self):
        changed = dict(self.original[1])
        changed['refresh.sh'] = b'echo no asset loop\n'
        with self.assertRaisesRegex(ValueError, 'asset loop'):
            persist.candidate(changed, self.script.read_bytes())
        path = self.root / persist.PATHS[2]
        path.symlink_to(self.script)
        with self.assertRaisesRegex(ValueError, 'Symbolic'):
            persist.snapshot()


if __name__ == '__main__':
    unittest.main()
