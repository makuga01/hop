"""Exercise the piped installer with isolated downloads and destinations."""

import hashlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "install.sh"


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="hop-installer-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.downloads = self.root / "downloads"
        self.tools = self.root / "tools"
        self.scratch = self.root / "scratch"
        self.destination = self.root / "custom install" / "bin"
        for directory in [self.downloads, self.tools, self.scratch]:
            directory.mkdir()
        self.env = dict(os.environ, HOP_INSTALL_DIR=str(self.destination),
                        TMPDIR=str(self.scratch), FIXTURES=str(self.downloads),
                        DOWNLOAD_LOG=str(self.root / "requests"), TEST_SYSTEM="Darwin",
                        TEST_ARCH="arm64", PATH=str(self.tools) + os.pathsep + os.environ["PATH"])
        self.write_tool("uname", '''
import os, sys
print(os.environ["TEST_SYSTEM" if sys.argv[1] == "-s" else "TEST_ARCH"])
''')
        self.write_tool("curl", '''
import os, pathlib, shutil, sys
args = sys.argv[1:]
url = args[-1]
with open(os.environ["DOWNLOAD_LOG"], "a") as log:
    log.write(url + "\\n")
if os.environ.get("FAIL_DOWNLOAD"):
    sys.exit(22)
name = url.rsplit("/", 1)[1]
shutil.copyfile(pathlib.Path(os.environ["FIXTURES"]) / name, args[args.index("-o") + 1])
''')
        sums = []
        for platform in ["darwin", "linux"]:
            for arch in ["arm64", "amd64"]:
                name = f"hop_0.1.0_{platform}_{arch}.tar.gz"
                payload = b"#!/bin/sh\nprintf 'hop 0.1.0\\n'\n"
                with tarfile.open(self.downloads / name, "w:gz") as archive:
                    member = tarfile.TarInfo("hop")
                    member.size = len(payload)
                    member.mode = 0o755
                    archive.addfile(member, io.BytesIO(payload))
                digest = hashlib.sha256((self.downloads / name).read_bytes()).hexdigest()
                sums.append(f"{digest}  {name}\n")
        (self.downloads / "SHA256SUMS").write_text("".join(sums))

    def write_tool(self, name, source):
        file = self.tools / name
        file.write_text(f"#!{sys.executable}\n" + source)
        file.chmod(0o755)

    def run_installer(self):
        result = subprocess.run(["sh"], input=SCRIPT.read_text(), text=True,
                                env=self.env, capture_output=True, timeout=15)
        self.assertEqual(list(self.scratch.iterdir()), [], "temporary downloads were left behind")
        if self.destination.exists():
            self.assertEqual(list(self.destination.glob(".hop-install.*")), [])
        return result

    def test_supported_platforms_and_custom_directory(self):
        for system, arch, suffix in [("Darwin", "arm64", "darwin_arm64"),
                                      ("Darwin", "x86_64", "darwin_amd64"),
                                      ("Linux", "aarch64", "linux_arm64"),
                                      ("Linux", "x86_64", "linux_amd64")]:
            with self.subTest(system=system, arch=arch):
                self.env.update(TEST_SYSTEM=system, TEST_ARCH=arch)
                path_before = self.env["PATH"]
                result = self.run_installer()
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.env["PATH"], path_before)
                binary = self.destination / "hop"
                self.assertEqual(subprocess.check_output([binary, "--version"], text=True), "hop 0.1.0\n")
                self.assertIn(f'Run: "{binary}"', result.stdout)
                requests = (self.root / "requests").read_text().splitlines()
                self.assertEqual(requests[-2], "https://github.com/makuga01/hop/releases/latest/download/SHA256SUMS")
                self.assertEqual(requests[-1], f"https://github.com/makuga01/hop/releases/download/v0.1.0/hop_0.1.0_{suffix}.tar.gz")

    def test_bad_checksum_preserves_existing_install(self):
        self.destination.mkdir(parents=True)
        binary = self.destination / "hop"
        binary.write_text("old installation")
        (self.downloads / "hop_0.1.0_darwin_arm64.tar.gz").write_bytes(b"bad download")
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Checksum mismatch", result.stderr)
        self.assertEqual(binary.read_text(), "old installation")

    def test_download_failure_does_not_create_destination(self):
        self.env["FAIL_DOWNLOAD"] = "1"
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Download failed", result.stderr)
        self.assertFalse(self.destination.exists())

    def test_unsupported_system_stops_before_downloading(self):
        self.env["TEST_SYSTEM"] = "FreeBSD"
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / "requests").exists())
        self.assertFalse(self.destination.exists())

    def test_symlink_destination_is_not_replaced(self):
        self.destination.mkdir(parents=True)
        target = self.root / "managed-hop"
        target.write_text("managed installation")
        (self.destination / "hop").symlink_to(target)
        result = self.run_installer()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Refusing to replace", result.stderr)
        self.assertEqual(target.read_text(), "managed installation")
        self.assertTrue((self.destination / "hop").is_symlink())


if __name__ == "__main__":
    unittest.main()
