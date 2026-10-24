#!/usr/bin/env python3
"""Check module imports and Handler/Service/Repository package dependencies."""
import json
import os
import subprocess


def import_error(owner, source, target):
    if target.startswith("github.com/StuHelper/stuhelper-") and not (target == owner or target.startswith(owner + "/")):
        local = source.removeprefix(owner + "/")
        if not local.startswith(("cmd/", "examples/")):
            public_path = target.split("/", 3)[3:] or [""]
            if not public_path[0].startswith(("api/", "contracts/")) and public_path[0] not in {"api", "contracts"}:
                return f"Cross-module implementation import: {source} -> {target}"
    source_parts, target_parts = source.split("/"), target.split("/")
    if "handler" in source_parts and "repository" in target_parts:
        return f"Handler bypasses service: {source} -> {target}"
    if "service" in source_parts and "handler" in target_parts:
        return f"Service depends on handler: {source} -> {target}"
    if "repository" in source_parts and any(part in target_parts for part in ("handler", "service")):
        return f"Repository depends on an upper layer: {source} -> {target}"
    return None


def main():
    env = {**os.environ, "GOWORK": "off"}
    owner = subprocess.check_output(["go", "list", "-m"], env=env, text=True).strip()
    output = subprocess.check_output(["go", "list", "-json", "./..."], env=env, text=True)
    decoder = json.JSONDecoder()
    errors = []
    while output.strip():
        package, end = decoder.raw_decode(output.lstrip())
        output = output.lstrip()[end:]
        for dependency in package.get("Imports", []):
            error = import_error(owner, package["ImportPath"], dependency)
            if error:
                errors.append(error)
    if errors:
        raise SystemExit("\n".join(errors))
    print("PASS: module import and layer boundaries")


if __name__ == "__main__":
    main()
