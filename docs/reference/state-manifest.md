---
title: Workstation state manifest
description: The manifest format consumed by "telepresence apply" and "telepresence delete" - its schema, the reconciliation rules, and the handler process lifecycle.
---

# Workstation state manifest

A workstation state manifest declares the Telepresence state of a
workstation: an optional connection and a set of attachments (intercept,
replace, ingest, or wiretap), each optionally paired with the local process
that handles its traffic. `telepresence apply -f <file>` brings the
workstation to the declared state and `telepresence delete -f <file>` tears
it down. The [how-to](../howtos/state-manifest.md) walks through typical
usage; this page describes the format and the exact semantics.

Docker mode is out of scope for the manifest, because that state already has
a declarative home. A docker-mode workstation's state is container state —
the handler processes are containers, with images, networks, environment,
and mounts — and Docker Compose is the established format for declaring it.
The [Telepresence compose extension](compose.md) makes an annotated compose
specification the source for the connection and the attachments as well,
with `telepresence compose up` and `down` filling the roles that apply and
delete have here. A manifest that covered docker mode would be a second
declarative owner of the same state, competing with the compose file.

## Schema

A manifest is a YAML (or JSON) document with the required header

```yaml
apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
```

and at least one of `connection` and `attachments`. Every manifest is
validated against a JSON schema before anything is compared or changed, and
properties not defined by the schema are rejected. The schema is published as
[workstation-state.v1alpha1.json](../schemas/workstation-state.v1alpha1.json);
its source lives in the Telepresence repository at
`pkg/client/cli/manifest/state.schema.yaml`. Point an IDE's YAML language
server at the published schema to get completion and validation while
editing.

### connection

The `connection` object corresponds to the flags of `telepresence connect`:
`name`, `kubeconfig`, `context`, `namespace`, `kubeFlags`,
`managerNamespace`, `mappedNamespaces`, `alsoProxy`, `neverProxy`,
`allowConflictingSubnets`, `proxyVia`, `rerouteLocal`, and `rerouteRemote`.

### attachments

Each attachment has a `type` (`intercept`, `replace`, `ingest`, or
`wiretap`), a unique `name`, and the properties of the corresponding
imperative command's flags: common ones (`namespace`, `container`, `env`,
`mount`, `nodeAgent`, `command`), the intercept/wiretap port and filter
properties (`workload`, `service`, `ports`, `address`, `mechanism`,
`httpHeaders`, `httpPathEqualities`, `httpPathPrefixes`, `httpPathRegexps`,
`plaintext`), the intercept-only `metadata` and `toPod`, and the
replace/ingest forms described by the schema.

## Apply semantics

Apply establishes the connection first, then reconciles each attachment in
declaration order:

- **Connection declared**: a running matching daemon is checked read-only
  against the manifest. A session whose settings differ from the manifest is
  a drift error and nothing is touched. An aligned or session-less daemon
  proceeds through the ordinary connect flow, exactly like a repeated
  `telepresence connect`.
- **Connection not declared**: the currently selected connection is used and
  apply fails when none exists. Delete then leaves that connection intact.
- **Attachment missing**: created, reported as `created`.
- **Attachment established and matching**: left untouched, reported as
  `unchanged`. Only fields the manifest actually specifies are compared, and
  only those with a server-side representation; the env file paths, for
  example, take effect when an attachment is created or re-created.
- **Attachment established but drifted** (different type, workload,
  namespace, container, service, ports, address, mechanism, HTTP filters,
  plaintext, metadata, toPod, mount point, or agent kind): removed and
  created anew from the manifest, reported as `re-created` with the detected
  drift.

The first failing attachment aborts apply. Attachments already brought in
line by the same run are left in place — they are part of the desired state,
and re-running apply is idempotent.

`--dry-run` performs the same loading, validation, and comparison but
changes nothing, reporting `would-connect`, `would-create`, and
`would-re-create` instead. Connection drift is still an error. When the
declared connection does not exist yet there is no session to compare
against, so every attachment is reported as `would-create`.

Both commands honor `--output` for structured output; the summary is then a
single JSON or YAML object instead of one line per attachment.

## Handler processes

An attachment's `command` declares the argv of the local process that
handles its traffic — the manifest equivalent of the trailing
`-- <cmd> <args...>` of the imperative commands. Because such a process must
see the remote environment variables and the mount paths, apply starts it
only after the attachment is established, with the remote environment merged
over the local one, including the same variables the imperative commands
synthesize: `TELEPRESENCE_INTERCEPT_ID`, `TELEPRESENCE_ROOT`, and
`TELEPRESENCE_API_HOST`.

Unlike the imperative commands, apply does not stay around to supervise the
process. It therefore:

- starts the handler detached, in its own process group, with stdout and
  stderr appended to `handler-<attachment name>.log` in the Telepresence log
  directory,
- registers it with the user daemon, which terminates the process whenever
  the attachment is removed — by delete, by a drift re-create, or by an
  imperative leave, and
- records the pid and argv under the Telepresence cache directory, so that a
  later apply can reconcile the running process against the manifest.

Reconciliation of the handler on an attachment that is otherwise unchanged:

| Recorded handler state          | Apply action | Reported         |
|---------------------------------|--------------|------------------|
| Running, argv matches           | none         |                  |
| Exited, or never started        | start        | `handler: started`   |
| Running, argv differs           | stop, start  | `handler: restarted` |
| Running, `command` removed      | stop         | `handler: stopped`   |

A change to `command` alone never re-creates the attachment, because the
command has no server-side representation. `--dry-run` reports the
corresponding `would-start`, `would-restart`, and `would-stop` without
touching the process.

## Delete semantics

Delete removes the manifest's attachments in reverse declaration order and
stops their handler processes. A declared attachment that is not established
is reported as `absent` and is not an error. Only when the manifest declares
`connection` does delete also disconnect it; daemons keep running either
way.
