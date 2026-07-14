# QUIC Tunnel Transport

Status: draft for review. No implementation exists yet.

## Background

All traffic between a Telepresence client and the cluster rides a data path that was
chosen for universal reachability, not throughput:

```
app socket
  → TUN device (pkg/vif, gVisor netstack terminates TCP/UDP)
  → one bidirectional gRPC Tunnel stream per flow (rpc/manager Tunnel RPC,
    muxed and managed by pkg/tunnel)
  → a single HTTP/2 connection, carried as plaintext gRPC
  → SPDY/WebSocket port-forward through the Kubernetes apiserver
    (pkg/client/portforward)
  → kubelet → traffic-manager pod → re-originated dial to the target
```

The client side establishes this in `pkg/client/k8s/connect.go` using
`grpc.WithContextDialer(portforward.Dialer(...))`. Flows destined for intercepted
containers may instead use a second instance of the same construction directly to the
traffic-agent pod (`pkg/client/agentpf`); the choice is made in
`pkg/client/rootd/stream_creator.go`.

This design has two properties worth preserving at all cost:

* **It works wherever `kubectl` works.** No LoadBalancer, no NodePort, no open UDP,
  no NetworkPolicy exceptions, no VPN. RBAC on the apiserver is the entire authn/authz
  story.
* **No TCP-over-TCP meltdown.** Because gVisor terminates TCP at the client and the
  manager re-originates it, the tunnel carries payload, not TCP segments.

And it has costs that this document proposes to address:

1. **Head-of-line blocking.** Every flow shares one TCP connection. A single lost
   packet on the path to the apiserver stalls *all* tunneled flows until
   retransmission, because HTTP/2 multiplexing cannot deliver stream B while stream A
   has a gap in the underlying byte stream.
2. **The apiserver is the data plane.** It was never designed for that. It adds two
   hops (apiserver, kubelet), it is rate-limited and connection-capped by cluster
   operators, and a busy apiserver degrades tunnel throughput and latency for reasons
   entirely unrelated to Telepresence.
3. **Fragile long-lived connections.** Port-forwards drop on apiserver restarts,
   idle timeouts, and network transitions (laptop switches from Wi-Fi to wired, VPN
   reconnects). Reconnection is slow and visible to the user.
4. **UDP is emulated over a reliable stream.** Latency-sensitive UDP (HTTP/3, DNS
   under load, media protocols) inherits TCP's delivery semantics.

Note on encryption: the gRPC itself is plaintext; confidentiality comes from the TLS
on the apiserver connection. So today there is exactly one layer of transport
encryption, and it is *a property of routing through the apiserver*. Any transport
that bypasses the apiserver must bring its own encryption and its own trust bootstrap.

## Rejected alternative: adopt Tailscale

Tailscale (or an embedded `tsnet`) looks attractive because it solves NAT traversal
and encryption in one package, but it is a poor fit as a replacement transport:

* **It needs a UDP path or a relay.** Into a typical managed cluster there is no
  inbound UDP at all. Tailscale's fallback is DERP relays, which means customer
  traffic transiting third-party servers — unacceptable for a large share of users —
  and self-hosting relays is exactly the infrastructure burden the port-forward
  design avoids.
* **Control-plane dependency.** Coordination-server accounts (or operating Headscale
  from the Helm chart) per developer per cluster is a large onboarding regression
  versus "you already have a kubeconfig", and it replaces Kubernetes RBAC as the
  trust root.
* **Scope.** Telepresence needs a point-to-point tunnel between one client and one
  cluster, not a mesh.

What *is* worth stealing from Tailscale is its architecture: a universally-working
relay path plus opportunistic upgrade to a direct encrypted UDP path. Telepresence
already has the relay path — the apiserver port-forward. This design adds the
upgrade.

## Proposal

Add QUIC as an alternative transport for the tunnel, used opportunistically when the
cluster operator has exposed a UDP path to the traffic-manager, with the existing
port-forwarded gRPC transport remaining the default and the always-available
fallback.

QUIC rather than direct (m)TLS gRPC or WireGuard because:

* **Per-stream independence.** QUIC streams are independently retransmitted; loss on
  one flow no longer stalls the others. This directly fixes cost #1 and maps
  one-to-one onto the existing flow-per-stream model in `pkg/tunnel`.
* **Datagram frames (RFC 9221)** give real unreliable delivery for tunneled UDP,
  fixing cost #4.
* **Connection migration.** A QUIC connection survives the client changing IP
  address; laptop roaming stops killing the tunnel (cost #3). 0-RTT resumption makes
  the remaining reconnects cheap.
* **Mandatory TLS 1.3.** Encryption is part of the handshake, not a bolt-on. A
  direct gRPC transport would need the same certificate/identity work *plus* separate
  TLS configuration; WireGuard would need key distribution plus a userspace stack,
  and still lacks stream multiplexing (we would be back to designing our own muxing
  on top of it).

### Trust bootstrap

The direct path must not weaken the "kubeconfig is the credential" model. The
port-forwarded gRPC connection — which the client always establishes first, and which
is authenticated by Kubernetes RBAC — doubles as the trust channel:

1. The traffic-manager generates (or is given via Helm) a CA and a server certificate
   for its QUIC endpoint. Rotation is the manager's problem; nothing is stored on the
   client.
2. Over the existing port-forwarded connection, the client requests the QUIC endpoint
   descriptor: address(es), port, CA bundle, and a short-lived client token or client
   certificate minted for the session.
3. The client dials QUIC, verifying the server against exactly that CA (no system
   roots), and presents the session credential. The manager binds the QUIC connection
   to the already-established session.

An attacker who can reach the UDP port but has no RBAC access gets a TLS handshake
they cannot complete. An operator who never exposes the port gets today's behavior,
unchanged.

### Reachability and fallback

* The Helm chart gains an opt-in QUIC endpoint: a UDP `containerPort` on the manager
  plus a Service of the operator's choosing (LoadBalancer, NodePort, or an existing
  Gateway). Default: disabled.
