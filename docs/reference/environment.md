---
title: Environment variables
description: "How Telepresence can import environment variables from your Kubernetes cluster to use with code running on your laptop."
hide_table_of_contents: true
---

# Environment variables

Telepresence can import environment variables from the cluster pod when running an intercept.
You can then use these variables with the code running on your laptop of the service being intercepted.

There are several options available to do this:

1. `telepresence intercept [service] --port [port] --env-file=[FILENAME]`

   This will write the environment variables to a file. This file can be used when starting containers locally. The option `--env-syntax`
   will allow control over the syntax of the file. Valid syntaxes are "docker", "compose", "sh", "csh", "cmd", and "ps" where "sh", "csh",
   and "ps" can be suffixed with ":export".

2. `telepresence intercept [service] --port [port] --env-json=[FILENAME]`

   This will write the environment variables to a JSON file. This file can be injected into other build processes.

3. `telepresence intercept [service] --port [port] -- [COMMAND]`

   This will run a command locally with the pod's environment variables set on your laptop.  Once the command quits the intercept is stopped (as if `telepresence leave [service]` was run).  This can be used in conjunction with a local server command, such as `python [FILENAME]` or `node [FILENAME]` to run a service locally while using the environment variables that were set on the pod via a ConfigMap or other means.

   Another use would be running a subshell, Bash for example:

4. `telepresence intercept [service] --port [port] -- /bin/bash`

   This would start the intercept then launch the subshell on your laptop with all the same variables set as on the pod.

5. `telepresence intercept [service] --docker-run -- [CONTAINER]`

   This will ensure that the environment is propagated to the container. Will also work for `--docker-build` and `--docker-debug`.

## Telepresence Environment Variables

Telepresence adds some useful environment variables in addition to the ones imported from the intercepted pod:

### TELEPRESENCE_ROOT
Directory where all remote volumes mounts are rooted. See [Volume Mounts](volume.md) for more info.

### TELEPRESENCE_MOUNTS
Colon separated list of remotely mounted directories.

### TELEPRESENCE_CONTAINER
The name of the intercepted container. Useful when a pod has several containers, and you want to know which one that was intercepted by Telepresence.
