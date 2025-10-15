---
title: telepresence intercept
description: Intercept a service
hide_table_of_contents: true
---

Intercept a service

### Usage:
```
  telepresence intercept [flags] <name> [-- [[docker run flags] <image name>] OR [<command>]] args...]
```

### Flags:
```
      --address string                 Local address to forward to, e.g. '--address 10.0.0.2' (default "127.0.0.1" or name of container)
      --container string               Name of container that provides the environment and mounts for the intercept. Defaults to the container matching the first intercepted port.
      --detailed-output                Provide very detailed info about the intercept when used together with --output=json or --output=yaml'
      --docker-build string            Build a Docker container from the given docker-context (path or URL), and run it with intercepted environment and volume mounts, by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'
      --docker-build-opt stringArray   Options to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag.
      --docker-debug string            Like --docker-build, but allows a debugger to run inside the container with relaxed security
      --docker-mount string            The volume mount point in docker. Defaults to same as "--mount"
      --docker-run                     Run a Docker container with intercepted environment, volume mount, by passing arguments after -- to 'docker run', e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'
  -e, --env-file string                Also emit the remote environment to an file. The syntax used in the file can be determined using flag --env-syntax
  -j, --env-json string                Also emit the remote environment to a file as a JSON blob.
      --env-syntax string              Syntax used for env-file. One of "docker", "compose", "sh", "csh", "cmd", "json", and "ps"; where "sh", "csh", and "ps" can be suffixed with ":export" (default "docker")
  -h, --help                           help for intercept
      --http-header strings            HTTP header filters. Only requests with matching headers will be intercepted. Supports both formats: --http-header "X-User-ID=dev123" or --http-header "X-User-ID: dev123" (curl -H compatible). Multiple headers use AND logic.
      --http-path-equal strings        HTTP path filters. Only requests with matching paths will be intercepted. Exact path matching.
      --http-path-prefix strings       HTTP path prefix filters. Only requests with matching path prefixes will be intercepted.
      --http-path-regex strings        HTTP path regex filters. Only requests with paths matching the regex will be intercepted.
      --local-mount-port uint16        Do not mount remote directories. Instead, expose this port on localhost to an external mounter
      --mechanism mechanism            Which extension mechanism to use (default "tcp")
      --metadata strings               Metadata to attach to the intercept. Use --metadata key=value to set a single key/value pair, or --metadata key1=value1 --metadata key2=value2 to set multiple key/value pairs. The metadata can be retrieved using the Telepresence API server.
      --mount string                   The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely. Append ":ro" to mount everything read-only. (default "true")
      --plaintext                      Use plaintext instead of TLS when communicating with the intercept handler
  -p, --port strings                   Local ports to forward to. Use <local port>:<identifier> to uniquely identify service ports, where the <identifier> is the port name or number. With --docker-run and a daemon that doesn't run in docker', use <local port>:<container port> or <local port>:<container port>:<identifier>.
      --replace                        Indicates if the traffic-agent should replace application containers in workload pods. The default behavior is for the agent sidecar to be installed alongside existing containers. (DEPRECATED: Use the replace command.)
      --service string                 Optional name of service to intercept. Sometimes needed to uniquely identify the intercepted port.
      --to-pod strings                 Additional ports to forward to the intercepted pod, will available for connections to localhost:PORT. Use this to, for example, access proxy/helper sidecars in the intercepted pod. The default protocol is TCP. Use <port>/UDP for UDP ports
      --wait-message string            Message to print when intercept handler has started
  -w, --workload string                Name of workload (Deployment, ReplicaSet, StatefulSet, Rollout) to intercept, if different from <name>
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
