---
title: QUIC tunnel transport architecture
description: How the opt-in QUIC tunnel transport is built and why - the packet forwarder, its two-tier routing, the trust bootstrap, and the alternatives that were rejected.
---

This document describes how the opt-in QUIC tunnel transport is built and why.
It is the architecture reference that the QUIC subsystem's code comments point
at; for how to enable and operate the transport see
[quic-transport.md](quic-transport.md), and for the user-facing behavior of a
running system see the same document's runtime sections.

## Background: the port-forwarded data path

All traffic between a Telepresence client and the cluster rides a data path that
was chosen for universal reachability, not throughput:

```
app socket
  -> TUN device (pkg/vif, gVisor netstack terminates TCP/UDP)
  -> one bidirectional gRPC Tunnel stream per flow (rpc/manager Tunnel RPC,
     muxed and managed by pkg/tunnel)
  -> a single HTTP/2 connection, carried as plaintext gRPC
  -> SPDY/WebSocket port-forward through the Kubernetes apiserver
     (pkg/client/portforward)
  -> kubelet -> traffic-manager pod -> re-originated dial to the target
```

The client establishes this in `pkg/client/k8s/connect.go` using
`grpc.WithContextDialer(portforward.Dialer(...))`. Flows destined for intercepted
containers may instead use a second instance of the same construction directly to
the traffic-agent pod (`pkg/client/agentpf`); the choice is made in
`pkg/client/rootd/stream_creator.go`.

This design has two properties worth preserving at all cost:

* **It works wherever `kubectl` works.** No LoadBalancer, no NodePort, no open
  UDP, no NetworkPolicy exceptions, no VPN. RBAC on the apiserver is the entire
  authn/authz story.
* **No TCP-over-TCP meltdown.** Because gVisor terminates TCP at the client and
  the manager re-originates it, the tunnel carries payload, not TCP segments.

It also has costs that the QUIC transport addresses:

1. **Head-of-line blocking.** All flows on a tunnel share that tunnel's one TCP
   connection. A single lost packet on the path to the apiserver stalls every
   flow sharing the connection until retransmission, because HTTP/2 multiplexing
   cannot deliver stream B while stream A has a gap in the underlying byte
   stream.
2. **The apiserver is the data plane.** It was never designed for that. It adds
   two hops (apiserver, kubelet), it is rate-limited and connection-capped by
   cluster operators, and a busy apiserver degrades tunnel throughput and latency
   for reasons unrelated to Telepresence.
3. **Fragile long-lived connections.** Port-forwards drop on apiserver restarts,
   idle timeouts, and network transitions (Wi-Fi to wired, VPN reconnects).
   Reconnection is slow and visible to the user.
4. **UDP is emulated over a reliable stream.** Latency-sensitive UDP (HTTP/3, DNS
   under load, media protocols) inherits TCP's delivery semantics.

Encryption note: the gRPC itself is plaintext; confidentiality comes from the TLS
on the apiserver connection. So the port-forwarded path has exactly one layer of
transport encryption, and it is *a property of routing through the apiserver*.
Any transport that bypasses the apiserver must bring its own encryption and its
own trust bootstrap.

## Why QUIC

QUIC is added as an alternative transport for the tunnel, used opportunistically
when the cluster operator has exposed a UDP path to the traffic-manager, with the
port-forwarded gRPC transport remaining the default and the always-available
fallback. QUIC rather than direct (m)TLS gRPC or WireGuard because:

* **Per-stream independence.** QUIC streams are independently retransmitted; loss
  on one flow no longer stalls the others. This directly fixes cost #1 and maps
  one-to-one onto the existing flow-per-stream model in `pkg/tunnel`.
* **Datagram frames (RFC 9221)** can carry tunneled UDP unreliably, intended to
  address cost #4 (UDP emulated over a reliable stream). This capability is
  implemented on the client↔manager path but is **not proven**: a measurement of
  an inner QUIC (HTTP/3) request/response workload found datagram carriage no
  better than stream carriage and often worse (see the reference doc and
  `perf/README.md`, "Datagram carriage"). It is disabled by default and opt-in via
  `TELEPRESENCE_QUIC_ENABLE_DATAGRAMS`, pending a workload that demonstrates a benefit.
