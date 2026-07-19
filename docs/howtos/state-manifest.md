---
title: Declare the workstation state with a manifest
description: Describe a connection, attachments, and the local processes that handle their traffic in a YAML manifest, then let "telepresence apply" and "telepresence delete" set the workstation up and tear it down.
hide_table_of_contents: true
---

# Declare the workstation state with a manifest

Setting up a development session often means running the same sequence of
commands: connect, attach to one or more workloads, and start the local
processes that handle their traffic. A workstation state manifest captures
that setup in a single YAML file. `telepresence apply -f <file>` brings the
workstation to the state the manifest describes, and `telepresence delete -f
<file>` tears it down again. Apply is idempotent, so the manifest can be
checked into the project repository and re-applied at any time; whatever
already matches is left untouched.

## A first manifest

The following manifest declares a connection and a single intercept of the
`api` workload's `http` port:

```yaml
apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
connection:
  namespace: my-team
attachments:
  - type: intercept
    name: api
    ports: ["8080:http"]
```

```console
$ telepresence apply -f dev.yaml
connection: connected
intercept api: created
```

Running the same command again changes nothing:

```console
$ telepresence apply -f dev.yaml
connection: reused
intercept api: unchanged
```

## Run the local process as part of apply

The local process that handles the intercepted traffic usually needs the
remote container's environment variables and volume mounts, so it must start
after the attachment is established. Declare it with `command` — the manifest
equivalent of the trailing `-- <cmd> <args...>` accepted by `telepresence
intercept` and its siblings:

```yaml
apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
connection:
  namespace: my-team
attachments:
  - type: intercept
    name: api
    ports: ["8080:http"]
    env:
      file: api.env
    command: ["node", "server.js"]
```

```console
$ telepresence apply -f dev.yaml
connection: connected
intercept api: created, handler: started
```

The process runs detached with the remote environment merged into its own,
and its output goes to `handler-api.log` in the Telepresence log directory.
A later apply restarts it if it has exited, or if the manifest's `command`
has changed; changing only the command never disturbs the attachment itself:

```console
$ telepresence apply -f dev.yaml
connection: reused
intercept api: unchanged, handler: restarted
```

## Several attachments

Attachments are applied in the order they are declared, and each type takes
the same properties as the flags of its imperative command:

```yaml
apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
connection:
  namespace: my-team
attachments:
  - type: intercept
    name: api
    ports: ["8080:http"]
    command: ["node", "server.js"]
  - type: ingest
    name: worker
    mount:
      path: /tmp/worker-volumes
  - type: wiretap
    name: billing
    ports: ["9090:grpc"]
```

## Preview with --dry-run

`--dry-run` loads and validates the manifest, compares it against the current
state, and reports what apply would do without touching anything:

```console
$ telepresence apply --dry-run -f dev.yaml
connection: reused
intercept api: would-re-create (drift: ports: manifest=[8081:http] actual port=http target-port=8080 pod-ports=[])
ingest worker: unchanged
wiretap billing: would-create
```

A manifest attachment that differs from what is currently established — a
different port, container, filter, or mount point — is re-created by a real
apply: removed, then created from the manifest.

## Tear it down

`telepresence delete -f <file>` removes the manifest's attachments in reverse
order and stops their handler processes:

```console
$ telepresence delete -f dev.yaml
connection: disconnected
wiretap billing: removed
ingest worker: removed
intercept api: removed, handler: stopped
```

Only a manifest that declares `connection` disconnects during delete. A
manifest with attachments alone applies to, and deletes from, whatever
connection is currently established, and leaves that connection running.

The manifest format, the exact reconciliation rules, and the handler process
lifecycle are described in the
[workstation state manifest reference](../reference/state-manifest.md).