* The client probes the advertised endpoint concurrently with normal startup. If the
  handshake succeeds within a short budget, new tunnel streams use QUIC; otherwise
  everything stays on the port-forwarded path. The result is cached per connect but
  re-probed on migration failure.
* Fallback must be per-connection and silent. Mid-session loss of the QUIC path
  (e.g. a firewall that starts dropping UDP after idle) moves *new* streams back to
  gRPC; established QUIC streams are torn down like any dropped tunnel stream and
  redialed by the application.
* `telepresence status` reports which transport is active so support conversations
  don't have to guess.

### Where it plugs in

The abstraction seam already exists and should not move:

* `pkg/tunnel/provider.go` — `Provider`/`StreamProvider` is how stream consumers
  obtain a tunnel stream. A `quicProvider` implements the same interface by opening a
  QUIC stream and framing `TunnelMessage`s on it (length-prefixed protobuf, same
  messages, same `pkg/tunnel` stream protocol version negotiation).
* `pkg/tunnel/stream.go` — `GRPCStream` is just `Recv`/`Send` of `TunnelMessage`;
  the stream state machine (idle timers, closeSend/Disconnect handshake) is
  transport-agnostic and is reused as-is. Keeping the message framing identical means
  the manager-side `state.Tunnel` handling and the agent forwarding paths do not care
  which transport delivered the stream.
* `pkg/client/rootd/stream_creator.go` — the decision point that today picks
  manager vs. agent provider additionally picks QUIC vs. gRPC per the probe result.
* Manager side: a QUIC listener (`quic-go`) that accepts streams, decodes the same
  `TunnelMessage` framing, and feeds them into the existing `state.Tunnel` entry
  point (`cmd/traffic/cmd/manager/service.go`).

Client-to-agent tunnels (`pkg/client/agentpf`) stayed on port-forward in the first
iteration; the sections below remove that limitation by routing all QUIC — manager-
and agent-bound alike — through a single stateless packet forwarder. *Externally*
exposing anything per agent pod remains explicitly out of scope; agents listen only
on their pod IPs and are reachable solely through the forwarder. The
manager-terminated endpoint implemented in the first iteration becomes an internal
backend behind that forwarder: its listener, CA, certificates, framing, fallback,
and observability all carry over — the Service that used to expose the manager
directly is re-pointed at the forwarder.

### The forwarder

The forwarder is the architecture's single exposed component and its foundation:
a stateless QUIC *packet* router that owns the one UDP entry point into the
cluster. Every QUIC connection — client⇄manager and client⇄agent alike — passes
through it, encrypted end to end. The forwarder never terminates TLS, holds no
keys, and sees no plaintext; the manager and the agents run QUIC listeners on
their pod IPs, reachable only through it.

Routing works in two tiers, following the QUIC-LB pattern
(draft-ietf-quic-load-balancers):

* A connection's first packet (the client Initial) is routed by the **SNI** in its
  ClientHello, which is readable without terminating TLS. SNI names identify the
  backend: `manager.<install-id>` for the traffic-manager, `<pod-uid>.<install-id>`
  for an agent.
* Every subsequent packet is routed by the **server-issued connection ID**:
  backends mint connection IDs that encode their own pod IP (quic-go supports
  custom connection-ID generators; an IPv6 address plus a version/length octet
  fits inside the 20-byte CID limit). A forwarder can therefore route any
  mid-connection packet with no flow table at all.

Statelessness is what makes the forwarder an acceptable hard dependency: a restart
loses nothing that matters, replicas need no coordination, and it can run as a small
Deployment (default) or DaemonSet. It ships as another command in the tel2 image.

One qualification to "stateless": a ClientHello can span multiple Initial packets
(post-quantum hybrid key shares push it past one packet's CRYPTO capacity, and Go's
TLS stack sends them by default), and continuation fragments carry no SNI. The
forwarder therefore keeps a small, ephemeral **handshake cache** — original client
DCID + source address → backend, seconds-scale TTL, consulted only for long-header
packets. Established connections never touch it (server-issued CIDs route those), so
a forwarder restart costs only the handshakes in flight at that moment.

Anything that resolves to no backend — non-QUIC junk on the port, QUIC versions the
forwarder cannot read Initial keys for, SNI-less ClientHellos from foreign clients,
malformed packets, undecodable CIDs — is **dropped silently** (rate-limited metric,
no response). Nothing is ever defaulted to a backend: routing unauthenticated
traffic inward would only relocate the DoS surface, and the Telepresence client
always sends SNI in a known version.

