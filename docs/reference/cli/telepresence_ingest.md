---
title: telepresence ingest
description: Ingest a container
hide_table_of_contents: true
---

Ingest a container

### Usage:
```
  telepresence ingest [flags] <name> [-- [[docker run flags] <image name>] OR [<command>]] args...]
```

### Flags:
```
  -c, --container string               Name of container that provides the environment and mounts for the ingest
      --docker-build string            Build a Docker container from the given docker-context (path or URL), and run it with ingested environment and volume mounts, by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'
      --docker-build-opt stringArray   Options to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag.
      --docker-debug string            Like --docker-build, but allows a debugger to run inside the container with relaxed security
      --docker-mount string            The volume mount point in docker. Defaults to same as "--mount"
      --docker-run                     Run a Docker container with ingested environment, volume mount, by passing arguments after -- to 'docker run', e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'
  -e, --env-file string                Also emit the remote environment to an file. The syntax used in the file can be determined using flag --env-syntax
  -j, --env-json string                Also emit the remote environment to a file as a JSON blob.
      --env-syntax string              Syntax used for env-file. One of "docker", "compose", "sh", "csh", "cmd", "json", and "ps"; where "sh", "csh", and "ps" can be suffixed with ":export" (default "docker")
  -h, --help                           help for ingest
      --local-mount-port uint16        Do not mount remote directories. Instead, expose this port on localhost to an external mounter
      --mount string                   The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use "true" to have Telepresence pick a random mount point (default). Use "false" to disable filesystem mounting entirely. (default "true")
      --to-pod strings                 An additional port to forward from the ingested pod, will be made available at localhost:PORT Use this to, for example, access proxy/helper sidecars in the ingested pod. The default protocol is TCP. Use <port>/UDP for UDP ports
      --wait-message string            Message to print when ingest handler has started
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
