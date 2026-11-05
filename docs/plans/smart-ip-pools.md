# Smart source pools — implementation contract

Requested: per-link bidirectional authenticated quality measurement, 600-second quarantine with retest, named TXT files with selection, adaptive/healthy round-robin/failover, authenticated peer-source synchronization, Persian performant UI.

Invariants:
- Real destination never follows spoof source. Never reply to the observed forged address.
- A probe acknowledgement binds an unpredictable token to the source actually observed by the authenticated receiver. No quality credit for an unauthenticated reply.
- A missing reply with a stale reverse/control path is UNKNOWN, not evidence to quarantine every candidate.
- Quarantine excludes data for 600 seconds; expiry makes a source eligible for a bounded retest, never automatically healthy.
- Pool state belongs to this link, direction, carrier and port tuple. Existing tunnels retain legacy round-robin until explicitly changed.
- Bound probe traffic, pending tokens, uploads, decompression, transfer assembly, IPC requests and UI responses. No per-IP goroutines or database writes in the packet path.
- File selection is independent of dispatch mode; overlapping files do not multiply address weight.
- Peer manifests are authenticated, revisioned and validated as complete snapshots before acceptance; data still requires an allowed source. Never send the PSK to the peer API or a public service.
- A large pool is measured progressively; no claim that 122,000 addresses have instantaneous quality scores.

Implementation order: scheduler/tests; engine probes/control IPC/peer sync; file APIs/storage; Persian pool UI; race/network/browser validation.

Acceptance: 600-second fake-clock quarantine and recovery; no mass quarantine on reverse outage; per-source attribution/replay; both ends and all carriers; overlapping file selection; 122k-address imports and pagination; unauthorized/corrupt peer manifests rejected; version mismatch explicit; existing regression tests and mobile/RTL checks.

## UI review

| Before | After | Why |
|---|---|---|
| One inline textarea | Permanent manual default plus independently selected TXT groups | Raw pasted addresses and named imports retain their identities |
| Whole pools in general settings | File IDs for selection; compact general editor | Large pools do not exceed request limits or load on every refresh |
| Unconditional packet rotation | Adaptive, healthy round-robin, priority failover | Exclude unverified/quarantined sources and show actual measurements |
| Manual peer-list copying | Authenticated, repairable, cancellable manifest sync | Avoid partial/incorrect allowlist application |
| Tab-unmounted draft | Persistent pool editor, global revision guard | Preserve both pool selections and general-form edits |
