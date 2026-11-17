# Plan: caller checks for the local daemons (2.33.2)

Fixes the private advisory GHSA-vmm9-q6qf-c3mx. This branch lives only in the
advisory's private fork until the release. GitHub Actions does not run there, so all
verification is local (see "Verification").

## The problem

The root daemon and the user daemon serve their gRPC APIs on a loopback TCP port. Since
2.33.0 other machines can't reach them, but every process on the workstation can, under
any account. Neither daemon checks who is calling.

* The root daemon runs as root or SYSTEM. It builds its cluster client from a kubeconfig,
  kubectl flags and a client configuration that the caller sends, and it trusts all three.
  The user daemon makes them safe before it sends them, but the root daemon doesn't
  enforce that. A caller that isn't the user daemon can make it act on input of the
  caller's choosing.
* Any connection can end the running session or change its DNS settings, and a second
  `Connect` is answered with the first session's details.
* The user daemon acts in the cluster as the user and hands out the user's credentials
  to the root daemon. In host mode it serves that on the same unchecked listener.

The third point isn't in the report. We found it while working on the first two.

## Decisions

Made by Thomas on 2026-11-17:

* The transport stays TCP on loopback on every platform. No Unix sockets, no named pipes.
* Any local user may connect to the root daemon. There is no installation-wide token, so
  the installers and service definitions don't change.
* The root daemon serves one caller at a time. A session belongs to the connection that
  created it. No secret is involved.
* The user daemon gets a secret, checked the same way as the kubeauth token. It lives as
  long as the user daemon process.
* The root daemon never starts a process. It asks for plugin credentials over the
  connection that owns the session.
* Everything ships together in 2.33.2.

Three work items, in order. Each is one or more commits. This plan is removed in the last
commit on the branch.

---

## 1. The root daemon acts only on inert input

After this item, nothing a caller sends makes the root daemon read a file or start a
program that the caller chose.

**Kubeconfig**

* The user daemon sends a kubeconfig that is final: the kubectl flag overrides merged in
  (`MergedRawConfig` in client-go), reduced to the one context, and flattened so every
  certificate and key is inline. `CreateExternalKubeConfig` in
  `pkg/authenticator/patcher` already does the last two.
* The root daemon validates what it gets before it builds a client
  (`createSession` in `pkg/client/rootd/session.go`). It refuses a kubeconfig that
  contains a credential command, an auth provider, or any reference to a file (CA file,
  client certificate or key file, token file).
* The root daemon stops applying the caller's kubectl flags. Today
  `NetworkConfig.kube_flags` carries all of them and `k8s.NewKubeconfig` applies all of
  them, including the ones that name files. After the merge above, the root daemon needs
  none of them for the cluster client. If a flag turns out to be needed that a kubeconfig
  can't carry (`request-timeout` is the candidate), it goes on a short allow-list in the
  root daemon. No flag that takes a path can be on that list.
* `NetworkConfig.home_dir` is sent today and not used by the root daemon. Stop sending it.

**Credentials from a plugin**

A kubeconfig that uses a credential plugin can't be made final, because the plugin's
tokens expire and the root daemon keeps its own cluster connection for the whole
session. Today the user daemon replaces the plugin with a `telepresence kubeauth`
command line, and the root daemon runs that command line as it finds it.

After this item the root daemon starts no process and dials no address that a caller
gave it. It gets plugin credentials over the connection that owns the session (item 2).

* The user daemon removes the plugin entry from the kubeconfig. It runs the plugin
  itself, as the user, and puts the first credential in the `Connect` request.
* Right after `Connect`, the user daemon opens a stream to the root daemon. When the
  root daemon needs a fresh credential it asks on that stream, and the user daemon runs
  the plugin again and answers. Only the owner's connection can open the stream.