* **Connection migration.** A QUIC connection survives the client changing IP
  address; laptop roaming stops killing the tunnel (cost #3). TLS 1.3 session
  resumption makes the remaining reconnects cheap.
* **Mandatory TLS 1.3.** Encryption is part of the handshake, not a bolt-on. A
  direct gRPC transport would need the same certificate/identity work *plus*
  separate TLS configuration; WireGuard would need key distribution plus a
  userspace stack, and still lacks stream multiplexing.

Two alternatives were weighed and rejected. **Tailscale/`tsnet`** needs a UDP path
or third-party DERP relays (customer traffic transiting servers we do not run),
adds a coordination-server control-plane dependency per developer per cluster, and
supplies a mesh where a point-to-point tunnel is what is needed; what is worth
borrowing — a universally-working relay plus opportunistic upgrade to a direct
encrypted path — is exactly the shape adopted here, with the apiserver port-forward
as the relay. **Direct gRPC to an exposed manager port** is cheaper (only the
dialer changes) and removes the apiserver from the path, but keeps head-of-line
blocking and UDP-over-stream semantics, does not survive address migration, and
still needs the same certificate bootstrap; spending that identity work on the
transport that *also* fixes HoL blocking, UDP fidelity, and roaming is the better
trade. If the bootstrap machinery is ever wanted on its own, a direct-TLS-gRPC mode
falls out of it almost for free.

## Trust bootstrap

The direct path must not weaken the "kubeconfig is the credential" model. The
port-forwarded gRPC connection -- which the client always establishes first, and
which is authenticated by Kubernetes RBAC -- doubles as the trust channel:

1. The traffic-manager generates an ephemeral CA and a server certificate for
   its QUIC endpoint at startup, held only in memory. Rotation is the manager's
   problem; nothing is stored on the client.
2. Over the existing port-forwarded connection, the client requests the QUIC
   endpoint descriptor: address(es), port, CA bundle, and a short-lived
   session-scoped client certificate (CN = session ID).
3. The client dials QUIC, verifying the server against exactly that CA (no system
   roots), and presents the session credential. The manager binds the QUIC
   connection to the already-established session, and checks per stream that the
   stream's declared session ID matches the client certificate's CommonName.

An attacker who can reach the UDP port but has no RBAC access gets a TLS handshake
they cannot complete. An operator who never exposes the port gets the
port-forwarded behavior, unchanged.

The CA is ephemeral: a manager restart mints a new one, which implicitly revokes
every certificate the previous manager signed. Both ends re-fetch (the client its
CA bundle and client certificate on reconnect, an agent its server certificate over
its re-established manager session) and converge on the new CA within seconds.

## Reachability and fallback

* The Helm chart has an opt-in QUIC endpoint: a UDP path to the forwarder plus a
  Service of the operator's choosing (LoadBalancer or NodePort; the explicit
  `externalHost` override covers anything else). Default: disabled.
* The client probes the advertised endpoint concurrently with normal startup. If
  the handshake succeeds within a short budget, new tunnel streams use QUIC;
  otherwise everything stays on the port-forwarded path.
* Fallback is per-connection and silent. Mid-session loss of the QUIC path (for
  example a firewall that starts dropping UDP after idle) moves *new* streams back
  to gRPC; established QUIC streams are torn down like any dropped tunnel stream
  and redialed by the application. A session that has fallen back re-probes QUIC on
  an interval and returns to it without a reconnect once the path is healthy again.
* `telepresence status` reports which transport is active so support conversations
  do not have to guess.

## Where it plugs in

The abstraction seam already exists and does not move:

* `pkg/tunnel/provider.go` -- `Provider`/`StreamProvider` is how stream consumers
  obtain a tunnel stream. A `quicProvider` implements the same interface by opening
  a QUIC stream and framing `TunnelMessage`s on it (length-prefixed protobuf, same
  messages, same `pkg/tunnel` stream protocol version negotiation).
* `pkg/tunnel/stream.go` -- `GRPCStream` is just `Recv`/`Send` of `TunnelMessage`;
  the stream state machine (idle timers, closeSend/Disconnect handshake) is
  transport-agnostic and reused as-is. Keeping the message framing identical means
  the manager-side `state.Tunnel` handling and the agent forwarding paths do not
  care which transport delivered the stream.
