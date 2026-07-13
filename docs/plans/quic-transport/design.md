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

Client-to-agent tunnels (`pkg/client/agentpf`) stay on port-forward in the first
iteration; flows to intercepted pods can also be routed via the manager's QUIC
endpoint since the manager already knows how to relay to agents. Exposing QUIC per
agent pod is explicitly out of scope — it multiplies the exposure surface for little
gain.

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