**Backend allowlist (required).** A CID-routing forwarder would otherwise be an
open UDP redirector to any pod IP an attacker encodes into a forged CID. The
forwarder must validate every routing decision — SNI resolution and decoded CIDs —
against the set of live manager and agent pod IPs, maintained by watching pods with
the corresponding labels (a small RBAC grant for its own ServiceAccount). Packets
that resolve outside the allowlist are dropped.

**Failure modes.** The forwarder dying kills every QUIC connection at once —
immediately and unambiguously (connection error, never a hang) — and every consumer
falls back to its port-forward path, which does not involve the forwarder. QUIC is
retried on the next connect. The manager dying no longer affects the client⇄agent
*data path*: the forwarder routes packets and the agents terminate their own TLS, so
cluster-originated traffic to an intercepted workload keeps tunneling to the laptop
handler through a manager outage. This is the decisive advantage over relaying agent
traffic through the manager, and the reason the forwarder is a requirement rather
than an optimization.

The scope of that guarantee is worth stating precisely, because it is narrower than
"everything keeps working". What survives a manager outage is the agent attachment
data path — a request that *originates in the cluster*, hits the intercepted pod, and
is tunneled agent → forwarder → laptop. What does *not* survive is traffic the
developer originates from the laptop through the VPN (`curl some-cluster-service`):
that path needs cluster DNS resolution and subnet routing, both of which run over the
manager-bound tunnel, so it is down for the duration of the outage like everything
else VPN-borne. Verified manually on 2026-07-13: with the traffic-manager scaled to
zero, an in-cluster `curl` of the intercepted service still reached the local
handler, while a laptop-side `curl` through the VPN did not — exactly as this scoping
predicts.

### Agent connections over QUIC

The client's connection to a traffic-agent — sidecar or node-agent alike — is a gRPC
connection to the agent's API port, carried today over its own Kubernetes
port-forward per agent pod. Everything an attachment needs (the `WatchDial` reverse
dials, the agent `Tunnel` streams, environment and mount negotiation) flows over
that one connection, so moving *it* moves the entire attachment.

* The agent runs a QUIC listener on its pod IP (no exposure; reachable only via
  the forwarder). On arrival — and again whenever its manager connection is
  re-established, since a manager restart mints a new CA — it requests a server
  certificate for its SNI name over its existing, authenticated manager session.
  It accepts any client certificate chaining to the CA; all such certificates are
  short-lived and session-scoped by construction.
* `agentpf` swaps the transport under the agent gRPC connection: instead of a
  Kubernetes port-forward, a `grpc.WithContextDialer` that dials the forwarder
  with the agent's SNI name. One QUIC connection per agent (TLS terminates at the
  agent, so connections cannot be shared across agents), all sharing the client's
  UDP socket. The agent's SNI name travels in the `AgentPodInfo` the client
  already watches.
* Fallback: if the QUIC connection to an agent dies, `agentpf`'s existing
  reconnect logic dials the Kubernetes port-forward instead. The port-forward
  machinery is only ever bypassed, never disabled; it remains the reconnect
  target for the remainder of the session. Caller cancellation is, as always, not
  a transport failure.

Path comparison: the port-forward is client → apiserver → kubelet → agent, one TCP
connection per agent pod, each subject to HoL blocking and apiserver throughput
limits. The forwarded path is client → forwarder → agent, where the middle hop is
stateless packet forwarding: per-stream independence end to end, no apiserver, no
TLS re-termination, and no session state anywhere in the path.

**Manager CA rotation.** The CA that signs agent (and manager) certificates is
ephemeral — a manager restart mints a new one, which implicitly revokes every
certificate the previous manager signed. A surviving agent re-fetches its server
certificate over its (re-established) manager session, and a client re-fetches the CA
bundle and its own client certificate when it reconnects, so both ends converge on
the new CA within seconds of the manager coming back. During that window an agent may
still be presenting a certificate from the old CA; a client that dials it then fails
verification and falls back to the port-forward. That fallback is currently *sticky*
per agent — the client stays on the port-forward for that peer until the agent
connection is torn down and re-established, rather than re-probing QUIC once the agent
has re-fetched. Correct (the fallback works) but suboptimal after any manager rollout;
listed under Hardening. This is observable, and asserted, by the manager-outage
integration test's recovery phase, which re-establishes the attachment and waits for
the agent transport to return to QUIC.

### Zero-configuration endpoint discovery

`quicTunnel.enabled=true` should be sufficient for the common case. It deploys the
forwarder and its Service (`LoadBalancer` by default) and enables the QUIC
listeners in the manager and the agents. In-cluster ports stay chart defaults —
they are pod-internal and no admin has a reason to care about them. The externally
reachable address is discovered rather than configured:

* The manager watches the forwarder's Service (RBAC already grants get/list/watch
  on services, and on nodes in cluster-scoped installs).
* `LoadBalancer`: advertise `status.loadBalancer.ingress[].ip|hostname` with the
  Service port, once assigned. Until assignment the endpoint is simply not
  advertised; clients pick it up on a later connect.
* `NodePort`: advertise the assigned `nodePort` with node addresses, preferring
  `ExternalIP` over `InternalIP`.
* The endpoint descriptor carries an ordered list of candidate addresses rather
  than a single host, plus the SNI scheme. The client dials candidates
  concurrently within the probe budget and keeps the first whose handshake
  completes. An unreachable candidate is harmless — that is the silent-fallback
  property doing its job — so discovery can guess generously. (The descriptor RPC
  is unreleased; reshaping it is not a compatibility event.)
