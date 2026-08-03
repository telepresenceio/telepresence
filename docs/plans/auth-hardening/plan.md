
# Plan: authentication hardening

Five work items, in order. Items 1–3 are independent and can land separately. Item 5
depends on the credential built in item 4.

---

## 1. Unblock the QUIC forwarder under enforcing authentication

The quic-forwarder calls `WatchQuicBackends` with no credentials, and the manager's
authentication interceptor rejects it when
`security.authentication.mode=enforcing`. The forwarder's allowlist then never becomes
ready, it drops every datagram, and its readiness probe never passes.

**Change** — `cmd/traffic/cmd/manager/auth/interceptor.go`

* Add a `watchQuicBackendsMethod` constant next to `versionMethod`, set to
  `/telepresence.manager.Manager/WatchQuicBackends`.
* Return it from `skipAuth` alongside `versionMethod`.

**Change** — `cmd/traffic/cmd/quicforwarder/allowlist.go`

* Raise the visibility of a persistently failing watch: `WatchAllowlist` currently
  surfaces repeated failures only through `pkg/grpc/watcher`'s retry logging. Log at
  warn when the allowlist has still received no snapshot after the first few retries.

**Tests**

* `auth/interceptor_test.go` — in `ModeEnforcing`, a stream call to
  `WatchQuicBackends` with no `authorization` metadata reaches the handler; a stream
  call to a non-exempt method still returns `Unauthenticated`.
* `quicforwarder/allowlist_test.go` — start the existing `fakeQuicBackendsServer`
  behind a stream interceptor that rejects every method except `Version` and
  `WatchQuicBackends` with `codes.Unauthenticated`; assert `allowlist.Ready()` becomes
  true and that manager and agent backends resolve.

**Done when** — with `security.authentication.mode=enforcing` and
`quicTunnel.enabled=true`, the quic-forwarder pod reaches Ready, `/healthz` returns
200, and a client establishes a QUIC tunnel instead of falling back to port-forward.

---

## 2. Authorize `SetLogLevel`

`cmd/traffic/cmd/manager/service.go:1912` accepts no session, checks no principal, and
performs no authorization, yet `state.SetTempLogLevel` propagates the new level to the
manager and to every agent via `WatchLogLevel`.

**Change** — `rpc/manager/manager.proto`

* Add a `SessionInfo session` field to `LogLevelRequest` (additive, next free tag).
* Run `make protoc`.

**Change** — `pkg/client/userd/daemon/grpc.go:341`

* Populate the new `session` field when building the manager-bound `LogLevelRequest`.

**Change** — `cmd/traffic/cmd/manager/service.go`

* In `SetLogLevel`, resolve the session with `ensureClientSession` and reject when it
  is absent or not owned by the caller.
* Follow `authorizeIntercept`'s shape for the identity check: obtain
  `auth.PrincipalFrom(ctx)`; when the principal is nil and `s.authMode` is
  `auth.ModeEnforcing`, return `codes.Unauthenticated`. Outside enforcing mode, log and
  proceed so older clients are unaffected.

**Tests**

* `service_test.go` — `SetLogLevel` with no session is rejected; with a session
  belonging to another identity is rejected; with the owning session succeeds.
* Verify an old client (no `session` field) still succeeds in `permissive` mode and is
  rejected in `enforcing` mode.

**Done when** — an authenticated cluster user with no Telepresence session cannot
change the log level of the manager or any agent.

---

## 3. Scope the agent's SFTP server

`cmd/traffic/cmd/agent/agent.go:142` calls `sftp.NewServer(conn)` with no options, so
absolute paths resolve against the whole agent container filesystem. FTP is already
confined to `agentconfig.ExportsMountPoint`.

**Change** — `cmd/traffic/cmd/agent/agent.go`

* Pass `sftp.WithServerWorkingDirectory(agentconfig.ExportsMountPoint)` to
  `sftp.NewServer`.
* Do **not** pass `sftp.ReadOnly()`. Read-write is required by `MountPolicyRemote`, and
  `MountPolicyRemoteReadOnly` is already enforced by the kubelet mount.

**Verify before merging** — every client mount path still resolves. The agent
advertises `MountPoint` as `ExportsMountPoint/<container>` (`agent.go:320`), so all
three consumers should be unaffected, but each must be exercised:

* `pkg/client/remotefs/sftp.go` (sshfs, and sshfs-win on Windows)
* `pkg/client/remotefs/fuseftp.go` / `fuseftp_linked.go` / `fuseftp_docker.go`
* the docker volume path in `pkg/client/docker/volume.go`

