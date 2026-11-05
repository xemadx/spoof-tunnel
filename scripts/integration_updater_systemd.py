#!/usr/bin/env python3
"""Prove a root panel sandbox can start a separate sandboxed update service.

Only unique fixture units and a private /run directory are used. No installed
panel, tunnel, application database, or production service is touched.
"""
import os, pathlib, re, subprocess, tempfile, time

if os.geteuid() != 0:
    raise SystemExit('run as root')
if pathlib.Path('/proc/1/comm').read_text().strip() != 'systemd':
    print('SKIP: host PID 1 is not systemd; update process/rollback tests still run in Go')
    raise SystemExit(0)

root = pathlib.Path(__file__).resolve().parents[1]
directory = pathlib.Path(tempfile.mkdtemp(prefix='spoof-update-test-', dir='/run'))
identifier = directory.name.removeprefix('spoof-update-test-')
helper = f'spoof-update-fixture-{identifier}.service'
caller = f'spoof-update-caller-{identifier}'
unit_path = pathlib.Path('/run/systemd/system') / helper
assert not unit_path.exists() and unit_path.parent == pathlib.Path('/run/systemd/system')
marker = directory / 'independent-worker-ran'

def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=30)

try:
    text = (root / 'scripts/install.sh').read_text()
    match = re.search(r"<<'UPDATEUNIT'\n(.*?)\nUPDATEUNIT", text, re.S)
    assert match, 'installer must ship the independent updater unit'
    unit = match[1].replace('ExecStart=/opt/spoof-panel/spoof-panel --apply-update', f'ExecStart=/usr/bin/touch {marker}')
    unit = unit.replace('ReadWritePaths=/etc/spoof-panel /opt/spoof-panel', f'ReadWritePaths={directory}')
    unit_path.write_text(unit + '\n')
    run('systemctl', 'daemon-reload')
    run('systemd-analyze', 'verify', str(unit_path))
    run('systemd-run', '--quiet', '--wait', '--collect', f'--unit={caller}',
        '-p', 'NoNewPrivileges=true',
        '-p', 'CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE',
        '-p', 'AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE',
        '-p', 'ProtectSystem=strict', '-p', f'ReadWritePaths={directory}',
        '-p', 'ProtectHome=true', '-p', 'ProtectKernelTunables=true',
        '-p', 'PrivateTmp=true', '-p', 'KillMode=mixed',
        '/usr/bin/systemctl', 'start', '--no-block', helper)
    deadline = time.monotonic() + 5
    while not marker.exists() and time.monotonic() < deadline:
        time.sleep(.05)
    assert marker.exists(), 'worker did not survive the initiating service exit'
    assert run('systemctl', 'show', '--property=Result', '--value', helper).stdout.strip() == 'success'
    print('PASS: restricted panel starts independent updater; helper writes permitted state and survives caller exit')
finally:
    subprocess.run(['systemctl', 'stop', helper], capture_output=True, timeout=15)
    subprocess.run(['systemctl', 'reset-failed', helper], capture_output=True, timeout=15)
    unit_path.unlink(missing_ok=True)
    subprocess.run(['systemctl', 'daemon-reload'], capture_output=True, timeout=15)
    marker.unlink(missing_ok=True)
    directory.rmdir()
