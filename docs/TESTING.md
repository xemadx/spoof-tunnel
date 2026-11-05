# Validation and regressions

Use Linux/WSL with Go from go.mod, Node.js 24, npm, iproute2 and TUN. Raw-socket integration tests require root and run in isolated namespaces; they send no public spoof traffic.

## Checks

```sh
go test -race ./internal/l3 ./internal/tester
(cd panel/backend && go test -race ./internal/... && go vet ./internal/...)
bash scripts/build.sh
(cd panel/frontend && npx playwright install chromium && npm run test:ui)
SPOOF_PANEL_BINARY=dist/spoof-panel python3 scripts/smoke_panel.py
sudo env SPOOF_L3_BINARY="$PWD/dist/spoof-l3" python3 scripts/integration_l3.py
sudo env SPOOF_L3_BINARY="$PWD/dist/spoof-l3" python3 scripts/integration_delay.py
sudo python3 scripts/integration_multipeer.py
```

The carrier matrix covers all 25 ordered send/receive combinations of ICMP, UDP, raw TCP, protocol 4 and protocol 253, plus five homogeneous carrier cases with reciprocal rotating-source allowlists. Every case checks inner ping, an exact 128 KiB TCP echo, UDP echo and authenticated heartbeat responses on both ends. The delayed-path test adds 1700 ms in each direction and requires a valid heartbeat while inner ping RTT exceeds three seconds.

The multi-peer integration test runs the real Go panel and engine with one Iran IP and three simultaneous foreign peers on an isolated virtual switch. For all five carriers (ICMP, UDP, raw TCP, IPIP and protocol 253), peers share receive ports/ICMP identifiers and the foreign spoof source but have separate keys, TUN subnets and public forward ports. Concurrent TCP and UDP probes verify distinct service replies, conflicting configurations are rejected, and stopping/restarting one link leaves the other operational.

Unit regressions cover immediate forwarder stop, TCP half-close, completed-connection goroutine cleanup, diagnostic restart joining, IPv4 iteration overflow, disabled forwarding persistence, subnet/interface conflicts, stale updates, shared password-reset revocation, and WebSocket header authentication. Native legacy transport regressions join receiving calls before freeing Rust handles and close UDP receivers/senders during traffic. Raw tests skip when not run as root.

Playwright uses the real static production output with controlled API responses to check desktop/mobile controls, theme hydration, login errors, diagnostic restoration, IP-pool state, traffic totals, focus management, UTF-8 password lengths slow-response ordering, and the locally served Persian Vazirmatn font. This is separate from smoke_panel.py, which exercises the built Go HTTP server and a temporary real SQLite database, including administrator upgrade/reset behavior.

## Legacy source

The retained UDP relay uses Rust/CGo; the new TUN engine does not. To validate the default legacy library and its Go wrappers:

```sh
(cd rust && cargo test --locked -p spoof-transport && cargo build --locked --release -p spoof-transport)
go test -race ./cmd/... ./internal/...
sudo env "PATH=$PATH" go test -race ./internal/transport
```

Optional XDP requires building the eBPF program and loader with its separate toolchain/kernel support. It is not part of the production TUN carrier matrix.

## CI and limits

The active validation workflow is `.github/workflows/panel-test.yml`; legacy tests are in `test.yml`. Tagged releases run validation before creating amd64 and arm64 packages through `scripts/build.sh` and `scripts/package.py`. Cross-compiled arm64 binaries are packaged rather than executed on amd64 runners. Lint, TypeScript checks and production build errors fail validation.

These checks establish reproducible behavior in the tested environments. They do not establish provider spoof acceptance, censorship resistance, sustained production throughput, every browser/device, an independent cryptographic audit, or infinite-duration stability. Test the actual VPS pair before relying on it.

## CI cache and frontend reuse

npm's download cache is restored using the frontend lockfile. Go module and compiled-build caches include both module dependency files; validation and each release architecture have separate keys, with a commit suffix so successful runs save updated incremental caches. Release jobs download the exact static frontend artifact that passed validation instead of reinstalling Node dependencies and rebuilding the same UI twice. A missing frontend artifact fails packaging. Cache misses always rebuild normally; network integration tests run on every validation and are not cached.


## v0.3.9 review regressions