**Tests**

* Agent unit test: a client of the running `sftpServer` can read a file under
  `ExportsMountPoint` and cannot read one outside it.
* Run the existing mount suites: `integration_test/ignored_mounts_test.go`,
  `integration_test/podscaling_test.go`, `regression_test/suites/mounts/`.

**Done when** — the SFTP server serves only the exports tree, and all mount suites
pass on Linux, macOS, and Windows.

---

## 4. Authenticate the agent's file-sharing ports

Both file-sharing servers accept any peer. SFTP (`agent.go:142`) runs the SFTP
subsystem straight over TCP with no SSH layer, so no authentication or encryption at
all. FTP registers a single `anonymous` user whose password is the wildcard `"*"`
(`go-ftpserver/server.go:121`), so any password is accepted. Both bind `:0` on all
interfaces and both ports are published in `AgentInfo`.

### 4a. Session credential

**Change** — `cmd/traffic/cmd/manager/service.go:148`

* Create the QUIC CA unconditionally, not only when `env.TunnelQuicPort != 0`, so a
  signing key always exists. Keep the endpoint advertisement gated as it is today.

**Change** — manager

* Mint a short-lived, session-scoped credential naming the client session, signed by
  that CA, and return it to the client on connect.

**Change** — client and agent

* Client presents the credential when opening a file-sharing connection.
* Agent verifies the signature offline against the CA PEM it already receives from
  `GetQuicAgentCert` (`ParseMaterial`, `cmd/traffic/cmd/agent/quic.go:55`) and rejects
  unverified peers before serving any file operation.

### 4b. Carry the credential on each protocol

* **FTP** — pass the credential as the password. The protocol already has `USER`/`PASS`
  and `go-ftpserver`'s `AuthUser` is the verification point. Replace the wildcard
  password with real verification.
* **SFTP** — produce a short design note first: raw SFTP has no authentication step,
  and stock `sshfs -o directport` speaks the subsystem directly with no handshake to
  carry a credential. The note must pick one of: run a real SSH server on the agent and
  authenticate with a manager-issued key; carry SFTP inside an authenticated transport;
  or route all mounts through the already-authenticated FTP path and retire the raw
  SFTP listener. Record the choice, its client-side impact across the three consumers
  listed in item 3, and its Windows story, then implement it.

**Change** — `go-ftpserver`

* Fix `AuthUser`'s guard, `!(ok && user.password == "*" || user.password == password)`.
  On a map miss `ok` is false and `user` is nil, and the second `||` operand
  dereferences it. Confirm the nil dereference, then return an error for an unknown
  user without evaluating `user`.

**Tests**

* Agent unit tests: a connection presenting no credential, an expired one, or one
  signed by a different CA is refused on both protocols; a valid one succeeds.
* `go-ftpserver`: `AuthUser` with an unknown username returns an error and does not
  panic.
* Re-run the mount suites from item 3.

**Done when** — neither file-sharing port serves any file operation to a peer that has
not presented a valid session credential.

---

## 5. Bind agent connections to their session

`cmd/traffic/cmd/agent/server.go:110`, `WatchDial` stores a dial-watcher channel under
whatever session ID the caller supplies, and `Store` is last-writer-wins. `Tunnel`
resolves `awaitingForwards` from the caller-declared session ID the same way
(`server.go:76`). A caller naming another client's session ID displaces that client's
channel and receives its intercepted traffic.

**Change** — `cmd/traffic/cmd/agent`

* Verify the credential from item 4a on `WatchDial` and `Tunnel`, and reject any call
  whose declared session ID differs from the one the credential names. Apply this in
  the handlers, not in the QUIC listener, so it covers the port-forward transport too.
* Make `dialWatchers.Store` refuse to displace an existing watcher for a session
  instead of overwriting it.
* Stop trusting the caller-declared `ClientSessionId` in `ReportMetrics`
  (`server.go:96`); use the session the credential names.

**Tests**

* Agent unit tests: `WatchDial` and `Tunnel` naming a session other than the
  credential's are rejected; a second `WatchDial` for a session that already has a live
  watcher is refused; the legitimate client's watcher survives.
* Regression: an attach continues to work across an agent restart and a client
  reconnect, over both QUIC and port-forward.

**Done when** — one client cannot register for, receive, or disrupt another client's
dial requests on any transport.
