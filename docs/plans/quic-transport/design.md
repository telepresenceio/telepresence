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
retried on the next connect. The manager dying no longer affects client⇄agent
traffic at all: the forwarder routes packets and the agents terminate their own
TLS, so attachments keep flowing through a manager restart exactly as they do
today. This is the decisive advantage over relaying agent traffic through the
manager, and the reason the forwarder is a requirement rather than an
optimization.

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
   both transports.

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
   address list + SNI scheme in the endpoint descriptor, concurrent client probe,
   docs reduced to "set `quicTunnel.enabled=true`".

## Open questions

* Should the probe result influence DNS and agent flows immediately, or only new
  subnets/flows? (Leaning: all new streams, never migrate live ones.)
* Certificate lifetime and rotation policy for the QUIC endpoint; whether to reuse
  the agent-injector CA machinery or keep a dedicated CA.
* Whether `quic-go`'s datagram MTU constraints require fragmenting large UDP
  payloads in `pkg/tunnel` or whether streams-with-message-boundaries is good enough
  for the UDP case in practice.
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
