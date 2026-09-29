#!/usr/bin/env python3
"""Build the four standalone release archives and their SHA-256 checksums."""
import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = (("darwin", "amd64"), ("darwin", "arm64"), ("linux", "amd64"), ("linux", "arm64"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", help="Require a release tag matching VERSION")
    args = parser.parse_args()
    version = (ROOT / "VERSION").read_text().strip()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        parser.error("VERSION must contain a semantic version without v")
    if args.tag is not None and args.tag != "v" + version:
        parser.error(f"tag {args.tag!r} does not match VERSION (v{version})")
    out = ROOT / "dist" / version
    out.mkdir(parents=True, exist_ok=True)
    checksums = []
    with tempfile.TemporaryDirectory(prefix="hop-release-") as temp:
        binary = Path(temp) / "hop"
        for system, arch in TARGETS:
            env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0")
            subprocess.run([os.environ.get("GO", "go"), "build", "-trimpath", "-buildvcs=false",
                            f"-ldflags=-s -w -X main.version={version}", "-o", str(binary), "./cmd/hop"],
                           cwd=ROOT, env=env, check=True)
            archive = out / f"hop_{version}_{system}_{arch}.tar.gz"
            def metadata(info):
                info.uid = info.gid = 0
                info.uname = info.gname = ""
                return info
            with tarfile.open(archive, "w:gz") as tar:
                tar.add(binary, arcname="hop", filter=metadata)
                for name in ("README.md", "CHANGELOG.md", "LICENSE"):
                    if (ROOT / name).is_file():
                        tar.add(ROOT / name, arcname=name, filter=metadata)
            checksums.append(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}\n")
            print(archive.relative_to(ROOT), flush=True)
    (out / "SHA256SUMS").write_text("".join(checksums))


if __name__ == "__main__":
    main()
