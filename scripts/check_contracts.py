#!/usr/bin/env python3
"""Validate contracts and compare every existing API with its PR base version."""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
TOOL = ["go", "run", "github.com/oasdiff/oasdiff@v1.34.1"]


def main():
    base = os.environ.get("CONTRACT_BASE_REF", "HEAD")
    subprocess.run(["git", "rev-parse", "--verify", f"{base}^{{commit}}"], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
    for name in ("platform", "module"):
        spec = f"contracts/{name}.openapi.yaml"
        subprocess.run([*TOOL, "validate", spec, "--allow-external-refs=false", "--fail-on", "WARN"], cwd=ROOT, check=True)
        previous = subprocess.run(["git", "show", f"{base}:{spec}"], cwd=ROOT, capture_output=True)
        if previous.returncode:
            # Verify absence, instead of treating every Git error as a new API.
            paths = subprocess.check_output(["git", "ls-tree", "--name-only", base, "--", spec], cwd=ROOT)
            if paths.strip():
                raise RuntimeError(f"Cannot read prior contract: {spec}")
            print(f"Validated initial API definition: {spec}; no prior version in base")
            continue
        with tempfile.TemporaryDirectory(prefix="stuhelper-contract-") as directory:
            prior = Path(directory) / "base.yaml"
            prior.write_bytes(previous.stdout)
            subprocess.run([*TOOL, "breaking", str(prior), spec, "--allow-external-refs=false", "--fail-on", "WARN"], cwd=ROOT, check=True)
    print("PASS: OpenAPI validation and compatibility for all existing base contracts")


if __name__ == "__main__":
    main()
