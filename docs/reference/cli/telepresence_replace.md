---
title: telepresence replace
description: Replace a container
hide_table_of_contents: true
---

Replace a container

### Usage:
```
  telepresence replace [flags] &lt;name&gt; [-- [[docker run flags] &lt;image name&gt;] OR [&lt;command&gt;]] args...]
```

### Flags:
```
      --address string                 Local address to forward to, e.g. '--address 10.0.0.2' (default &quot;127.0.0.1&quot; or name of container)
      --container string               Name of container that should be replaced. Can be omitted if the workload only has one container.
      --detailed-output                Provide very detailed info about the replace when used together with --output=json or --output=yaml'
      --docker-build string            Build a Docker container from the given docker-context (path or URL), and run it with replaced environment and volume mounts, by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'
      --docker-build-opt stringArray   Options to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag.
      --docker-debug string            Like --docker-build, but allows a debugger to run inside the container with relaxed security
      --docker-mount string            The volume mount point in docker. Defaults to same as &quot;--mount&quot;
      --docker-run                     Run a Docker container with replaced environment, volume mount, by passing arguments after -- to 'docker run', e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'
  -e, --env-file string                Also emit the remote environment to an file. The syntax used in the file can be determined using flag --env-syntax
  -j, --env-json string                Also emit the remote environment to a file as a JSON blob.
      --env-syntax string              Syntax used for env-file. One of &quot;docker&quot;, &quot;compose&quot;, &quot;sh&quot;, &quot;csh&quot;, &quot;cmd&quot;, &quot;json&quot;, and &quot;ps&quot;; where &quot;sh&quot;, &quot;csh&quot;, and &quot;ps&quot; can be suffixed with &quot;:export&quot; (default &quot;docker&quot;)
  -h, --help                           help for replace
      --local-mount-port uint16        Do not mount remote directories. Instead, expose this port on localhost to an external mounter
      --mount string                   The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use &quot;true&quot; to have Telepresence pick a random mount point (default). Use &quot;false&quot; to disable filesystem mounting entirely. Append &quot;:ro&quot; to mount everything read-only. (default &quot;true&quot;)
  -p, --port strings                   Local ports to forward to. Use &lt;local port&gt;:&lt;identifier&gt; to uniquely identify container ports, where the &lt;identifier&gt; is the port name or number. Use &quot;all&quot; (the default) to forward all ports declared in the replaced container to their corresponding local port.  (default [all])
      --to-pod strings                 Additional ports to forward to the pod containing the replaced container, will available for connections to localhost:PORT. Use this to, for example, access proxy/helper sidecars in the pod. The default protocol is TCP. Use &lt;port&gt;/UDP for UDP ports
      --wait-message string            Message to print when replace handler has started
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