* The root daemon keeps the credential in the token source that
  `pkg/client/k8s/manager_token.go` already has. That source refreshes a minute before
  the expiry time and assumes ten minutes when the plugin gives none. It takes a
  function that fetches the credential, so the same code serves a local plugin and the
  stream. The root daemon also asks again when the cluster answers "unauthorized".
* The token source feeds both uses the root daemon has for the credential: the cluster
  client, including its port-forwards, and the token it presents to the traffic-manager.
* A plugin that returns a client certificate instead of a token is supported with one
  limit. The certificate is part of the TLS setup and can't be swapped on a live client,
  so it is fetched once at session start. When it expires, the session ends with a
  message that says to connect again. The cloud providers and OIDC logins return tokens.
* In host mode the user daemon no longer serves the credential service on its listener.
  Nothing calls it there after this change. The kubeauth daemon and the `kubeauth`
  command stay as they are for Docker mode.

Rejected alternatives: the root daemon composes the `kubeauth` command line itself
(it still starts a process and needs the user daemon's secret), and the user daemon
pushes credentials on a timer (a plugin without an expiry time gives it nothing to time).

**Client configuration**

`Connect` receives the caller's whole client configuration as JSON and uses it for the
session (`client.UnmarshalJSONConfig` in `pkg/client/rootd/grpc.go`).

* The root daemon starts from its own configuration and takes only an allow-list of
  sections from the caller: DNS, routing, cluster, timeouts, gRPC, and log levels when
  it isn't a managed service (today's rule).
* Settings that name files, directories, programs or images never come from the caller.
* A unit test lists every field of the configuration with its decision, and fails when a
  field is added without one.

**Tests**

* Unit: each construct the validator refuses; the token source fed from the stream
  (refresh before expiry, no expiry time, "unauthorized", stream gone); the certificate
  limit; the configuration filter.
* Regression: connect in host mode with a kubeconfig that uses a credential plugin, kept
  up past one token refresh. A fake plugin that prints a stored credential is enough.
* Regression: connect with path-valued kubectl flags (`--kubeconfig`,
  `--certificate-authority`, `--client-certificate`, `--client-key`) works as before.

**Done when** the root daemon refuses every non-inert input, and users of credential
plugins and of path-valued flags see no difference.

---

## 2. The root daemon ties the session to the caller's connection

**Identifying a connection** (`pkg/grpc/server`)

* A gRPC stats handler gives every accepted connection a number. `TagConn` stores it in
  the connection's context under a private key type, so every call on that connection
  can read it and nothing in the daemon's own context can shadow it.
* `HandleConn` reports the number when the connection closes.
* The number is assigned by the server and never sent by a client. The peer address
  that gRPC already provides is not used, because a port can be reused.

**Rules in the root daemon** (`pkg/client/rootd`)

| Call | From the owner | From another connection |
|------|----------------|-------------------------|
| `Connect`, no session | creates the session, caller becomes owner | same |
| `Connect`, session exists | answers as today | refused: "in use" |
| `Version` | allowed | allowed |
| `Status` | full status | idle or in use, nothing else |
| `Quit` | allowed | refused while a session exists |
| `SetLogLevel` | allowed | refused while a session exists |
| every other call | allowed | refused |

* The checks sit in one interceptor with a table of methods, installed through
  `server.NewWithAuth`.
* `Quit` stays open to every connection while no session exists. The upgrade path
  needs that: after an upgrade, or after a user daemon died, `telepresence quit -s`
  quits the root daemon from a fresh connection. By then the session is gone.
* When the owner's connection closes, the root daemon ends the session.
* The in-process root session (Docker mode, `--embed-network`) has no listener and is
  not affected.

**User daemon and CLI**

* The user daemon already dials the root daemon once per session and keeps that
  connection. The dial gets `grpc.WithIdleTimeout(0)`, as the other long-lived
  connections in the client have, so the session never depends on a stream happening to
  be open.
* The recovery loop in `connectRootDaemon` (the root daemon reports another session, so
  disconnect it and retry) is removed. A leftover session now ends when its connection
  closes. "In use" becomes an error for the user: the root daemon is serving another
  session on this machine.
