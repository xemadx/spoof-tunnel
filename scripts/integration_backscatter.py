#!/usr/bin/env python3
"""Check firewall suppression and ownership in a loopback-only namespace."""
import os
import pathlib
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
name = f"spoof-backscatter-{os.getpid()}"
with tempfile.TemporaryDirectory(prefix="spoof-backscatter-") as temp:
    binary = str(pathlib.Path(temp) / "backscatter.test")
    subprocess.run(["go", "test", "-race", "-c", "-o", binary, "./internal/l3"], cwd=root, check=True)
    subprocess.run(["ip", "netns", "add", name], check=True)
    try:
        subprocess.run(["ip", "-n", name, "link", "set", "lo", "up"], check=True)
        subprocess.run(["ip", "netns", "exec", name, "env", "SPOOF_BACKSCATTER_ISOLATED=1", binary,
                        "-test.run", "^TestBackscatterIsolated$", "-test.v"], check=True, timeout=30)
    finally:
        subprocess.run(["ip", "netns", "del", name], check=True)
