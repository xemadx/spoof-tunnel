# Tester sender regression fixed in v0.3.4

The overflow fix in `IPRangeSet.IterateIPs` returned from the entire iterator at the end of its first merged interval. A list of six separate source addresses therefore sent only the first source. The worker then unconditionally reported done/100%. This regression was introduced by commit ba829dd and is present in v0.3.1 through v0.3.3.

The iterator now breaks only the inner interval loop. Callback cancellation still returns from the full iterator, and the last IPv4 address cannot wrap. Tests reproduce the exact reported six-address input, mixed ranges and cancellation. A loopback-only Linux namespace integration checks both ICMP and TCP: six disjoint source IPs, ten packets each, exactly 60 packets received with all six sources present. No user-provided public address is transmitted in this integration test.

Sender state now reports processed IPs, kernel-accepted packets and failed sends. An incomplete traversal cannot report success, and stopping a sender no longer forces progress to 100%. The UI displays these counters independently of receiver results. Kernel acceptance does not prove delivery to the remote host.
