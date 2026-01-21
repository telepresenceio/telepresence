---
title: telepresence wiretap
description: Wiretap a Service
hide_table_of_contents: true
---

Wiretap a Service

### Usage:
```
  telepresence wiretap [flags] &lt;wiretap_base_name&gt; [-- &lt;command with arguments...&gt;]
```

### Flags:
```
      --address string                 Local address to forward to, e.g. '--address 10.0.0.2' (default &quot;127.0.0.1&quot; or name of container)
      --container string               Name of container that provides the environment and mounts for the wiretap. Defaults to the container matching the first wiretapped port.
      --detailed-output                Provide very detailed info about the wiretap when used together with --output=json or --output=yaml'
      --docker-build string            Build a Docker container from the given docker-context (path or URL), and run it with wiretapped environment and volume mounts, by passing arguments after -- to 'docker run', e.g. '--docker-build /path/to/docker/context -- -it IMAGE /bin/bash'
      --docker-build-opt stringArray   Options to docker-build in the form key=value, e.g. --docker-build-opt tag=mytag.
      --docker-debug string            Like --docker-build, but allows a debugger to run inside the container with relaxed security
      --docker-mount string            The volume mount point in docker. Defaults to same as &quot;--mount&quot;
      --docker-run                     Run a Docker container with wiretapped environment, volume mount, by passing arguments after -- to 'docker run', e.g. '--docker-run -- -it --rm ubuntu:20.04 /bin/bash'
  -e, --env-file string                Also emit the remote environment to an file. The syntax used in the file can be determined using flag --env-syntax
  -j, --env-json string                Also emit the remote environment to a file as a JSON blob.
      --env-syntax string              Syntax used for env-file. One of &quot;docker&quot;, &quot;compose&quot;, &quot;sh&quot;, &quot;csh&quot;, &quot;cmd&quot;, &quot;json&quot;, and &quot;ps&quot;; where &quot;sh&quot;, &quot;csh&quot;, and &quot;ps&quot; can be suffixed with &quot;:export&quot; (default &quot;docker&quot;)
  -h, --help                           help for wiretap
      --http-header strings            HTTP header filters. Only requests with matching headers will be wiretapped. Supports both formats: --http-header &quot;X-User-ID=dev123&quot; or --http-header &quot;X-User-ID: dev123&quot; (curl -H compatible). Multiple headers use AND logic.
      --http-path-equal strings        HTTP path filters. Only requests with matching paths will be wiretapped. Exact path matching.
      --http-path-prefix strings       HTTP path prefix filters. Only requests with matching path prefixes will be wiretapped.
      --http-path-regex strings        HTTP path regex filters. Only requests with paths matching the regex will be wiretapped.
      --local-mount-port uint16        Do not mount remote directories. Instead, expose this port on localhost to an external mounter
      --mechanism mechanism            Which extension mechanism to use (default &quot;tcp&quot;)
      --mount string                   The absolute path for the root directory where volumes will be mounted, $TELEPRESENCE_ROOT. Use &quot;true&quot; to have Telepresence pick a random mount point (default). Use &quot;false&quot; to disable filesystem mounting entirely. Append &quot;:ro&quot; to mount everything read-only. (default &quot;true&quot;)
      --plaintext                      Use plaintext instead of TLS when communicating with the intercept handler
  -p, --port strings                   Local ports to forward to. Use &lt;local port&gt;:&lt;identifier&gt; to uniquely identify service ports, where the &lt;identifier&gt; is the port name or number. With --docker-run and a daemon that doesn't run in docker', use &lt;local port&gt;:&lt;container port&gt; or &lt;local port&gt;:&lt;container port&gt;:&lt;identifier&gt;.
      --service string                 Optional name of service to wiretap. Sometimes needed to uniquely identify the intercepted port.
      --wait-message string            Message to print when wiretap handler has started
  -w, --workload string                Name of workload (Deployment, ReplicaSet, StatefulSet, Rollout) to wiretap, if different from &lt;name&gt;
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
