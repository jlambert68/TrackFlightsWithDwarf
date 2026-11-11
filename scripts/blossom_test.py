import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
FAKE_DOCKER = r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['FAKE_LOG'], 'a') as out:
    out.write(json.dumps({'args': sys.argv[1:], 'bind': os.environ.get('BLOSSOM_BIND'), 'port': os.environ.get('BLOSSOM_PORT'), 'config': os.environ.get('BLOSSOM_CONFIG')})+'\n')
'''

class BlossomTargetsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.log = self.root / 'calls'
        self.docker = self.root / 'docker'
        self.docker.write_text(FAKE_DOCKER)
        self.docker.chmod(0o755)
        self.env = {**os.environ, 'FAKE_LOG': str(self.log)}

    def make(self, target, *args):
        return subprocess.run(['make', '--no-print-directory', '-s', '-C', str(ROOT), target,
                               'DOCKER='+str(self.docker), 'DATA_ROOT='+str(self.root/'data'), *args],
                              env=self.env, text=True, capture_output=True)

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_start_is_isolated_and_preserves_configuration(self):
        result = self.make('blossom-start')
        self.assertEqual(result.returncode, 0, result.stderr)
        config = self.root/'data/blossom/config.yml'
        self.assertEqual(config.read_text(), (ROOT/'blossom-server/config.example.yml').read_text())
        call = self.calls()[-1]
        self.assertEqual(call['bind'], '127.0.0.1')
        self.assertEqual(call['port'], '3001')
        self.assertEqual(call['config'], str(config))
        self.assertIn('trackflights-blossom', call['args'])
        self.assertEqual(call['args'][-2:], ['up', '-d'])
        config.write_text('custom configuration\n')
        result = self.make('blossom-start')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(config.read_text(), 'custom configuration\n')

    def test_overrides_and_stop_keep_volume(self):
        result = self.make('blossom-stop', 'BLOSSOM_PROJECT=another-blossom', 'BLOSSOM_PORT=3012')
        self.assertEqual(result.returncode, 0, result.stderr)
        call = self.calls()[-1]
        self.assertEqual(call['port'], '3012')
        self.assertIn('another-blossom', call['args'])
        self.assertEqual(call['args'][-1], 'stop')
        self.assertNotIn('-v', call['args'])

if __name__ == '__main__':
    unittest.main()