* `pkg/client/rootd/stream_creator.go` -- the decision point that picks manager vs.
  agent provider additionally picks QUIC vs. gRPC per the probe result.
* Manager side: a QUIC listener (`quic-go`) accepts streams, decodes the same
  `TunnelMessage` framing, and feeds them into the existing `state.Tunnel` entry
  point (`cmd/traffic/cmd/manager/service.go`).

All QUIC -- manager- and agent-bound alike -- is routed through a single stateless
packet forwarder. *Externally* exposing anything per agent pod is out of scope;
agents listen only on their pod IPs and are reachable solely through the forwarder.

## The forwarder

The forwarder is the architecture's single exposed component and its foundation: a
stateless QUIC *packet* router that owns the one UDP entry point into the cluster.
Every QUIC connection -- `client<->manager` and `client<->agent` alike -- passes through
it, encrypted end to end. The forwarder never terminates TLS, holds no keys, and
sees no plaintext; the manager and the agents run QUIC listeners on their pod IPs,
reachable only through it.

Routing works in two tiers, following the QUIC-LB pattern
(draft-ietf-quic-load-balancers):

* A connection's first packet (the client Initial) is routed by the **SNI** in its
  ClientHello, which is readable without terminating TLS. SNI names identify the
  backend: `traffic-manager.telepresence` for the traffic-manager,
  `<pod-uid>.agent.telepresence` for an agent (pkg/quicfwd's `ManagerSNI` and
  `AgentSNI`). The names are fixed rather than install-scoped: every resolution is
  validated against the backend allowlist below, which each install's forwarder
  receives from its own traffic-manager, so two installs in one cluster cannot
  cross-route even though they use the same manager name.
* Every subsequent packet is routed by the **server-issued connection ID**:
  backends mint connection IDs that encode their own pod IP (quic-go supports
  custom connection-ID generators; an IPv6 address plus a version/length octet fits
  inside the 20-byte CID limit). A forwarder can route any mid-connection packet
  from the CID alone. This is also what makes connection migration work: a client
  whose source address changes is a new source to the forwarder, but its packets
  still carry the same server-issued CID, so they route to the same backend and
  quic-go's own path validation confirms the new path.

Statelessness is what makes the forwarder an acceptable hard dependency: a restart
loses nothing that matters, replicas need no coordination, and it runs as a small
Deployment that can be scaled freely. It ships as another command in the tel2 image.
"Stateless" here means stateless for *routing identity*: every routing decision comes
from the packet itself (SNI or server-issued CID), never from remembered
per-connection state. The forwarder does keep soft per-flow relay state -- which client
address a given backend flow's responses go back to -- but that is reconstructed from
the next client packet after a restart (QUIC is client-initiated), so it changes none
of the properties above.

That relay state is keyed by the client's source address alone, which rests on an
invariant the client upholds: **every QUIC connection is dialed on a UDP socket of its
own** (`quic.DialAddr`; one connection per client 4-tuple). One source address
therefore maps to exactly one backend. A client that shared a socket -- say, one
`quic.Transport` carrying the manager connection and agent connections together --
would have every packet of the second connection forwarded to the first one's backend,
because an established flow wins before any packet inspection. Anything that changes
the client's socket model must revisit this keying.

One qualification to "stateless": a ClientHello can span multiple Initial packets
(post-quantum hybrid key shares push it past one packet's CRYPTO capacity, and Go's
TLS stack sends them by default), and continuation fragments carry no SNI. The
forwarder therefore keeps a small, ephemeral **handshake cache** -- original client
DCID + source address -> backend, seconds-scale TTL, consulted only for long-header
packets. Established connections never touch it (server-issued CIDs route those), so
a forwarder restart costs only the handshakes in flight at that moment. Because the
cache is keyed on source address, a client whose address changes mid-handshake -- as
opposed to after it -- is the one case migration does not cover.

Anything that resolves to no backend -- non-QUIC junk on the port, QUIC versions the
forwarder cannot read Initial keys for, SNI-less ClientHellos from foreign clients,
malformed packets, undecodable CIDs -- is **dropped silently** (rate-limited metric,
no response). Nothing is ever defaulted to a backend: routing unauthenticated
traffic inward would only relocate the DoS surface, and the Telepresence client
always sends SNI in a known version.