* The `Disconnect` that the user daemon sends on a fresh connection as cleanup is
  removed for the same reason.
* `telepresence quit -s` calls the root daemon's `Quit` after the user daemon has quit.
  That call can arrive before the root daemon has seen the closed connection, so the CLI
  retries for a short, bounded time when the answer is "in use".

**Behavior change**: if the connection between the two daemons breaks while both are
alive, the session ends and the user has to connect again. Today the session survives.
On loopback this should only happen when a process dies.

**Tests**

* Unit, with two client connections against a root daemon service that uses a fake
  session: the second `Connect` is refused, the second connection can't disconnect or
  change DNS settings, its `Status` shows no details, and closing the owner's connection
  ends the session.
* Regression: connect, kill the user daemon, connect again without `quit -s`.
* Regression: `quit -s` right after a session leaves no daemon behind.

**Done when** no connection except the owner's can read, change or end a session.

---

## 3. The user daemon requires a secret

**One check, shared with kubeauth**

* The token interceptor in `pkg/client/docker/kubeauth/cmd.go` moves to
  `pkg/grpc/server`, with a list of methods that are exempt.
* The kubeauth daemon uses the shared interceptor and keeps its per-run token.

**Where the secret lives**

* The daemon info file (`daemon.json` in the user's cache directory) gets a `token`
  field and is written readable by the user only. It is world-readable today.
* Host mode: the CLI creates the secret when it launches the user daemon and writes it
  to the info file, which it already does for the port. The user daemon reads it at
  start. It lives as long as the user daemon process.
* Docker mode: the CLI creates the secret, hands it to the daemon container through its
  environment, and writes the info file. It lives as long as the container.

**Who must present it**

* Every call on the user daemon's listener except `Version`. `Version` stays open so
  that a CLI of another version still gets the version-mismatch message and not
  "unauthenticated".
* The CLI reads the secret from the info file, where it already reads the port, and
  sends it with every call.
* The root daemon never holds the secret. It doesn't call the user daemon (item 1).

**Tests**

* Unit: the interceptor (missing, wrong, right, exempt method); the info file mode.
* Regression: a call to the user daemon without the secret is refused, and `Version`
  answers.
* Regression: the existing areas for connect, intercept and Docker mode pass unchanged.

**Done when** no process without the secret gets anything from the user daemon except
its version.

---

## Docs and release notes

* `CHANGELOG.yml`, version 2.33.2 with `date: (TBD)`, two security entries: one for the
  root daemon, one for the user daemon secret. The second says that a program which
  talks to the user daemon's gRPC API directly must now send the secret, and where to
  find it.
* Any reference page that describes the daemons and their ports gets the new rules.
* The advisory text is completed when it is published, after the release.

## Verification

Nothing runs in the private fork, so before the release PR:

* `make check-unit`, `make lint-go`, `make lint-go GOOS=windows`, `make lint-docs`.
* Regression areas on `kind-dev` (`RTEST_CONTEXT=kind-dev`): smoke, connect, intercept,
  Docker mode, and the new tests above.
* macOS, through the Mac tunnel: host mode and Docker mode, and the package installer's
  root daemon service.
* Windows: the service and the `runas` start, tested by Thomas in a Windows 11 virtual
  machine (VMware) on the development workstation.

## Release

2.33.2 is cut from `release/v2`, which holds only a documentation fix since 2.33.1. No
feature is merged there until 2.33.2 is out. The release follows `/ship-release`.

## Settled in review

* Windows is tested by Thomas, see "Verification".
* No connection except the owner's may quit the root daemon or set its log level while a
  session is active. Both stay open while the daemon is idle.
* The root daemon asks for plugin credentials over the owner's connection and starts no
  process.
* The user daemon's secret is not replaced at session start. The root daemon never holds
  it, so there is no copy to make worthless.