* `quicTunnel.externalHost`/`externalPort` remain as overrides that replace
  discovery entirely, for topologies the manager cannot see (NAT in front of the
  LoadBalancer, port remapping, DNS names that only resolve on the developer VPN).
* Namespace-scoped installs may lack node read access; NodePort discovery then
  degrades to requiring the explicit override, which the reference documentation
  must state.

### Non-goals

* Replacing the port-forwarded gRPC transport. It remains the default and the only
  path that requires zero cluster configuration.
* NAT hole-punching (ICE/STUN between client and node, with the port-forward as
  signaling channel). It is the natural *next* step if evidence shows a significant
  population that cannot open UDP ingress but could hole-punch, and nothing in this
  design precludes it — the trust bootstrap and the QUIC transport are exactly the
  pieces it would reuse. Not now.
* Embedded Tailscale/`tsnet` support for shops that already run a tailnet. If asked
  for, it is a reachability variant (the manager becomes dialable over the tailnet),
  not a transport variant, and is orthogonal to this work.
* Multiplexing changes inside `pkg/tunnel`. The flow-per-stream model is kept.

### Also considered: direct gRPC to an exposed manager port

Cheaper than QUIC (same protocol end to end, only the dialer changes) and it removes
the apiserver from the path, but it keeps head-of-line blocking, keeps UDP-over-stream
semantics, does not survive address migration, and needs the same certificate
bootstrap work anyway since the plaintext gRPC cannot be exposed as-is. Given that
the identity work is the expensive common part, spending it on the transport that
also fixes HoL blocking, UDP fidelity, and roaming is the better trade. If the
bootstrap machinery lands first, a direct-TLS-gRPC mode falls out of it almost for
free and could ship as an intermediate step.

## Implementation phases

1. **Trust bootstrap.** Manager-side CA/cert handling, session-scoped client
   credentials, RPC on the existing manager connection to fetch the endpoint
   descriptor. Useful on its own (enables direct-gRPC mode).
2. **Transport.** `TunnelMessage` framing over QUIC streams; `quicProvider` on the
   client; QUIC listener on the manager feeding `state.Tunnel`; datagram support for
   UDP flows can come later — streams-only is already a strict improvement.
3. **Reachability.** Helm chart opt-in (UDP port + Service), client probe,
   per-stream fallback, `telepresence status` reporting.
4. **Hardening.** Migration testing (address change mid-session), idle-timeout
   tuning against real-world middleboxes, integration tests that run the suite over
   both transports. Includes recovering QUIC after a manager CA rotation: a
   session whose QUIC path trips to the port-forward fallback re-probes on an
   interval rather than sticking there until reconnect (`quicReprobeLoop`,
   `pkg/client/rootd/quic.go` -- see "Relay hardening" below).

Phases 1–4 are implemented (manager-terminated endpoint, exposed directly). The
forwarder-first architecture builds on them:

5. **The forwarder.** Stateless SNI + connection-ID packet routing, the pod
   allowlist watch, a new command in the tel2 image, Deployment + the single
   exposed Service. The manager's listener moves behind it: pod-IP listening,
   CID generator encoding the pod IP, SNI-named server certificate; the chart's
   QUIC Service targets the forwarder instead of the manager. Client changes are
   minimal (SNI on dial). Everything from phases 1–4 — trust bootstrap, framing,
   fallback, status/usage reporting, integration suites — carries over and must
   stay green throughout.
6. **Agents behind the forwarder.** Agent-side QUIC listener with certificate
   fetch over the agent's manager session (re-fetch on manager reconnect), SNI
   names in `AgentPodInfo`, the `agentpf` QUIC dialer with port-forward fallback
   on reconnect, injector/node-agent plumbing. Integration coverage must assert
   that attachments actually ride QUIC, that a forwarder restart mid-session
   degrades to port-forwards and recovers on reconnect, and that a manager
   restart leaves client⇄agent QUIC traffic flowing.
7. **Zero-configuration discovery.** Service/node watch in the manager, candidate
   address list in the endpoint descriptor, concurrent client probe, docs reduced
   to "set `quicTunnel.enabled=true`". Implemented: `quictunnel.Discovery` watches
   the forwarder's Service (raw Get+Watch, because the shared Services informer's
   transform strips exactly the LoadBalancer status discovery needs) and, given
   Node RBAC, the Nodes; `GetQuicTunnelEndpoint` advertises the ordered candidate
   list (`QuicTunnelEndpoint.candidates`, with `host`/`port` duplicating the first
   for older clients); the client races candidates with a 250 ms stagger and agent
   connections reuse the winning address. The SNI scheme mentioned in early drafts
   was already carried by phases 5–6 and needed nothing here.

## What measurement taught us, and the improvements it motivates

Phases 1–6 were followed by a measurement campaign (the `perf/` harness, plus
microbenchmarks in `cmd/traffic/cmd/quicforwarder`) that falsified parts of the
original performance narrative, confirmed others, and surfaced improvements that
were not visible on paper. The facts first, because the improvements only make
sense against them:

