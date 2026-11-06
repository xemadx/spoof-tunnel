#!/usr/bin/env python3
"""Test the real TLS listener and installer health check without public ACME."""
import os
import pathlib
import socket
import subprocess
import tempfile
import threading
import time

root = pathlib.Path(__file__).resolve().parent.parent
binary = str(pathlib.Path(os.environ.get("SPOOF_PANEL_BINARY", root / "dist/spoof-panel")).resolve())
domain = "panel.example.com"

with tempfile.TemporaryDirectory(prefix="spoof-tls-") as directory:
    work = pathlib.Path(directory)
    data = work / "data"
    env = {**os.environ, "SPOOF_DATA_DIR": str(data), "SPOOF_PUBLIC_IP": "192.0.2.10"}

    def run(args, **kwargs):
        return subprocess.run(args, env=env, check=True, capture_output=True, text=True, **kwargs).stdout

    # A private fixture CA exercises curl's actual chain and hostname checks.
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
         "-subj", "/CN=Spoof TLS fixture CA", "-keyout", str(work / "ca.key"), "-out", str(work / "ca.pem")])
    run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", f"/CN={domain}",
         "-keyout", str(work / "server.key"), "-out", str(work / "server.csr")])
    (work / "extensions").write_text(f"subjectAltName=DNS:{domain}\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n")
    run(["openssl", "x509", "-req", "-in", str(work / "server.csr"), "-CA", str(work / "ca.pem"),
         "-CAkey", str(work / "ca.key"), "-CAcreateserial", "-days", "1", "-extfile", str(work / "extensions"),
         "-out", str(work / "server.pem")])
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    run([binary, "--setup", "--setup-user", "fixtureadmin", "--setup-pass", "fixture-password-123", "--setup-port", str(port)])
    before = (data / "credentials.txt").read_text()
    prefix = next(line.split(": ", 1)[1] for line in before.splitlines() if line.startswith("Web Path:"))
    run([binary, "--set-tls", "--tls-domain", domain, "--tls-cert", str(work / "server.pem"), "--tls-key", str(work / "server.key")])
    status = run([binary, "--tls-status"])
    assert "enabled=true\n" in status and "valid=true\n" in status, status
    displayed = run([binary, "--setup"])
    expected = f"https://{domain}:{port}{prefix}/"
    assert expected in displayed and f"URL: {expected}\n" in (data / "credentials.txt").read_text()
    assert "Username: fixtureadmin" in (data / "credentials.txt").read_text()
    assert "unchanged (not stored)" in displayed

    source = (root / "scripts/install.sh").read_text()
    functions = source[source.index("# ── TLS certificates"):source.index("# Verify checksums.")]
    script = work / "verify.sh"
    script.write_text('set -euo pipefail\n' + functions + '\nspoof_tls_verify "$1" "$2" "$3"\n')
    # A hostile proxy setting and delayed listener reproduced the false-negative
    # health check. Use the exact installer function, not a duplicate curl call.
    env.update(CURL_CA_BUNDLE=str(work / "ca.pem"), HTTPS_PROXY="http://127.0.0.1:9",
               ALL_PROXY="http://127.0.0.1:9", NO_PROXY="", no_proxy="")
    processes = []
    log = (work / "panel.log").open("w")

    def start_later():
        time.sleep(2)
        processes.append(subprocess.Popen([binary, "--bind", "127.0.0.1"], env=env, stdout=log, stderr=log))

    starter = threading.Thread(target=start_later)
    starter.start()
    try:
        run(["bash", str(script), domain, str(port), prefix], timeout=55)
        starter.join()
        wrong = subprocess.run(["bash", str(script), "wrong.example.com", str(port), prefix], env=env, capture_output=True, text=True, timeout=10)
        assert wrong.returncode != 0 and "Certificate trust verification failed" in wrong.stdout, wrong
        log.flush()
        assert expected in (work / "panel.log").read_text()
        print("PASS real TLS: boolean status, domain URL, preserved account/port/path, delayed listener, proxy bypass, trusted chain and hostname rejection")
    finally:
        starter.join()
        for process in processes:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        log.close()