**Backend allowlist (required).** A CID-routing forwarder would otherwise be an open
UDP redirector to any pod IP an attacker encodes into a forged CID. The forwarder
validates every routing decision -- SNI resolution and decoded CIDs -- against the
set of live manager and agent backends, which the traffic-manager streams to it as
full-replacement snapshots over its in-cluster gRPC port (the `WatchQuicBackends`
RPC), derived from the agent sessions the manager already tracks; the forwarder needs
no Kubernetes API access of its own. Packets that resolve outside the allowlist are
dropped. The allowlist is soft state with a deliberate asymmetry: losing the manager
keeps the last known-good snapshot, so established routing keeps working, but a
forwarder that has never received a snapshot drops everything until the first one
arrives. `WatchQuicBackends` itself carries no credential -- any in-cluster caller can
read the manager/agent pod IPs, UIDs, and QUIC ports it serves -- which matches the
plaintext in-cluster posture of the manager's other gRPC endpoints and exposes no
keys: reaching a backend still requires completing its mTLS handshake.

**Failure modes.** The forwarder dying kills every QUIC connection at once --
immediately and unambiguously (connection error, never a hang) -- and every consumer
falls back to its port-forward path, which does not involve the forwarder. QUIC is
retried on the next connect. The manager dying no longer affects the `client<->agent`
*data path*: the forwarder routes packets and the agents terminate their own TLS, so
cluster-originated traffic to an intercepted workload keeps tunneling to the laptop
handler through a manager outage. This is the decisive advantage over relaying agent
traffic through the manager, and the reason the forwarder is a requirement rather
than an optimization.

The scope of that guarantee is narrower than "everything keeps working". What
survives a manager outage is the agent attachment data path -- a request that
*originates in the cluster*, hits the intercepted pod, and is tunneled
agent -> forwarder -> laptop. What does *not* survive is traffic the developer
originates from the laptop through the VPN (`curl some-cluster-service`): that path
needs cluster DNS resolution and subnet routing, both of which run over the
manager-bound tunnel, so it is down for the duration of the outage like everything
else VPN-borne. The guarantee also assumes the forwarder itself keeps running through
the outage: a forwarder that restarts while the manager is down comes up with no
allowlist snapshot and drops everything until the manager returns.

## Agent connections over QUIC

The client's connection to a traffic-agent -- sidecar or node-agent alike -- is a
gRPC connection to the agent's API port, carried by default over its own Kubernetes
port-forward per agent pod. Everything an attachment needs (the `WatchDial` reverse
dials, the agent `Tunnel` streams, environment and mount negotiation) flows over
that one connection, so moving *it* moves the entire attachment.

* The agent runs a QUIC listener on its pod IP (no exposure; reachable only via the
  forwarder). On arrival -- and again whenever its manager connection is
  re-established, since a manager restart mints a new CA -- it requests a server
  certificate for its SNI name over its existing, authenticated manager session. It
  accepts any client certificate chaining to the CA; all such certificates are
  short-lived and session-scoped by construction. This is not a new authorization
  surface: an authenticated session already reaches every managed pod -- agent API
  ports included -- at the network level through the manager tunnel/VPN, which enforces
  no per-user RBAC of its own (the manager dials whatever destination a tunnel stream
  names, with the manager's own cluster access). Gating the agent QUIC path on a
  session-scoped certificate is therefore no weaker than -- in fact slightly stricter
  than -- the VPN path it parallels.
* `agentpf` swaps the transport under the agent gRPC connection: instead of a
  Kubernetes port-forward, a `grpc.WithContextDialer` that dials the forwarder with
  the agent's SNI name. One QUIC connection per agent (TLS terminates at the agent,
  so connections cannot be shared across agents), each dialed on a UDP socket of its
  own -- the forwarder's source-address keying (see "The forwarder" above) forbids
  sharing one socket between connections. The agent's SNI name travels in the
  `AgentPodInfo` the client already watches.
* Fallback: if the QUIC connection to an agent dies, `agentpf`'s existing reconnect
  logic dials the Kubernetes port-forward instead. The port-forward machinery is
  only ever bypassed, never disabled; it remains the reconnect target for the
  remainder of the session. Caller cancellation is, as always, not a transport
  failure. After a manager rollout re-issues the CA, the client's manager-endpoint
  re-probe also clears the per-agent fallback latches so new agent dials re-attempt
  QUIC.

