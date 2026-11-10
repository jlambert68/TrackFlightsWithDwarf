import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent

FAKE_DOCKER = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
args=sys.argv[1:]
with open(os.environ['FAKE_LOG'], 'a') as out: out.write(json.dumps(args)+'\n')
existing=os.environ.get('FAKE_EXISTING','').split(',')
if args[:2]==['container','inspect']: sys.exit(0 if args[2] in existing else 1)
if args and args[0]=='inspect':
    print('true' if '.State.Running' in args[2] else 'ghcr.io/mattn/nostr-relay:latest')
'''
FAKE_CURL = r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['FAKE_LOG'], 'a') as out: out.write(json.dumps(['curl']+sys.argv[1:])+'\n')
print('{}')
'''

class RelayTargetsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.log = self.root / 'calls'
        for name, content in [('docker', FAKE_DOCKER), ('curl', FAKE_CURL)]:
            p = self.root / name
            p.write_text(content)
            p.chmod(0o755)
        self.env = {**os.environ, 'FAKE_LOG': str(self.log), 'PATH': str(self.root)+os.pathsep+os.environ['PATH']}

    def make(self, target, *args, existing=''):
        self.env['FAKE_EXISTING'] = existing
        return subprocess.run(['make', '--no-print-directory', '-s', '-C', str(ROOT), target,
                               'DOCKER='+str(self.root/'docker'), 'DATA_ROOT='+str(self.root/'data'), *args],
                              env=self.env, text=True, capture_output=True)

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_existing_flight_relay_is_reused(self):
        result = self.make('nostr-start-flight-data', existing='nostr-relay')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(c[0] in ('run','start') for c in self.calls()))

    def test_analysis_has_own_name_port_and_data(self):
        result = self.make('nostr-start-analysis', existing='nostr-relay')
        self.assertEqual(result.returncode, 0, result.stderr)
        run = next(c for c in self.calls() if c[0] == 'run')
        self.assertEqual(run[run.index('--name')+1], 'nostr-relay-analysis')
        self.assertEqual(run[run.index('-p')+1], '127.0.0.1:7448:7447')
        self.assertIn('source='+str(self.root/'data/nostr-relay-analysis')+',target=/data', run[run.index('--mount')+1])

    def test_analysis_customization_and_health_url(self):
        result = self.make('nostr-check-analysis', 'NOSTR_ANALYSIS_CONTAINER=results', 'NOSTR_ANALYSIS_PORT=7555', existing='results')
        self.assertEqual(result.returncode, 0, result.stderr)
        curl = next(c for c in self.calls() if c[0] == 'curl')
        self.assertEqual(curl[-1], 'http://127.0.0.1:7555/')

    def test_sdr_uses_flight_relay_host_and_port(self):
        def dry_run(target, *args):
            return subprocess.run(['make', '--no-print-directory', '-n', '-o', 'build-sdr', '-o', 'build', '-C', str(ROOT), target, *args],
                                  env=self.env, text=True, capture_output=True, check=True).stdout
        for target in ('run-sdr', 'run-sdr-pi', 'run-local', 'run-local-pi'):
            with self.subTest(target=target):
                self.assertIn('ws://192.168.1.10:7555', dry_run(target, 'NOSTR_RELAY_HOST=192.168.1.10', 'NOSTR_PORT=7555'))
                self.assertIn('ws://127.0.0.1:7447', dry_run(target))
        self.assertIn('wss://remote.example/relay', dry_run('run-sdr', 'NOSTR_RELAY=wss://remote.example/relay'))
        self.assertIn('-nostr-relay ""', dry_run('run-sdr', 'NOSTR_RELAY='))

    def test_reject_shared_container_or_data(self):
        for args in [('NOSTR_ANALYSIS_CONTAINER=nostr-relay',), ('NOSTR_ANALYSIS_DATA_DIR='+str(self.root/'data/nostr-relay'),)]:
            result = self.make('nostr-start-analysis', *args)
            self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

if __name__ == '__main__':
    unittest.main()
