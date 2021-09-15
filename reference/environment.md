---
description: "How Telepresence can import environment variables from your Kubernetes cluster to use with code running on your laptop."
---

# Environment variables

Telepresence can import environment variables from the cluster pod when running an intercept.
You can then use these variables with the code running on your laptop of the service being intercepted.

There are three options available to do this:

1. `telepresence intercept [service] --port [port] --env-file=FILENAME`

  This will write the environment variables to a Docker Compose `.env` file. This file can be used with `docker-compose` when starting containers locally. Please see the Docker documentation regarding the [file syntax](https://docs.docker.com/compose/env-file/) and [usage](https://docs.docker.com/compose/environment-variables/) for more information.

2. `telepresence intercept [service] --port [port] --env-json=FILENAME`

  This will write the environment variables to a JSON file. This file can be injected into other build processes.

3. `telepresence intercept [service] --port [port] -- [COMMAND]`

  This will run a command locally with the pod's environment variables set on your laptop.  Once the command quits the intercept is stopped (as if `telepresence leave [service]` was run).  This can be used in conjunction with a local server command, such as `python [FILENAME]` or `node [FILENAME]` to run a service locally while using the environment variables that were set on the pod via a ConfigMap or other means.

  Another use would be running a subshell, Bash for example:

  `telepresence intercept [service] --port [port] -- /bin/bash`

  This would start the intercept then launch the subshell on your laptop with all the same variables set as on the pod.

## Telepresence Environment Variables

Telepresence adds some useful environment variables in addition to the ones imported from the intercepted pod:

### TELEPRESENCE_ROOT
Directory where all remote volumes mounts are rooted. See [Volume Mounts](../volume/) for more info.

### TELEPRESENCE_MOUNTS
Colon separated list of remotely mounted directories.

### TELEPRESENCE_CONTAINER
The name of the intercepted container. Useful when a pod has several containers, and you want to know which one that was intercepted by Telepresence.

### TELEPRESENCE_INTERCEPT_ID
ID of the intercept (same as the "x-intercept-id" http header).

Useful if you need special behavior when intercepting a pod. One example might be when dealing with pub/sub systems like Kafka, where all processes that don't have the `TELEPRESENCE_INTERCEPT_ID` set can filter out all messages that contain an `x-intercept-id` header, while those that do, instead filter based on a matching `x-intercept-id` header. This is to assure that messages belonging to a certain intercept always are consumed by the intercepting process.