Path comparison: the port-forward is client -> apiserver -> kubelet -> agent, one
TCP connection per agent pod, each subject to HoL blocking and apiserver throughput
limits. The forwarded path is client -> forwarder -> agent, where the middle hop is
stateless packet forwarding: per-stream independence end to end, no apiserver, no
TLS re-termination, and no session state anywhere in the path.

## Zero-configuration endpoint discovery

`quicTunnel.enabled=true` is sufficient for the common case. It deploys the
forwarder and its Service (`LoadBalancer` by default) and enables the QUIC listeners
in the manager and the agents. In-cluster ports stay chart defaults -- they are
pod-internal and no admin has a reason to care about them. The externally reachable
address is discovered rather than configured:

* The manager watches the forwarder's Service (RBAC grants get/list/watch on
  services, and on nodes in cluster-scoped installs).
* `LoadBalancer`: advertise `status.loadBalancer.ingress[].ip|hostname` with the
  Service port, once assigned. Until assignment the endpoint is simply not
  advertised; clients pick it up on a later connect.
* `NodePort`: advertise the assigned `nodePort` with node addresses, preferring
  `ExternalIP` over `InternalIP`.
* The endpoint descriptor carries an ordered list of candidate addresses rather than
  a single host, plus the SNI scheme. The client dials candidates concurrently
  within the probe budget and keeps the first whose handshake completes. An
  unreachable candidate is harmless -- that is the silent-fallback property doing its
  job -- so discovery can guess generously.
* `quicTunnel.externalHost`/`externalPort` remain as overrides that replace
  discovery entirely, for topologies the manager cannot see (NAT in front of the
  LoadBalancer, port remapping, DNS names that only resolve on the developer VPN).
* Namespace-scoped installs may lack node read access; NodePort discovery then
  degrades to requiring the explicit override, as the reference documentation states.

## Non-goals

* Replacing the port-forwarded gRPC transport. It remains the default and the only
  path that requires zero cluster configuration.
* NAT hole-punching (ICE/STUN between client and node, with the port-forward as
  signaling channel). It is the natural *next* step if evidence shows a significant
  population that cannot open UDP ingress but could hole-punch, and nothing here
  precludes it -- the trust bootstrap and the QUIC transport are exactly the pieces
  it would reuse.
* Embedded Tailscale/`tsnet` support for shops that already run a tailnet. If asked
  for, it is a reachability variant (the manager becomes dialable over the tailnet),
  not a transport variant, and is orthogonal to this work.
* Multiplexing changes inside `pkg/tunnel`. The flow-per-stream model is kept.
* Application 0-RTT. QUIC/TLS session resumption is used -- a reconnect resumes rather
  than paying a full handshake -- but tunnel payloads are never sent as 0-RTT early
  data: early data is replayable, and replaying a tunnel write could duplicate TCP
  bytes, UDP datagrams, or a dial's side effects. Resumption stays confined to the
  handshake.
* Active local-path migration. CID routing already survives the client's *source
  address* changing (NAT rebind) and a forwarder restart, but rootd does not watch for
  the workstation gaining a new local interface and proactively open a path over it; a
  roam that changes the local interface falls back and re-probes like any other path
  loss. Proactive migration is a possible later refinement -- one whose new path must
  get a socket of its own, to preserve the one-connection-per-source-address invariant
  the forwarder's flow keying relies on.
* Multiple traffic-manager replicas. The QUIC endpoint assumes a single manager pod:
  the CA and session state are process-local and the manager SNI is not pod-specific,
  so a second replica could hand a client a certificate one manager minted while the
  forwarder routes the fixed manager SNI to another. The manager runs as a single
  replica; more than one is unsupported for the QUIC path (the port-forwarded path is
  unaffected). The connection ID also carries the backend's own (internal) pod IP in
  the clear; obfuscated CIDs (QUIC-LB style) are a possible hardening if that ever
  matters.
* Pipelining the per-flow stream setup. Opening a flow still costs one round trip
  (`streamInfo` -> `streamOK`) before the server-side dial begins. That RTT was hidden
  by larger overheads on the port-forwarded path but is visible on QUIC for short-lived
  calls; letting the server validate `streamInfo`, dial immediately, and pipeline the
  reply is a worthwhile follow-up that the flow-per-stream model already allows.