* **Head-of-line blocking is real and QUIC eliminates it — for the flows, not the
  bytes.** With loss injected on the download data path and the offered load kept
  well below the connection's loss-limited capacity, the gRPC transport's p95
  request latency is 1.6–1.8× QUIC's at 1–3 % loss, and — the cleaner signal — the
  QUIC *median* stays at the clean-network baseline through 3 % loss while the gRPC
  median degrades. The confirmation required three experiment designs: bulk
  transfers and saturating request rates both degenerate into measuring
  congestion-control efficiency (where kernel TCP beats a userspace stack), because
  a single congestion-controlled connection under random loss is Mathis-bound
  (`~MSS/RTT × 1.22/√loss`) no matter how clever its streams are. QUIC's stream
  independence protects *innocent* flows from each other's losses; it does not make
  the punctured flow faster, and it does not raise the connection's aggregate
  capacity.
* **Raw throughput is not the story.** Over a real WAN the port-forwarded transport
  moves bulk data ~25 % faster than the QUIC path, because kernel TCP + TSO beats a
  userspace UDP stack whose socket buffers are capped by the node's
  `net.core.rmem_max` (208 KiB on GKE's Container-Optimized OS — silent
  burst-overflow loss that pins the congestion window). We raise every buffer we
  control and document the node sysctl, but on managed node images this ceiling is
  a fact of life. Any pitch of this transport as "faster downloads" would be
  dishonest; the honest pitch is tail latency and flow isolation.
* **An unhypothesized clean-network win: idle restart.** With sparse, pause-heavy
  traffic at WAN RTT, the gRPC transport's clean-network median is 2×RTT versus
  QUIC's 1×RTT. Linux collapses a TCP connection's congestion window after an idle
  period (`tcp_slow_start_after_idle`, RFC 2861, default-on, and it is the
  *cluster-side* kernel that matters — not tunable on managed platforms), so the
  first burst after every pause pays an extra round trip on the shared TCP
  connection. quic-go performs no such collapse. Interactive development traffic is
  almost entirely pauses, so this is arguably the most user-visible benefit
  measured so far: after every pause, the first interaction over QUIC is one RTT
  faster.
* **Two latent defects were flushed out by measuring**, both worth remembering as
  design constraints: a QUIC stream only returns its stream-limit credit when both
  directions terminate, so every layer that adapts `quic.Stream` to another
  interface must cancel the receive direction explicitly (the tunnel protocol ends
  conversations at the message level and never reads to transport EOF); and a
  packet relay must raise its UDP socket buffers or it converts scheduling hiccups
  into congestion signals.

The improvements, in the order they are worth doing (each has a detailed,
self-contained handoff plan in this directory — see `README.md` for the index
and the shared context those plans assume):

### Unreliable datagrams for tunneled UDP (RFC 9221)

The design's cost #4 — UDP-over-reliable-stream semantics — is still unpaid: today
a UDP flow rides an ordered QUIC stream exactly as it rides an ordered HTTP/2
stream on the fallback transport. For sparse request/response UDP (DNS, the bulk of
real tunneled UDP) this is fine and even beneficial: the transport retransmits a
lost query in ~1 RTT instead of the resolver waiting out its multi-second timeout.
But for sustained or latency-sensitive UDP the damage is structural, and it is
worst precisely when the inner protocol is itself QUIC (HTTP/3 through the VIF):

* one lost carrier packet stalls **every** datagram of the flow behind it, so all
  the inner connection's streams stall together — the tunnel silently re-imposes
  the head-of-line blocking the application adopted QUIC to escape;
* the outer transport retransmits datagrams the inner protocol has already
  re-sent — duplicate data and polluted inner RTT estimates;
* the inner congestion controller never sees loss, only delay, then bursts of
  drops at queue boundaries when the tunnel stream backpressures into netstack —
  the worst possible signal for a loss-based controller.

Browsers mostly *mask* this by racing HTTP/3 against HTTP/2 and quietly falling
back, so the symptom in the field is "H3 never gets used through Telepresence"
rather than visible slowness.

The fix is the hybrid carriage the RFC was written for, and it is only possible on
the QUIC transport — HTTP/2 structurally cannot offer unreliable delivery, so this
is an upgrade the QUIC path earns over the fallback rather than a parity feature:

* `EnableDatagrams` on all three QUIC endpoints (client dial, manager listener,
  agent listener). The forwarder needs nothing: DATAGRAM frames live inside
  ordinary QUIC packets, and the forwarder routes packets by connection ID without
  looking deeper.
* A tunnel-level datagram framing of `ConnID + payload`, associating each datagram
  with its flow the way the stream's `streamInfo` does today. Flow setup, teardown,
  and anything with delivery semantics (`Disconnect`, `KeepAlive`) stay on the
  flow's stream; only `Normal` UDP payload messages are eligible.
* **Size-based hybrid, not all-or-nothing:** a UDP payload that fits the outer
  datagram budget (path MTU minus QUIC overhead minus the ConnID header) is sent
  unreliably; an oversized one falls back to the flow's stream. The VIF's MTU is
  under our control, so the common case fits by construction; IP-fragmented jumbo
  datagrams reassembled by netstack take the reliable path rather than forcing a
  fragmentation scheme of our own.
* The gRPC fallback transport keeps stream carriage unchanged, and a peer that
  did not negotiate datagram support (older manager/agent) simply never receives
  the datagram framing — the stream path remains complete on its own.