See [review assessment and probe compatibility](REVIEW-2026-11-04.md). `integration_tester.py` includes immediate startup, maximum 1000-packet ICMP/TCP delivery, rejected/duplicate accounting and canceled-send coverage in a loopback-only namespace. `integration_multipeer.py` also verifies that live edits return 409 without stopping the core and that recovery state is exported. `integration_forward.py` checks a real delayed TUN startup, cancellation and fail-closed routing in a private namespace. The browser suite now has 19 scenarios. Unprivileged Go tests check synchronous raw-socket permission errors; privileged namespace tests prove reception.


## v0.3.10 connection-card layout

The browser suite has 21 scenarios. Two new regressions reproduce mixed Persian/Latin tunnel names and asymmetric carriers in dark desktop (1132 px) and light mobile (320 px) views. They check that role/carrier/interface values match their labels, Latin values retain LTR direction, chips and their text do not overlap, the name does not overlap the status, and the document has no horizontal overflow. Card screenshots are saved in Playwright's test output for visual inspection.

## v0.4.0 smart pool regressions

The browser suite now has 31 scenarios, including permanent default/TXT group separation, staged upload, revision conflicts, draft preservation, deletion confirmation and desktop/mobile pool layouts. API regressions import 260,000 rows across two files into 200,000 unique selected sources, enforce group ownership and capacity, and prove a stalled upload cannot block Start/Stop.

Scheduler regressions cover full 600-second quarantine/retest, reverse-path ambiguity, small/MTU probe alternation, source attribution, adaptive timeout for slow paths, healthy round-robin fairness and restart checkpoints. Raw Linux tests also prove quality probes continue during a 15,000-source outgoing manifest and active-source probes bypass a 3,000-source backlog. They exercise all five carriers, a source-specific drop and atomic/repaired/replay-resistant peer manifests. Fresh single-use hello challenges prevent old confirmations from refreshing the control reference.

```sh
sudo env SPOOF_SMART_POOL=1 SPOOF_L3_BINARY="$PWD/dist/spoof-l3" python3 scripts/integration_l3.py
sudo env SPOOF_SMART_POOL=1 SPOOF_L3_BINARY="$PWD/dist/spoof-l3" python3 scripts/integration_delay.py
sudo go test -race ./internal/l3
```

The smart matrix transfers actual inner TCP/UDP payloads on five carriers and asymmetric ICMP/TCP, then restarts just one endpoint and verifies recovery. The adaptive delayed-path test uses 3.4-second RTT. These namespace tests establish local behavior; real provider routing/filtering still requires testing the deployed pair.

## v0.4.1 pool, update and log regressions

There are 43 browser scenarios. New cases reproduce clearing the active default while a TXT is only checked locally, both save controls, update confirmation, HTTP disconnect/rollback, a single successful reload with the new header version, retained login, visible percentage, unsupported installs, restored progress, readable log reasons, reconnect replacement and an 800-line burst bound.

Go race tests exercise all three smart dispatch modes through selection, source failure, quarantine, restart, full cooldown and two-response recovery. Reverse outages expire stale quality without blacklisting all sources; ineligible sources disappear from active telemetry. Authenticated raw control now covers all 25 ordered carrier pairs and peer readiness. Real TUN checks cover adaptive links plus round-robin/failover on ICMP/TCP, UDP/IPIP and protocol 253. Three-peer tests import TXT and atomically clear defaults through real HTTP before starting the cores, on all five carriers.

The updater suite builds two API/SQLite executables and replaces/restarts real processes using isolated service hooks. It verifies a successful upgrade, unchanged JWT/password/group data, database-and-binary rollback after an injected migration/startup failure, checksum rejection, unsafe archive rejection, version mismatch, download bounds and real byte progress. A committed WAL backup test verifies snapshot contents. The production systemd sandbox boundary is exercised separately with unique fixture services (`scripts/integration_updater_systemd.py`), without modifying any installed panel. Log tests verify atomic history/subscription boundaries and slow-subscriber resynchronization.

The workflow builds/validates once, shares the exact binary and privileged race-test artifacts, and runs independent carrier, smart-policy, raw, diagnostics and multi-peer suites in parallel. Every network job must pass before tagged packages are released. ARM64 packages are cross-compiled and their architecture/checksums checked; no native ARM64 runtime test is claimed.
