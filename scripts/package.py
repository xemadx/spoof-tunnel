#!/usr/bin/env python3
"""Assemble dist/ and the release tarball with POSIX (LF) line endings.

Windows tooling happily rewrites text files with CRLF, which breaks
`sha256sum -c` and shell scripts on the target Linux host. This script is the
single source of truth for packaging, so the archive is always LF-clean.
"""
import hashlib
import pathlib
import os
import shutil
import tarfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
DIST = ROOT / "dist"
ARCH = os.environ.get("SPOOF_ARCH", "amd64")
if ARCH not in {"amd64", "arm64"}:
    raise SystemExit("supported package architectures: amd64, arm64")
PREFIX = f"spoof-tunnel-linux-{ARCH}"
BINARIES = ["spoof-l3", "spoof-panel"]
TEXT_FILES = ["setup-credentials.sh", "install.sh", "README.md", "README-fa.md", "LICENSE", "NOTICE", "SHA256SUMS"]


def sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def normalized(path: pathlib.Path) -> pathlib.Path:
    """Return a path whose bytes are LF-only, writing elsewhere if needed."""
    data = path.read_bytes()
    if b"\r\n" not in data:
        return path
    fixed = path.parent / f".lf-{path.name}"
    fixed.write_bytes(data.replace(b"\r\n", b"\n"))
    return fixed


def main() -> None:
    DIST.mkdir(exist_ok=True)

    for name in BINARIES:
        if not (DIST / name).is_file():
            raise SystemExit(f"missing binary {DIST / name}; build it first")

    # Installer must ship the local setup script, never the download shim.
    shutil.copy2(ROOT / "scripts" / "install.sh", DIST / "install.sh")
    shutil.copy2(ROOT / "scripts" / "setup-credentials.sh", DIST / "setup-credentials.sh")
    for name in ["README.md", "README-fa.md", "LICENSE", "NOTICE"]:
        shutil.copy2(ROOT / name, DIST / name)

    for name in ["setup-credentials.sh", "install.sh", "README.md", "README-fa.md", "LICENSE", "NOTICE"]:
        source = DIST / name
        source.write_bytes(source.read_bytes().replace(b"\r\n", b"\n"))
    sums = "".join(f"{sha256(DIST / name)}  {name}\n" for name in BINARIES + ["install.sh", "setup-credentials.sh"])
    (DIST / "SHA256SUMS").write_bytes(sums.encode("utf-8"))
    print("SHA256SUMS:\n" + sums)

    docs = DIST / "docs"
    if docs.exists():
        shutil.rmtree(docs)
    shutil.copytree(ROOT / "docs", docs)

    archive = DIST / f"{PREFIX}.tar.gz"
    with tarfile.open(archive, "w:gz") as tar:
        for name in TEXT_FILES:
            tar.add(normalized(DIST / name), arcname=f"{PREFIX}/{name}")
        for name in BINARIES:
            tar.add(DIST / name, arcname=f"{PREFIX}/{name}")
        tar.add(docs, arcname=f"{PREFIX}/docs")

    for stale in DIST.glob(".lf-*"):
        stale.unlink()
    print(f"Packaged {archive.name}: {archive.stat().st_size:,} bytes")


if __name__ == "__main__":
    main()