This should be validated by its own experiment: an inner-QUIC-through-the-tunnel
benchmark (HTTP/3 client against an in-cluster server), designed load-first with
the lessons below.

**Implemented for the client↔manager path only** (`pkg/tunnel/datagram.go`,
`pkg/client/rootd/quic.go`, `cmd/traffic/cmd/manager/quictunnel/listener.go`). The
agent path (`cmd/traffic/cmd/agent/quicserver`) was out of scope and remains
unimplemented, because it isn't the same kind of connection: the agent's QUIC
listener serves the agent's whole gRPC server over QUIC streams (each stream is one
gRPC call, framed by gRPC itself), whereas the client↔manager path is
`pkg/tunnel`'s own TunnelMessage framing directly on a QUIC stream, one stream per
flow. A tunneled UDP payload bound for an agent is therefore never a bare
`pkg/tunnel` Normal message sitting on its own stream the way it is here — it is
already wrapped inside a gRPC `Tunnel` streaming call's own framing, itself inside a
QUIC stream. Datagram carriage for that path would need:

* A dispatch registry on the agent side keyed by ConnID, analogous to this change's
  `datagramConn`/`AttachDatagramRoute`, but reachable from *inside* the agent's gRPC
  `Tunnel` handler rather than from a listener that owns the raw stream directly.
* The agent's `quicserver` listener to enable `EnableDatagrams` on its `quic.Config`
  and expose the accepted `*quic.Conn` to that handler (today the gRPC server is
  handed a `net.Listener` adapter over the QUIC stream and never sees the
  connection itself).
* `pkg/client/agentpf`'s dialer to do the same on the client's side of that
  connection, and to carry a decoded ConnID similarly into its own gRPC-based
  stream plumbing (it wraps a QUIC stream as a `net.Conn` for a real gRPC client,
  not `pkg/tunnel`'s framing, so it cannot reuse `AttachDatagramRoute` or
  `DatagramCapable` as they stand).

None of this is free, and the client↔manager path is where the design's own
measurements (see "What measurement taught us") found the head-of-line cost that
motivates datagrams in the first place — a personal intercept's traffic to an
agent is comparatively low-volume and short-lived. Building it should wait for a
concrete case that needs it.

### ~~Pipelined stream setup: remove a round trip from every new flow~~ — decided: no measurable win

`NewClientStream` sends `streamInfo` and then **blocks waiting for** `streamOK`
before the dial message goes out, so every tunneled connection pays two tunnel
round trips before the peer even starts dialing the destination: one of ours, then
the semantically unavoidable `DialOK` (the remote connect). The `streamInfo` wait
exists to learn the peer stream version before committing to framing — a concern
that is fixed per session, not per stream, since every tunnel stream of a session
terminates in the same manager (or agent) process.

The improvement: resolve the peer version once per session (or optimistically
assume the current version and let the first `streamOK` correct it), send
`streamInfo` and the dial payload in the same flight, and treat a version mismatch
or rejection as a stream reset — exactly how the listener already rejects
handshake failures. On QUIC, opening a stream on an established connection is
free, so flow setup drops from 2×RTT + connect to 1×RTT + connect. At an 80 ms
WAN RTT that halves the time-to-first-byte of every short-lived connection, and a
development workload is dominated by short-lived connections. The same
optimization helps the gRPC transport equally — it is a tunnel-protocol
improvement that the QUIC measurements happened to expose.

**Verified and declined: the wait already overlaps the dial.**
`NewServerStream` (`pkg/tunnel/server_stream.go`) parses `streamInfo` — which
already carries the full `ConnID`, session ID and dial timeout — and only then
sends `streamOK`; its caller (`state.Tunnel` → `clientTunnel` → `dialer.Start`
for the manager, and the equivalent path in the agent and the QUIC listener)
starts the destination dial immediately after `NewServerStream` returns, a few
microseconds after `streamOK` is queued for transmission and never gated on
anything the client sends after `streamInfo`. `streamOK`'s only payload is
`peerVersion`, which today has no production consumer (`grep`-verified: every
`PeerVersion()` caller in the tree is a test assertion), so nothing
version-dependent is blocked on it either. The client's blocking wait therefore
overlaps the server's dial in wall-clock time instead of serializing before it,
and the transport's receive buffer already holds `DialOK` by the time the
client asks for it, regardless of when the application code issues that
`Receive` — so the second blocking read returns almost instantly once
`streamOK` has arrived.

Measured on the kind `dev` cluster (`PERF_IMPAIR_NODE=dev-control-plane`, 0 %
loss, one-way egress delay) with a fresh-TCP-connection TTFB probe (1-byte
ranged GET, fresh client per request, 30 samples) through an established
tunnel connection:

| Delay | Transport | p50 TTFB |
|---|---|---|
| none | QUIC | 1.9 ms |
| 80 ms | QUIC | 162.6 ms |
| 80 ms | gRPC | 163.0 ms |

Both arms land at baseline + **2×** the emulated delay (160 ms), not 3× (~240 ms,
which a genuine extra tunnel-setup round trip would add): one delayed leg is the
tunnel handshake (already at the 1×RTT + dial floor), the other is the HTTP
response itself returning over the same delayed leg — an irreducible cost
pipelining cannot touch. Removing the client's wait would not move `DialOK`'s
wall-clock arrival time earlier on either transport. No code change made.

