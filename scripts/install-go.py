#!/usr/bin/env python3
"""Install the latest stable Go toolchain for this Linux machine."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import struct
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

METADATA_URL = "https://go.dev/dl/?mode=json"
DOWNLOAD_URL = "https://go.dev/dl/"


def archive_arch(machine, bits):
    if machine in ("aarch64", "arm64"):
        return "arm64" if bits == 64 else "armv6l"
    if machine.startswith("armv") or machine == "arm":
        return "armv6l"
    if machine in ("x86_64", "amd64"):
        return "amd64" if bits == 64 else "386"
    if re.fullmatch(r"i[3-6]86", machine):
        return "386"
    raise ValueError(f"Unsupported Linux architecture: {machine}")


def select_release(releases, arch):
    release = next((item for item in releases if item.get("stable") is True), None)
    if release is None:
        raise ValueError("No stable Go release found")
    version = release.get("version", "")
    if not re.fullmatch(r"go\d+\.\d+(?:\.\d+)?", version):
        raise ValueError("Invalid Go version in release metadata")
    filename = f"{version}.linux-{arch}.tar.gz"
    artifact = next((f for f in release.get("files", []) if f.get("filename") == filename and f.get("os") == "linux" and f.get("arch") == arch and f.get("kind") == "archive"), None)
    if artifact is None or not re.fullmatch(r"[0-9a-f]{64}", artifact.get("sha256", "")):
        raise ValueError(f"No verified {filename} archive in the latest release")
    return version, filename, artifact["sha256"]


def install(root):
    if platform.system() != "Linux":
        raise ValueError("This installer supports Linux only")
    arch = archive_arch(platform.machine().lower(), struct.calcsize("P") * 8)
    with urllib.request.urlopen(METADATA_URL, timeout=30) as response:
        version, filename, expected = select_release(json.load(response), arch)
    root.mkdir(parents=True, exist_ok=True)
    with (root / ".install.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        current = root / "current"
        if current.exists() and not current.is_symlink():
            raise ValueError(f"Refusing to replace non-symlink {current}")
        destination = root / f"{version}-linux-{arch}"
        if not destination.exists():
            print(f"Downloading {filename}...", flush=True)
            with tempfile.TemporaryDirectory(prefix=".go-install-", dir=root) as tmp:
                tmp = Path(tmp)
                archive = tmp / filename
                digest = hashlib.sha256()
                with urllib.request.urlopen(DOWNLOAD_URL + filename, timeout=60) as response, archive.open("wb") as output:
                    while chunk := response.read(1024 * 1024):
                        digest.update(chunk)
                        output.write(chunk)
                if digest.hexdigest() != expected:
                    raise ValueError("Go download SHA-256 did not match the official metadata")
                with tarfile.open(archive, "r:gz") as bundle:
                    # Go archives must contain only regular files and directories under go/.
                    for member in bundle:
                        parts = Path(member.name).parts
                        if not parts or parts[0] != "go" or ".." in parts or not (member.isdir() or member.isfile()):
                            raise ValueError(f"Unexpected archive entry: {member.name}")
                    bundle.extractall(tmp, filter="data")
                go = tmp / "go" / "bin" / "go"
                if not go.is_file():
                    raise ValueError("Go binary missing from archive")
                subprocess.run([str(go), "version"], check=True)
                os.rename(tmp / "go", destination)
        elif not (destination / "bin" / "go").is_file():
            raise ValueError(f"Existing installation is incomplete: {destination}")
        # Replace only our symlink; preserve previous version directories.
        link = root / f".current-{os.getpid()}"
        try:
            link.symlink_to(destination.name)
            os.replace(link, current)
        finally:
            link.unlink(missing_ok=True)
        print(f"Installed {version} for linux/{arch} at {destination}")
        print(f"Make will use {current / 'bin' / 'go'}; to use it in your shell:")
        print(f'export PATH="{current / "bin"}:$PATH"')


def main():
    if len(sys.argv) != 2:
        raise ValueError("Usage: install-go.py INSTALL_DIRECTORY")
    install(Path(sys.argv[1]).expanduser().absolute())


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError, subprocess.SubprocessError, tarfile.TarError) as err:
        print(f"Go installation failed: {err}", file=sys.stderr)
        sys.exit(1)
