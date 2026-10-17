import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('user_release', Path(__file__).with_name('user_release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class UserReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.compose = self.root / 'compose.yml'
        self.compose.write_text('services:\n  user:\n    image: old-user\n  api:\n    image: untouched-api\n')
        self.live = self.root / 'live'
        (self.live / 'html').mkdir(parents=True)
        (self.live / 'nginx').mkdir()
        for name in ('index.html', '_spa.html', 'guide.html'):
            (self.live / 'html' / name).write_text('<body>live overlay</body>')
        (self.live / 'html' / 'old.js').write_text('preserve overlay')
        (self.live / 'html' / '._index.html').write_bytes(b'\x00\xa3metadata')
        (self.live / 'nginx' / 'nginx.conf').write_text('preserve all nginx configuration')
        (self.live / 'nginx' / 'modules').symlink_to('/usr/lib/nginx/modules')
        self.script = self.root / 'panel.js'
        self.script.write_text('window.test = true;\n')
        self.image = 'sha256:old'
        self.images = {}
        self.commands = []
        self.fail_publication = False
        self.mounts = []
        self.compose_v2 = True
        for name, value in [('APP', self.root), ('COMPOSE', self.compose), ('run', self.fake_run)]:
            p = patch.object(release, name, value)
            p.start()
            self.addCleanup(p.stop)
        self.args = argparse.Namespace(action='inventory', script=self.script,
            script_sha256=release.sha(self.script.read_bytes()), expected_state_sha256=None,
            expected_plan_sha256=None, production_write=True)

    def fake_run(self, *args):
        self.commands.append(args)
        if args == ('docker', 'compose', 'version'):
            if not self.compose_v2: raise subprocess.CalledProcessError(125, args)
            return 'v2'
        if args == ('docker-compose', '--version'): return '1.29.2'
        if args[0] == 'docker-compose': args = ('docker', 'compose') + args[1:]
        if args[:2] == ('docker', 'cp'):
            source = 'nginx' if args[2].endswith('/etc/nginx') else 'html'
            shutil.copytree(self.live / source, args[3], symlinks=True)
        elif args[:2] == ('docker', 'inspect'):
            return json.dumps(self.mounts) if '.Mounts' in args[3] else self.image if args[3] == '{{.Image}}' else 'container-id'
        elif args[:2] == ('docker', 'build'):
            image = args[args.index('-t') + 1]
            self.images[image] = Path(args[-1])
        elif args[:2] == ('docker', 'run'):
            self.assertEqual(args[3], '--network=container:' + release.CONTAINER)
            self.assertEqual(args[-2:], ('nginx', '-t'))
            self.assertIn('rm -rf /usr/share/nginx/html /etc/nginx', (self.images[args[4]] / 'Dockerfile').read_text())
        elif args[:2] == ('docker', 'compose'):
            self.assertEqual(args[-4:], ('up', '-d', '--no-deps', 'user'))
            image = release.yaml.safe_load(self.compose.read_text())['services']['user']['image']
            for name in ('html', 'nginx'):
                shutil.rmtree(self.live / name)
                shutil.copytree(self.images[image] / name, self.live / name, symlinks=True)
            self.image = image
        elif args[0] == 'curl':
            if self.fail_publication:
                return 'unexpected content'
            name = release.ASSET if args[-1].endswith(release.ASSET) else '_spa.html'
            return (self.live / 'html' / name).read_text()
        else:
            raise AssertionError(args)
        return ''

    def prepare(self):
        self.args.expected_state_sha256 = release.execute(self.args)['state_sha256']
        self.args.action = 'plan'
        self.args.expected_plan_sha256 = release.execute(self.args)['plan_sha256']
        self.args.action = 'deploy'

    def test_preserves_overlays_nginx_and_other_services_under_restrictive_umask(self):
        self.prepare()
        previous = os.umask(0o077)
        try:
            result = release.execute(self.args)
        finally:
            os.umask(previous)
        self.assertTrue(result['deployed'])
        self.assertEqual((self.live / 'html' / '._index.html').read_bytes(), b'\x00\xa3metadata')
        self.assertNotIn('._index.html', result['changed_html'])
        self.assertEqual((self.live / 'html' / release.ASSET).stat().st_mode & 0o777, 0o644)
        self.assertEqual((self.live / 'html' / 'old.js').read_text(), 'preserve overlay')
        self.assertTrue((self.live / 'nginx' / 'modules').is_symlink())
        self.assertEqual(release.yaml.safe_load(self.compose.read_text())['services']['api']['image'], 'untouched-api')
        self.assertEqual(len([c for c in self.commands if c[:2] == ('docker', 'build')]), 2)

    def test_legacy_compose_publishes_and_rolls_back(self):
        self.compose_v2 = False
        self.prepare()
        self.fail_publication = True
        before = release.files(self.live / 'html')
        with patch.object(release.time, 'sleep'), self.assertRaisesRegex(ValueError, 'lacks panel'):
            release.execute(self.args)
        self.assertEqual(release.files(self.live / 'html'), before)
        self.assertEqual(len([c for c in self.commands if c[0] == 'docker-compose' and '-f' in c]), 2)

    def test_rolls_back_full_live_tree_with_persistent_image(self):
        self.prepare()
        before_html = release.files(self.live / 'html')
        before_nginx = release.nginx_files(self.live / 'nginx')
        self.fail_publication = True
        with patch.object(release.time, 'sleep'), self.assertRaisesRegex(ValueError, 'lacks panel'):
            release.execute(self.args)
        self.assertEqual(release.files(self.live / 'html'), before_html)
        self.assertEqual(release.nginx_files(self.live / 'nginx'), before_nginx)
        self.assertTrue(release.yaml.safe_load(self.compose.read_text())['services']['user']['image'].endswith('-rollback'))

    def test_drift_refuses_any_build_or_write(self):
        self.prepare()
        (self.live / 'html' / 'old.js').write_text('new customer changes')
        with self.assertRaisesRegex(ValueError, 'drift'):
            release.execute(self.args)
        self.assertFalse(any(c[:2] == ('docker', 'build') for c in self.commands))

    def test_overlapping_mount_refused(self):
        self.mounts = [{'Destination': '/etc/nginx/conf.d'}]
        with self.assertRaisesRegex(ValueError, 'Mounted'):
            release.execute(self.args)


if __name__ == '__main__':
    unittest.main()