### Validate and advertise connection migration

Migration is the design's cost #3 and remains untested (the planned experiment 3).
Two things make it worth pulling forward. First, the measured latency story
(head-of-line plus idle-restart) is about *comfort*, while migration is about not
losing the session at all — a categorically stronger user experience claim.
Second, the forwarder architecture is migration-proof **by construction**: routing
is keyed on server-issued connection IDs that encode the backend pod, never on the
client's 4-tuple, so a client that hops from Wi-Fi to a hotspot keeps every tunnel
stream alive through the same forwarder without any state reconciliation. That
synergy should be demonstrated (roam the client mid-transfer in an integration
test, assert no stream resets) and then documented, because it differentiates this
design from both the port-forward (dies with the TCP connection) and from
NAT-rebinding-hostile alternatives.

**Measured and decided.** The forwarder-construction claim holds: a pure-Go
NAT-rebind test in `cmd/traffic/cmd/quicforwarder` (no netns, no root — a `natProxy`
swaps its forwarder-facing socket mid-transfer, exactly like a NAT re-mapping or an
interface roam that leaves the client's own local socket usable) proves an 8 MiB
transfer survives a mid-transfer rebind, survives two rebinds in one transfer, and
survives a rebind followed by idling past the keep-alive interval on the new path
alone — all via the same cold-path CID routing (`Router.Route`'s existing case (b),
`routeByCID`) that RFC 9221 datagrams already rely on for their own short-header
packets; migration needed no forwarder change because that path already existed
and is exercised identically regardless of frame content.

One case does not migrate, and is now precisely characterized rather than assumed:
a rebind that straddles a single connection attempt's own Initial-packet retry (the
handshake cache is keyed on source address, since no connection ID exists yet)
leaves two independently-negotiated backend connections alive for what the client
considers one `Dial()`; the client cannot reconcile them and the attempt fails
cleanly. This is narrower than "any rebind during the handshake fails" — a rebind
whose retry lands cleanly under one new source recovers the same way an
established flow does.

