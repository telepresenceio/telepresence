---
description: "CLI options for Telepresence to intercept traffic from your Kubernetes cluster to code running on your laptop."
---

# Client reference

The [Telepresence CLI client](../../quick-start) is used to connect Telepresence to your cluster, start and stop intercepts, and create preview URLs. All commands are run in the form of `telepresence <command>`.

## Commands

A list of all CLI commands and flags is available by running `telepresence help`, but here is more detail on the most common ones.
You can append `--help` to each command below to get even more information about its usage.

| Command | Description |
| --- | --- |
| `connect` | Starts the local daemon and connects Telepresence to your cluster and installs the Traffic Manager if it is missing.  After connecting, outbound traffic is routed to the cluster so that you can interact with services as if your laptop was another pod (for example, curling a service by it's name) |
| [`login`](login) | Authenticates you to Ambassador Cloud to create, manage, and share [preview URLs](../../howtos/preview-urls/)
| `logout` | Logs out out of Ambassador Cloud |
| `license` | Formats a license from Ambassdor Cloud into a secret that can be [applied to your cluster](../cluster-config#add-license-to-cluster) if you require features of the extension in an air-gapped environment|
| `status` | Shows the current connectivity status |
| `quit` | Tell Telepresence daemons to quit |
| `list` | Lists the current active intercepts |
| `intercept` | Intercepts a service, run followed by the service name to be intercepted and what port to proxy to your laptop: `telepresence intercept <service name> --port <TCP port>`. This command can also start a process so you can run a local instance of the service you are intercepting. For example the following will intercept the hello service on port 8000 and start a Python web server: `telepresence intercept hello --port 8000 -- python3 -m http.server 8000`. A special flag `--docker-run` can be used to run the local instance [in a docker container](../docker-run). |
| `leave` | Stops an active intercept: `telepresence leave hello` |
| `preview` | Create or remove [preview URLs](../../howtos/preview-urls) for existing intercepts: `telepresence preview create <currently intercepted service name>` |
| `loglevel` | Temporarily change the log-level of the traffic-manager, traffic-agents, and user and root daemons |
| `gather-logs` | Gather logs from traffic-manager, traffic-agents, user, and root daemons, and export them into a zip file that can be shared with others or included with a github issue. Use `--get-pod-yaml` to include the yaml for the `traffic-manager` and `traffic-agent`s. Use `--anonymize` to replace the actual pod names + namespaces used for the `traffic-manager` and pods containing `traffic-agent`s in the logs. |
| `version` | Show version of Telepresence CLI + Traffic-Manager (if connected) |
| `uninstall` | Uninstalls Telepresence from your cluster, using the `--agent` flag to target the Traffic Agent for a specific workload, the `--all-agents` flag to remove all Traffic Agents from all workloads, or the `--everything` flag to remove all Traffic Agents and the Traffic Manager.
| `dashboard` | Reopens the Ambassador Cloud dashboard in your browser |
| `current-cluster-id` | Get cluster ID for your kubernetes cluster, used for [configuring license](../cluster-config#add-license-to-cluster) in an air-gapped environment |
