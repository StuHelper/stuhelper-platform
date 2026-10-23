#!/usr/bin/env python3
"""Run the pinned official actionlint binary on Linux x86_64 runners."""

import hashlib
import io
import platform
import subprocess
import tarfile
import tempfile
from pathlib import Path

VERSION = "1.7.12"
SHA256 = "8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8"
URL = (f"https://github.com/rhysd/actionlint/releases/download/v{VERSION}/"
       f"actionlint_{VERSION}_linux_amd64.tar.gz")
ROOT = Path(__file__).resolve().parents[1]


def main():
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "AMD64"):
        raise SystemExit("This CI tool requires Linux x86_64; run actionlint locally for other platforms.")
    with tempfile.TemporaryDirectory(prefix="stuhelper-actionlint-") as directory:
        archive = Path(directory) / "actionlint.tar.gz"
        subprocess.run(["curl", "--fail", "--silent", "--show-error", "--location",
                        "--retry", "2", "--retry-all-errors", "--connect-timeout", "10",
                        "--max-time", "45", "--output", str(archive), URL], check=True)
        data = archive.read_bytes()
        if hashlib.sha256(data).hexdigest() != SHA256:
            raise SystemExit("actionlint archive checksum mismatch")
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as bundle:
            member = bundle.getmember("actionlint")
            if not member.isfile() or member.size > 30_000_000:
                raise SystemExit("Unexpected actionlint archive entry")
            source = bundle.extractfile(member)
            if source is None:
                raise SystemExit("Missing actionlint executable")
            binary = Path(directory) / "actionlint"
            binary.write_bytes(source.read())
            binary.chmod(0o700)
        subprocess.run([str(binary), "-color", "-shellcheck=", "-pyflakes="], cwd=ROOT, check=True)
    print("PASS: actionlint " + VERSION)


if __name__ == "__main__":
    main()
