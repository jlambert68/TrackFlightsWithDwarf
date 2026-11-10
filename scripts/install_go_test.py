import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("install_go", Path(__file__).with_name("install-go.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


def fake_release(archive, digest=None):
    return json.dumps([{"version": "go1.25.7", "stable": True, "files": [{"filename": "go1.25.7.linux-arm64.tar.gz", "os": "linux", "arch": "arm64", "kind": "archive", "sha256": digest or hashlib.sha256(archive).hexdigest()}]}]).encode()


def fake_archive():
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as tar:
        content = b"#!/bin/sh\necho 'go version go1.25.7 linux/arm64'\n"
        item = tarfile.TarInfo("go/bin/go")
        item.size = len(content)
        item.mode = 0o755
        tar.addfile(item, io.BytesIO(content))
    return output.getvalue()


class InstallerTest(unittest.TestCase):
    def test_architectures(self):
        self.assertEqual(installer.archive_arch("aarch64", 64), "arm64")
        self.assertEqual(installer.archive_arch("aarch64", 32), "armv6l")
        self.assertEqual(installer.archive_arch("armv7l", 32), "armv6l")
        self.assertEqual(installer.archive_arch("x86_64", 64), "amd64")

    def test_install_and_reuse(self):
        archive = fake_archive()
        def fetch(url, timeout):
            return io.BytesIO(fake_release(archive) if url == installer.METADATA_URL else archive)
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(installer.platform, "machine", return_value="aarch64"), mock.patch.object(installer.urllib.request, "urlopen", side_effect=fetch):
            root = Path(directory)
            installer.install(root)
            self.assertEqual((root / "current").resolve().name, "go1.25.7-linux-arm64")
            self.assertTrue((root / "current/bin/go").is_file())
            installer.install(root)

    def test_checksum_failure_preserves_current(self):
        archive = fake_archive()
        def fetch(url, timeout):
            return io.BytesIO(fake_release(archive, "0" * 64) if url == installer.METADATA_URL else archive)
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(installer.platform, "machine", return_value="aarch64"), mock.patch.object(installer.urllib.request, "urlopen", side_effect=fetch):
            root = Path(directory)
            (root / "previous").mkdir()
            (root / "current").symlink_to("previous")
            with self.assertRaisesRegex(ValueError, "SHA-256"):
                installer.install(root)
            self.assertEqual((root / "current").resolve().name, "previous")


if __name__ == "__main__":
    unittest.main()
