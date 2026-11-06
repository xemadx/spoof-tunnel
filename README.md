<div align="center">

# Spoof Tunnel

**Authenticated IPv4 TUN links. A Persian panel for every connection.**

[![Release](https://img.shields.io/github/v/release/xemadx/spoof-tunnel)](https://github.com/xemadx/spoof-tunnel/releases/latest)
[![Validation](https://github.com/xemadx/spoof-tunnel/actions/workflows/panel-test.yml/badge.svg)](https://github.com/xemadx/spoof-tunnel/actions/workflows/panel-test.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**English** · [فارسی](README-fa.md) · [Releases](https://github.com/xemadx/spoof-tunnel/releases) · [Operations](docs/OPERATIONS.md) · [Testing](docs/TESTING.md)

</div>

![Current Persian dashboard in dark mode](docs/images/dashboard.png)

*Actual v0.3.1 panel, served by the Go backend with a temporary SQLite database. The three configurations use documentation addresses and are stopped; this screenshot is not a live VPS performance claim.*

## What it does

`spoof-l3` creates an encrypted, point-to-point IPv4 TUN link between two Linux servers. Applications use its private addresses to exchange **TCP, UDP and ICMP**, independently of the outer packet carrier. The panel manages configuration, tunnel processes, authenticated peer status, forwarding and logs.

| Network engine | Management panel |
| --- | --- |
| ICMP, UDP, raw TCP, IP protocol 4 and protocol 253 | Persian RTL interface with locally served **Vazirmatn** |
| Independently selectable send/receive carriers | Complementary JSON configuration builder and import/export |
| AES-256-GCM, directional keys and replay protection | Dark/light themes and responsive navigation |
| Authenticated challenge/response heartbeat | Separate process state and authenticated peer state |
| Independent TUN interfaces and subnets | TCP entry-port forwarding to the selected peer |
| Reciprocal allowlists for rotating spoof sources | Live logs, traffic counters and SQLite persistence |

The current TUN engine and panel are Go binaries; they require no Rust or eBPF installation. The original UDP relay is retained separately in source.

## Install or update

Run as **root on each server**. Supported targets: Linux **amd64** and **arm64**, with systemd, iproute2, `/dev/net/tun`, `curl`, `tar` and `sha256sum`.

```sh
curl -fsSL https://raw.githubusercontent.com/xemadx/spoof-tunnel/main/install.sh | bash
```

The same command updates an existing installation to the **latest published Release**. It downloads the matching architecture, checks published checksums, installs the binary pair and restarts `spoof-panel`. Pushing source alone does not update this download; a Release with built assets must exist.

- First installation prints the administrator password and panel URL. Keep the password; it is not stored in recoverable form.
- Updates preserve the current database, password and configurations. Back up `/etc/spoof-panel` with the service stopped before upgrading.
- The panel and engine versions are stamped at build time. The header shows the running panel's version.
- The panel serves HTTP. Use an HTTPS reverse proxy or SSH forwarding for remote administration.

To explicitly install **v0.4.9**:

```sh
curl -fsSL https://raw.githubusercontent.com/xemadx/spoof-tunnel/v0.4.9/install.sh | SPOOF_RELEASE=v0.4.9 bash
```

Release packages are `spoof-tunnel-linux-amd64.tar.gz` and `spoof-tunnel-linux-arm64.tar.gz`, accompanied by archive `SHA256SUMS` and a signed `SHA256SUMS.sig`. No Go, Node.js or Rust toolchain is needed on the VPS.

## Connect two servers

1. Open the panel on both servers. On one, choose **ساخت اتصال** (paired connection builder).
2. Enter each server's **real IPv4 address** and **spoofed source IPv4 address**. Real addresses are packet destinations; spoofed addresses are the expected outer sources.
3. Select the upload/download carriers, receive ports or ICMP identifiers, and an unused private `/30` subnet.
4. Choose which server this panel represents. Generate both configurations; only the selected end is saved locally.
5. Download the other end's JSON and import it in **اتصال‌ها** on the other server. **The file contains the PSK: keep it private.**
6. Start both ends. A running process alone is not proof of a connection; wait for an authenticated peer response.
7. Test the private link, then your service:

```sh
# Iran side, for the first example subnet
ping -c 3 10.77.0.2
```

A service on the foreign host must listen on its TUN address or an appropriate wildcard address. The panel does not change the default route or automatically create a public Internet gateway.

## One Iran server, multiple foreign peers

Create a **separate pair for every foreign server**. Each pair gets a distinct key and interface; use a non-overlapping subnet. The builder suggests a free subnet from configurations stored in that panel, and the backend rejects recorded subnet/interface conflicts.

| Link | Private subnet | Iran TUN IP | Foreign TUN IP | Suggested Iran receive port/ID |
| --- | --- | --- | --- | --- |
| Iran ↔ foreign A | `10.77.0.0/30` | `10.77.0.1` | `10.77.0.2` | `5001` |
| Iran ↔ foreign B | `10.77.0.4/30` | `10.77.0.5` | `10.77.0.6` | `5002` |
| Iran ↔ foreign C | `10.77.0.8/30` | `10.77.0.9` | `10.77.0.10` | `5003` |

**Receive ports do not automatically increment.** Choose separate values for clarity and reduced cross-processing. Shared carrier ports are supported with independent keys; the integration test also exercises identical foreign spoof sources. Using the same key/configuration for different links defeats this separation. The panel checks its saved configurations, not every route or interface on the host: avoid existing LAN/VPN ranges too.

### Which port is which?

| Setting | Meaning |
| --- | --- |
| Carrier receive port | Outer UDP/raw-TCP destination port, matched by the other end's send configuration |
| ICMP identifier | The carrier's numeric identifier; it is not a TCP service port |
| Protocol 4 / 253 | No TCP/UDP carrier port; these carry encrypted project frames directly |
| Public forward port | A TCP listener on the entry server for application clients |
| Target service port | A service on the selected foreign TUN IP |
| Panel port | Administration only; separate from tunnel and application ports |

For example, Iran entry `2443 → 10.77.0.2:443`, `3443 → 10.77.0.6:443` and `4443 → 10.77.0.10:443` serve three different peers. Target port `443` can repeat on different foreign IPs; enabled public entry ports on the same Iran host must be unique. Configure rules in the tunnel editor's **فوروارد معکوس** tab. Rules follow the tunnel lifecycle. The entry listener uses `0.0.0.0`; permit only intended clients in your firewall. Entry forwarding is **TCP only**; inner UDP still traverses the TUN.

<details>
<summary><strong>More panel views: light theme, connections and mobile</strong></summary>

![Dashboard in light mode](docs/images/dashboard-light.png)
![Independent connection configurations](docs/images/connections.png)
<img src="docs/images/mobile.png" alt="Mobile connection list" width="390">

</details>

The dashboard health card has previous/next controls and a connection counter. Selection follows the connection ID through polling and reordering; deleting it selects an available connection.

## Diagnostics

| Symptom | Check first |
| --- | --- |
| Process fails to start | Instance logs, assigned local IP, TUN/raw-socket permissions and interface conflicts |
| Process runs, peer disconnected | Start the other end; match keys, real/spoof IPs, reciprocal carriers and receive values |
| Both ends run, no response | Provider source validation, firewall and reverse-path filtering |
| Peer responds, application fails | Private ping, service bind address, service port and explicit forwarding |
| Second link fails | Unique interface, non-overlapping subnet and correct exported peer configuration |

```sh
systemctl status spoof-panel
journalctl -u spoof-panel -n 100 --no-pager
/opt/spoof-panel/spoof-panel --version
/etc/spoof-panel/spoof-l3 --version
```

Lost administrator password? Reset it on the server; use 12–72 UTF-8 bytes. This invalidates previous login tokens:

```sh
/opt/spoof-panel/spoof-panel --setup --setup-pass 'YOUR_NEW_PASSWORD'
```

Rotating local spoof sources require the **same selected set in the other end's allowed-peer-source list**. Updated engines can exchange authenticated source manifests using the pool tab; enable automatic sync on both ends or request it explicitly. Existing connections retain legacy rotation until changed. See [Smart pools](docs/SMART-POOLS.md) and [راهنمای فارسی استخر هوشمند](docs/SMART-POOLS.fa.md).

## Build and verification

Build on Linux/WSL with the Go version specified in [go.mod](go.mod), Node.js 24 and npm:

```sh
SPOOF_VERSION=v0.4.9 bash scripts/build.sh
sudo bash dist/install.sh
```

The build exports the frontend, embeds it in `spoof-panel`, builds `spoof-l3` and packages both with the installer and documentation. GitHub Actions validates the source before creating release packages for both architectures.

Validation includes **25 ordered carrier combinations**, **five rotating-source cases**, delayed heartbeats above three seconds RTT, and **one Iran + three simultaneous foreign peers on all five carriers** with concurrent TCP/UDP probes and isolated stop/restart. **23 browser scenarios** cover panel behavior and locally served Persian fonts; Go race tests and real HTTP/SQLite tests cover the backend. Reproduce them with [Testing](docs/TESTING.md). See [Architecture](docs/ARCHITECTURE.md), [Operations](docs/OPERATIONS.md) and the [fix report](docs/FIXES-2026-11-01.md).

## Limits and attribution

Linux/IPv4 only. No automatic carrier fallback, FEC, custom retransmission/congestion control, IPv6 or full Internet gateway wizard. Raw TCP does not perform a normal TCP handshake. Protocol 4 is not wire-compatible with standard kernel IPIP. No standardized GRE, OSPF or IPsec support is claimed.

Namespace tests establish behavior in isolated Linux networks; they do not establish provider spoof acceptance, censorship resistance or sustained production throughput. Providers may block forged sources. Test your actual VPS pair. ARM64 is cross-compiled; runtime testing on ARM64 hardware and a real VPS systemd install remain separate deployment checks. The frame design has not undergone an independent cryptographic audit.

Derived from [ParsaKSH/spoof-tunnel](https://github.com/ParsaKSH/spoof-tunnel), with upstream history retained. Licensed under [Apache-2.0](LICENSE); see [NOTICE](NOTICE). This project is not PenguTunnel and is not wire-compatible with it.

Password recovery during an update: choose **2** in the installer to generate and display a new password; **1** keeps the current password. The prompt reads `/dev/tty` even with `curl | bash`. For unattended explicit resets, pipe the installer into `SPOOF_PASSWORD_ACTION=reset bash`. Fresh-install and reset credentials appear at the end of successful installation. Plaintext passwords are not written to disk.

### v0.3.9 reliability and diagnostics

Unexpected tunnel exits trigger at most eight automatic recovery attempts with exponential backoff from 1 to 30 seconds. Thirty seconds of stable execution resets the failure streak; manual Start/Restart resets the budget. The panel shows pending recovery and budget exhaustion. Explicit Stop and deletion cancel pending recovery; the boot auto-start setting remains independent. TCP forward target sockets are bound to their TUN device, preventing fallback to another route during startup or failure.

For spoof tests, update **both panels**. Start the receiver with an empty test code to generate a fresh code, copy the displayed 32-character code into the sender, then start sending. Probes include a per-run HMAC and source-bound sequence number; only unique authenticated sequences count. Tester packets from before v0.3.7 are intentionally rejected. Use a fresh code for every test.

Authenticated out-of-window frames produce clock/delay diagnostics after authentication, without changing the system clock or widening the replay window. The dashboard separates source-filter, malformed-packet, MTU and timestamp rejections and shows the latest filtered source. Source-filter counts include unrelated links and are not path loss. Oversized IPv4 packets without DF are fragmented; DF packets elicit ICMP fragmentation-needed with the next-hop MTU. Incoming invalid packets are counted too.


The receiver acknowledges startup only after its raw socket is ready. ICMP retains its 44-byte authenticated probe payload. TCP now uses a plain 40-byte IPv4/SYN with no payload or options, correlating each source/sequence/count with a 51-bit truncated HMAC in header fields. The new receiver also accepts v0.3.7 payload probes, but an old receiver cannot read the new TCP format: update both panels. Header-rewriting middleboxes can invalidate TCP correlation. These are diagnostic probes; the tunnel still uses AEAD. Rejected candidate packets and duplicate probes are counted separately, including when no source passes. A rejected packet does not by itself prove a wrong code; unrelated or corrupt traffic from the chosen pool can contribute.

Forwarders wait up to five seconds for their assigned TUN to be up, and Stop cancels that wait. There is still no fallback route. Running tunnel edits are rejected with HTTP 409; saving a stopped/error tunnel cancels queued recovery and requires explicit Start. A mismatched PSK/allowlist normally leaves a live core without a working peer; restarting cannot repair it.

The installer is **not an automatic updater**. It downloads the latest release only when you explicitly execute it; use `SPOOF_RELEASE=v0.4.9` to pin an installation. IPv4 outer overhead is 81 bytes for ICMP/UDP, 93 for TCP and 73 for protocol 4/253: inner MTU 1280 produces outer IP packets of 1361/1373/1353 bytes respectively, excluding link-layer headers. Measure the actual path MTU. Fragment loss can discard an entire inner datagram; no FEC/retransmission is promised. See [review follow-up and compatibility](docs/REVIEW-2026-11-04.md).


### v0.3.10 connection labels

Connection cards separate the role, send carrier, receive carrier and TUN interface into labelled, wrapping chips. Mixed Persian/Latin names use bidirectional isolation. Long names and narrow screens retain readable labels without overlap. Browser regressions check dark desktop and light mobile layouts, including a 320-pixel viewport.

### Smart source pools in v0.4.0

Pasted IPv4 addresses stay in a permanent **default** group; each TXT upload becomes an independent named group. Select one, several or all groups, and set their priority. Uploading stages a file without activating it.

Named TXT files, selected-set deduplication, quality-based dispatch, healthy-source round-robin and failure-based priority switching are available for TUN links. Authenticated per-source probes include small and MTU-sized frames; reference-qualified failures cause a 600-second quarantine followed by re-verification. A reverse-path outage is not treated as evidence that every source failed. The UI shows progressive discovery, quality, quarantine countdowns and authenticated peer-manifest synchronization. Upgrade both engines before enabling smart policies; legacy tunnels keep their existing behavior. [English operation guide](docs/SMART-POOLS.md) · [راهنمای فارسی](docs/SMART-POOLS.fa.md).

### v0.4.1: atomic pool edits and in-panel updates

Clearing the manual default and selecting a TXT group now commit together, including pending policy changes. Either save control uses the same atomic transaction; a rejected request leaves the previous configuration intact. Source-quality rows explain timeouts, missing reverse-path evidence and restart quarantine. Live pool summaries are readable in Persian, and log history switches atomically to a bounded, batched live stream.

Settings now checks GitHub Releases and installs a newer stable panel/core pair with progress, actual downloaded bytes, mandatory checksums, a consistent SQLite backup and rollback on startup failure. The page reloads once after the new version responds; the header reads the new running version. **Upgrade to v0.4.1 once with the installer to install the independent update service.** Future updates can then be started from Settings. [Update operation and limits](docs/UPDATING.md).

### v0.4.9: the TLS pre-check accepts NAT servers, panel URL uses the inbound IP

The TLS pre-check no longer compares the domain against the single outbound address (what ipify sees): on one live server the egress addresses were 205.252.135.55 and 205.252.135.239 while the inbound IP was 45.142.189.164, so a correctly pointed domain was rejected as "not this server" — and the printed panel URL itself was dead. The installer now accepts the domain when it matches any of the server's addresses (interface addresses first, then the IP the panel URL advertises, then the outbound address); when it matches none, it shows every address it found and asks whether to continue anyway and let certbot decide — a truly wrong domain still fails at certbot and the panel stays on HTTP. The panel binary prints its URL from a directly assigned public interface address first instead of the egress address; on a NATed host whose inbound IP is not visible locally, set `SPOOF_PUBLIC_IP` before installing. [TLS operation](docs/TLS.md).

### v0.4.8: the connection-test apply wait scales with the pool

Applying a connection-test result no longer waits a fixed 100 seconds for the new tunnels to report an authenticated peer. The wait now scales with the peer manifest — the full source pool, not the active cohort — because a cold-started tunnel must receive the whole manifest before the peer is accepted: a 122k-source pool needs about 5 minutes for that transfer alone, and the old fixed wait rolled back a healthy pair with the manifest only ~30% received. The new wait is about 7 minutes for a 122k-source pool at 25 probes/second, 100 seconds for small pools, and at most 15 minutes. The pool itself is untouched: a large pool still works, it just takes longer to sync after every restart. The applying note on the test page says so instead of promising 100 seconds. [Connection test](docs/CONNECTION-TEST.md).

### v0.4.7: free TLS certificates for the panel, minimal connection-test page

The installer can now get a free Let's Encrypt certificate for the panel. After the password is confirmed, if no valid certificate is configured it asks whether to get one, asks for the domain, and obtains it with certbot (HTTP-01 on port 80) after pre-checking that the domain resolves to this server's public IP and that port 80 is free; it then validates the pair, restarts the panel, and verifies the live HTTPS endpoint. Any failure leaves the panel on plain HTTP. On later runs a server that already has a certificate is never asked again; a certificate expiring within 30 days is renewed, and the panel picks up renewed files without a restart. Each server needs its own domain. The Settings page shows the certificate state and expiry, and the header shows a lock pill when HTTPS is on. [TLS operation](docs/TLS.md).

The connection-test page was redesigned to be minimal: a four-step strip (setup · measuring · recommendation · applying) shows where the test is at a glance, and the long explanatory paragraphs are gone — one short line per section, the data estimate in a single line, and the recommendation card reduced to the best pair, the weaker-direction speed, the median RTT, the cap warning when it applies, and one line about what applying does. Nothing was removed functionally: the same setup, the same 25-cell matrix, the same detail view, the same preview-and-apply flow, and the same Persian error explanations with a link to the Logs console.

### v0.4.6: no speed cap on the connection test, copy works on plain HTTP, one-button apply, Logs console

The connection test no longer has any user-chosen maximum: a capped cell climbs a doubling ladder (20 → 40 → 80 → 160 → 320 → 500 Mbps) until the link saturates — a round neither direction caps, or doubling the offer buys at most 5% more delivered goodput — and the reported result is the rung with the highest weaker-direction goodput, so the maximum is measured, not assumed. The 500 Mbps ceiling is the measurement limit of the sender, not a setting. The copy buttons also work on plain HTTP now: `navigator.clipboard` only exists on HTTPS, so copying fell back to selecting the text and asking for a manual copy. A shared clipboard helper now tries the Clipboard API first and falls back to the classic `execCommand("copy")` path, which works over HTTP; this fixes the connection-test code copy, the pair JSON copy, and the tester code copy.

Apply is now one button: if no target connection was picked, the preview and the receiving side adopt the live pair with the same real endpoints and TUN subnet instead of failing with "TUN subnet overlaps" on their own subnet — a new pair is created only when no such pair exists, and several identical pairs still refuse with a clear error instead of guessing. When a preview or apply fails, the panel says why in Persian (including the conflicting tunnel name and subnet) and points at the Logs console.

The new **Logs** section in the sidebar is one console for everything: every tunnel's engine and supervisor lines, peer-loss and peer-restore moments with their diagnostics, and benchmark preview/apply failures, with level/instance/text filters, live refresh, auto-scroll and copy. The ring is in memory and starts empty on every panel start.

### v0.4.5: peer-loss watchdog restarts a stalled tunnel

When a tunnel was connected and then its peer stays lost (the panel shows «در انتظار پاسخ طرف مقابل») for 120 seconds, the panel now restarts that tunnel automatically — the same restart you would press by hand, on that server; the other side runs the same rule, so both ends restart when the stall is symmetric. The 120-second threshold is longer than the engine's own self-heal window (pool quality expires after ~48s and sources are re-discovered), so the watchdog only fires when self-healing has actually failed. It never restarts a tunnel that never connected (initial discovery, wrong PSK, peer down), never fires twice without a fresh connection, and waits at least 5 minutes between restarts. Each restart is written to the tunnel log and the connection card shows how many times the watchdog restarted the tunnel and when it last did.

### v0.4.4: signed releases, kill-proof updates, uncapped connection test

From v0.4.4, `SHA256SUMS` is signed with an offline Ed25519 key; panels on v0.4.4+ refuse a signed-version release whose signature is missing or invalid, so a compromised release cannot pass on checksums alone. An update interrupted by a killed worker or power loss is now recovered at panel startup: a surviving intent marker reconciles the binary pair (completing the update when only the core was replaced, restoring the previous pair otherwise) instead of leaving a mixed installation. Connection test no longer stops at the base rate: a capped cell is re-measured once at a configurable maximum (default 100, up to 500 Mbps) and reports the true maximum, and applying a result seeds the benchmark-proven source into both engines so the new tunnel does not verify its pool from scratch, with a 100-second authenticated-peer wait and per-side pool diagnostics on failure. [Update operation and limits](docs/UPDATING.md) · [Connection test](docs/CONNECTION-TEST.md).

## Compare carriers before applying

In **Connection test**, pair the two panels with a temporary code and compare all 25 ordered carrier combinations using fixed spoof sources, authenticated RTT and delivered upload/download goodput. A separate confirmation creates both complementary ends or safely replaces the selected existing pair, with validation and rollback. Tests have explicit rate/time limits; a cell that reaches the base rate is re-measured once at a higher maximum so the reported speed is the real maximum, and results still capped at that maximum are lower bounds. [How it works, setup and limits](docs/CONNECTION-TEST.md).

Source pools can now use a bounded set such as **100 verified sources refreshed every 30 minutes**; adaptive, round-robin and failover dispatch stay inside that set. [Source-pool details](docs/SMART-POOLS.md).
