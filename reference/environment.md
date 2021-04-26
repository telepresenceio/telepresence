---
Description: "How Telepresence can import environment variables from your Kubernetes cluster to use with code running on your laptop."
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

  This will run a command locally with the pod's environment variables set on your laptop as long as the intercept is active.  This can be used in conjunction with a local server command, such as `python [FILENAME]` or `node [FILENAME]` to run a service locally while using the environment variables that were set on the pod via a ConfigMap or other means.

  Another use would be running a subshell, Bash for example:

  `telepresence intercept [service] --port [port] -- /bin/bash`

  This would start the intercept then launch the subshell on your laptop with all the same variables set as on the pod.