The client-side risk the plan flagged — rootd tearing the session down before
migration gets a chance — does not apply, but not because it was guarded against:
`pkg/client/rootd` has no network- or interface-change monitoring of any kind (no
netlink route subscription, no OS-level path-change API, no periodic route/gateway
polling). Every recovery path in rootd (`watchClusterInfo`'s `WatchWithRetry`,
`quicReprobeLoop`, `agentpf.Clients`' per-agent dead latch) is reactive to an
already-observed failure on a specific connection, never triggered by detecting a
network change as such. That means a NAT re-mapping is invisible to rootd end to
end (nothing reacts, which is correct — the existing connection just keeps
working), but it also means a genuine change to the client's own local address is
not actively migrated: quic-go's client-side path addition (`Conn.AddPath`) requires
the application to detect the change and call it, and nothing in
`pkg/client/rootd/quic.go` does. A true interface roam therefore degrades like any
other QUIC path failure — idle timeout, fallback to port-forwarded gRPC, background
re-probe onto a brand-new connection with in-flight streams lost — rather than
being torn down aggressively. Building active client-side path migration (OS-level
network-change detection plus `AddPath`) is real, non-trivial new functionality and
is left as a follow-up; it was out of scope for proving the forwarder's own
construction claim.

### 0-RTT session resumption

Reconnects — daemon restart, `telepresence quit`/`connect`, fallback recovery —
currently pay a full TLS handshake. quic-go supports session resumption; enabling
ticket-based resumption cuts a round trip from reconnect, and the certificates
involved are session-scoped and short-lived, which bounds the replay surface.
Plain resumption (without 0-RTT early data) is the right first step: it keeps the
anti-replay analysis trivial and still removes the expensive part. Low effort, low
risk, small but universal win.

**Implemented, plain resumption only.** `Allow0RTT` is unset (false) everywhere —
grep-clean across the repo. A `tls.ClientSessionCache` is attached to every QUIC
client dial, one instance owned by the object that is actually created once per
connector session in each package (`pkg/client/rootd`'s `session`;
`pkg/client/agentpf`'s `clients`, via its `quicEndpointCache.cache` field) and
never reset alongside the CA/cert re-fetch a manager restart triggers, so a stale
ticket from before the restart simply fails to resume and falls back to a full
handshake against the fresh CA. `quic.Conn.ConnectionState().TLS.DidResume` is
logged at debug after every client dial and folded into the `session.transport`
usage report as a `resumed` entry (present, and only ever `"true"`, when it
happens).

The plan flagged one bug candidate before implementation: the agent's QUIC
listener (`cmd/traffic/cmd/agent/quicserver`) returns a `*tls.Config` built fresh
on every handshake from its `GetConfigForClient` callback, and TLS session-ticket
keys are normally lazily generated and cached *on* a `*tls.Config` — so the
worry was that a fresh Config per handshake would mean fresh, unrelated keys
every time and resumption could never succeed. Investigation (reading
`crypto/tls`'s `handshake_server.go`/`handshake_server_tls13.go`, then
confirming empirically) found this does not apply: the auto-rotated keys are
derived from the *base* Config passed to `quic.Transport.Listen` — the one
holding `GetConfigForClient`, which is constructed once and lives for the
listener's lifetime — never from whatever `GetConfigForClient` returns, so a
fresh return value per handshake does not fragment ticket-key state. Separately,
and independently of ticket keys, a resumed ticket's embedded client certificate
is re-verified against the Config's *current* `ClientCAs` on every resumption
attempt, which is what makes a `SetMaterial` swap (a manager restart handing the
agent a new signing CA) correctly defeat resumption of a ticket minted under the
old CA. No code fix was needed; `getConfigForClient`'s doc comment now records
this so it isn't re-litigated, and both properties (resumption succeeds across
ordinary reconnects, and fails across a Material swap) are covered by tests
against the real listener.

Cross-*session* resumption (a ticket reused under a new telepresence session)
was already handled: the ticket carries the original session's client identity
(cert CN = session ID) forward into the resumed connection, and the manager's
existing per-stream `SessionID == cert CN` check
(`quictunnel/listener.go`'s `handleStream`) rejects it — proven by a test that
resumes across two dials sharing one cache and declares a different session ID
on the second connection's stream.

### Relay hardening

Implemented:

* **QUIC re-probe.** A session whose QUIC path trips to the port-forward
  fallback no longer sticks there until reconnect: `quicReprobeLoop`
  (`pkg/client/rootd/quic.go`) retries the dial every `quicReprobeInterval`
  (60s) after a trip, re-fetching the endpoint descriptor -- fresh CA bundle and
  session-scoped client certificate -- on every attempt, so a manager restart's
  CA rotation is picked up without the client ever reconnecting. A successful
  retry swaps in a fresh `quicFallbackProvider` and resets the agentpf QUIC
  endpoint cache and every agent's dead latch (`agentpf.Clients.ResetQuicEndpoint`),
  so agent connections recover too. In-flight streams are never migrated, only
  new ones see the recovered path.
* **Receive-side GRO** on the forwarder (`enableGRO`, `splitGRO` in
  `cmd/traffic/cmd/quicforwarder/gso_linux.go`), the read-side mirror of the
  existing GSO write path and gated by the same `QUIC_GO_DISABLE_GSO` escape
  hatch. Measured on `BenchmarkThroughputQuicForwarded` (idle machine, `go test
  -bench BenchmarkThroughputQuicForwarded -benchtime 3x`): roughly 552-559 MB/s
  before, 645-692 MB/s after -- a consistent, if noisy, ~20% improvement.
* **Buffer/offload observability.** The forwarder already logged its granted
  socket buffer sizes once at startup; the periodic `metrics.LogSnapshot` line
  now carries the same buffer sizes plus GRO/GSO enablement alongside the
  forwarded/dropped counters, so a long-lived log answers "why is throughput
  capped" without scrolling back to the startup line.

### Measurement methodology, distilled

Recorded here because the next experiment will otherwise re-learn them at the same
cost (each of these invalidated at least one full run):

1. Loss must be injected on the **data path** (inside the kind node, on its egress
   toward the client); client-egress loss only touches ACKs and requests and
   cannot produce head-of-line blocking on responses.
2. Requests must fit in a congestion window, **and** the offered load must stay
   well below the Mathis capacity at the top loss level *at the tested RTT* —
   capacity shrinks as 1/RTT, so think time must scale with emulated delay or the
   experiment silently degenerates into the queue-bound regime.
3. Everything that segments must be neutralized at the impairment point: TSO/GSO
   off on the impaired interface, `QUIC_GO_DISABLE_GSO` in the senders, or the two
   arms lose incomparable units (a 64 KB super-packet versus a 1350-byte
   datagram per drop event).
4. The clean-network (0 % loss) window must carry the same emulated RTT as the
   loss windows, and each arm needs a discarded warm-up window.
5. Percentile assertions belong on p95, not p99, at achievable sample counts; and
   the assertion threshold must come from measurement, not hope.
6. Sustained UDP egress from a cloud VM to a single external IP matches DoS
   heuristics (this campaign earned a GCP abuse notice and an outbound rate limit
   on the project). Remote-cluster experiments should keep the client inside the
   provider's network — which also removes the wifi and consumer-ISP variables.

## Open questions

* Should the probe result influence DNS and agent flows immediately, or only new
  subnets/flows? (Leaning: all new streams, never migrate live ones.)
* Certificate lifetime and rotation policy for the QUIC endpoint; whether to reuse
  the agent-injector CA machinery or keep a dedicated CA.
* ~~Whether `quic-go`'s datagram MTU constraints require fragmenting large UDP
  payloads in `pkg/tunnel`~~ — decided: no fragmentation scheme of our own. A
  size-based hybrid sends fitting payloads as unreliable datagrams and routes
  oversized ones over the flow's stream (see "Unreliable datagrams for tunneled
  UDP").
* Interaction with `telepresence connect --docker` (containerized daemon): the UDP
  probe runs from inside the container network; needs verification that nothing
  assumes host networking.
* ~~Encrypted connection IDs~~ — decided: deferred. Plain CIDs leak internal pod
  IPs to on-path observers; that is topology information, not payload or
  credentials. QUIC-LB's encrypted-CID variant can be added later if a user asks;
  the key-distribution machinery is not worth carrying up front.
* ~~SNI-less Initials~~ — decided: drop silently, never default to a backend; a
  small ephemeral handshake cache routes multi-packet ClientHello fragments (see
  "The forwarder").
