# Design note: authenticating the agent's file-sharing ports (plan item 4b)

Decision record required by plan item 4b before implementing SFTP authentication.
The durable parts of this note move into `docs/reference/authentication.md` when
the implementation lands; this file is deleted with the rest of the plan folder.

## The credential (item 4a)

One CA, two forms, both already rooted in `quictunnel.CA` (ECDSA P-256, in-memory,
regenerated on manager restart — restart revokes everything):

1. **X.509 session client certificate** — already minted by
   `MintClientCert(sessionID)` (CN = session ID, 24 h, ExtKeyUsage ClientAuth).
   Used wherever the transport is TLS: the QUIC tunnel.
2. **Signed session token** — new; a compact bearer string
   `v1.<sessionID>.<expiry>.<base64url ECDSA-sig by the CA key>` minted by the
   manager, verified offline by the agent against the CA certificate it already
   receives (`GetQuicAgentCert` → `CaPem`). Used where the transport cannot carry
   a certificate: the FTP `PASS` command (item 4b) and gRPC metadata on the
   agent's plaintext port-forwarded channel (item 5).

Delivery changes:

- The manager creates the CA unconditionally (today only when
  `TUNNEL_QUIC_PORT != 0`); endpoint advertisement stays gated.
- New RPC `GetSessionCredential(SessionInfo)` returns CA PEM, client cert/key
  PEMs, and the signed token to the owning session only (same ownership check as
  `GetQuicTunnelEndpoint`). Works on clusters with the QUIC listener disabled.
- The agent always fetches its material on manager (re)connect — today it skips
  the fetch entirely when `AGENT_QUIC_PORT` is unset — and retains the CA pool
  and the manager's authentication mode on its state, not just inside the QUIC
  listener. `GetQuicAgentCert` gains an `authentication_mode` field so a mode
  change propagates on agent reconnect.

## Enforcement follows the manager's authentication mode

The agent cannot distinguish an old client from an attacker, so requiring
credentials unconditionally would break every existing client against an
upgraded agent. Enforcement therefore mirrors the manager's
`security.authentication.mode`, exactly like the manager's own gRPC surface:

- **disabled / permissive** — credentials are verified and failures logged when
  presented; connections without one are still served (old clients unaffected).
- **enforcing** — no valid session credential, no file operation, on either
  protocol.

## FTP (decided by the plan)

The signed token rides as the FTP password. `go-ftpserver` gains a
`PasswordValidator` mode (and a fix for the nil-dereference in `AuthUser` on
unknown usernames); the agent supplies a validator that verifies the token
signature and expiry against the CA. Client side, `go-fuseftp` gains
user/password parameters on `NewFTPClient` (it logs in as `anonymous/anonymous`
today) and telepresence passes the session token. The docker volume path
(docker-volume-telemount) is unaffected: it reaches the agent through the
daemon's `--local-mount-port` bridge, which is covered under SFTP below.

## SFTP: the three options from the plan

1. **Real SSH server on the agent, manager-issued keys.** Heaviest option: host
   keys and their verification story on three platforms, sshfs/sshfs-win both
   shelling out to a real `ssh`, key files on disk on the workstation. Rejected
   for size and for pushing secrets into files the client must manage.
2. **Carry SFTP inside an authenticated transport.** Chosen — see below.
3. **Retire raw SFTP, route everything through FTP.** Rejected: SFTP is the
   client's *default* mounter (`intercept.useFtp` is opt-in), and
   `--local-mount-port` is documented to expose a raw SFTP port for third-party
   tools (IDEs, docker-volume-telemount) — both would break.

## Chosen: the tunnel is the authenticated transport — a source gate (option 2)

An mTLS wrap of the SFTP stream was designed first and rejected in review: the
QUIC tunnel is already TLS 1.3 end-to-end (with the session client certificate),
and the port-forward path is TLS on its client→manager leg, so TLS-in-SFTP
would double-encrypt the bulkiest data path telepresence has for near-zero
confidentiality gain.

Instead, the observation that decides the design: **every legitimate consumer
already reaches the file-sharing ports through the tunnel** — sshfs and the
IPv6 path via the VIF, `--local-mount-port` and docker-volume-telemount via the
bridge over the VIF — and tunnel-delivered connections are dialed *by the agent
itself*, so they originate from the pod's own IP (or loopback). Only a direct
cross-cluster connection shows a foreign source.

**Agent.** In enforcing mode the SFTP accept path refuses any connection whose
source is not the pod's own address or loopback; disabled/permissive modes
serve everything as before (logged). No TLS layer, no advertisement changes, no
new proto fields. The cryptographic session proof for tunnel-delivered traffic
lives in item 5: `Tunnel`/`WatchDial` verify the session token, so file access
is session-authenticated transitively (client proves its session at the tunnel;
the agent dials its own listener; the listener trusts agent-originated
connections). The FTP listener keeps its password credential per the plan.

**Client.** No changes at all for SFTP: sshfs keeps `directport`, the bridge
stays a plain pipe, Windows (sshfs-win) is untouched.

**Residual weakness, accepted:** an agent injected into a `hostNetwork`
workload shares its IP with everything on that node, so the source gate
degrades to same-node granularity there (still strictly better than the
world-open listener it replaces). The FTP token check is unaffected by this.

**Consumer impact summary (the three consumers from item 3):**

| Consumer | Change |
|----------|--------|
| sshfs / sshfs-win (`pkg/client/remotefs/sftp.go`) | none |
| fuseftp (linked, docker) | FTP password = session token (go-fuseftp bump); no transport change |
| docker volume / `--local-mount-port` (`bridge.go`, `volume.go`) | none |

## Item 5 hook

The same two credential forms bind the agent's gRPC surface: on QUIC, the
handler compares the declared session ID against the TLS peer certificate CN
(the listener already requires a CA-signed client cert but never reads the CN);
on the plaintext port-forwarded channel, the client attaches the signed token as
per-RPC gRPC metadata. `WatchDial`/`Tunnel` reject a session mismatch,
`dialWatchers.Store` refuses to displace a live watcher, and `ReportMetrics`
uses the verified session instead of the caller-declared one.
