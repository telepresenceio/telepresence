---
description: "Start using Telepresence in your own environment. Follow these steps to intercept your service in your cluster."
---

import Alert from '@material-ui/lab/Alert';
import Platform from '@src/components/Platform';
import QSCards from '../quick-start/qs-cards'

# Telepresence Quickstart

Telepresence is an open source tool that enables you to set up remote development environments for Kubernetes where you can still use all of your favorite local tools like IDEs, debuggers, and profilers.

## Prerequisites

   - [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/), the Kubernetes command-line tool, or the OpenShift Container Platform command-line interface, [oc](https://docs.openshift.com/container-platform/4.2/cli_reference/openshift_cli/getting-started-cli.html#cli-installing-cli_cli-developer-commands). 
   - A Kubernetes Deployment and Service.

   <Alert severity="info">

  **Don’t have access to Kubernetes cluster?** Try Telepresence in a free remote Kubernetes cluster without having to mess with your production environment. [Get Started >](https://app.getambassador.io/cloud/welcome?select=developer&utm_source=telepresence&utm_medium=website&utm_campaign=quickstart).

   </Alert>

## Install Telepresence on Your Machine

Install Telepresence by running the relevant commands below for your OS. If you are not the administrator of your cluster, you will need [administrative RBAC permissions](../reference/rbac/#administrating-telepresence) to install and use the Telepresence traffic-manager in your cluster.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs

# 1. Download the latest binary (~105 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# Apple silicon Macs

# 1. Download the latest binary (~101 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-arm64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```shell
# 1. Download the latest binary (~95 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-linux-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

To install Telepresence, [Click here to download the Telepresence binary](https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-windows-amd64.zip).

Once you have the binary downloaded and unzipped you will need to do a few things:

1. Rename the binary from `telepresence-windows-amd64.exe` to `telepresence.exe`
2. Move the binary to `C:\Program Files (x86)\$USER\Telepresence\`


</Platform.WindowsTab>
</Platform.TabGroup>

## Install Telepresence in Your Cluster

1. Install the traffic manager into your cluster with `telepresence helm install`. More information about installing Telepresence can be found [here](../install/manager). This will require root access on your machine.

```
$ telepresence helm install
...
Traffic Manager installed successfully
```

## Intercept Your Service

With Telepresence, you can create [global intercepts](../concepts/intercepts/?intercept=global) that intercept all traffic going to a service in your remote cluster and route it to your local environment instead.

1. Connect to your cluster with `telepresence connect` and connect to the Kubernetes API server:

   ```
   $ telepresence connect
   connected to context <your-context>

   ```

   ```console
   $ curl -ik https://kubernetes.default
   HTTP/1.1 401 Unauthorized
   Cache-Control: no-cache, private
   Content-Type: application/json
   ...

   ```

   <Alert>
    The 401 response is expected when you first connect.
   </Alert>

   You now have access to your remote Kubernetes API server as if you were on the same network. You can now use any local tools to connect to any service in the cluster.

2. Enter `telepresence list` and make sure the service you want to intercept is listed. For example:

   ```
   $ telepresence list
   ...
   example-service: ready to intercept (traffic-agent not yet installed)
   ...
   ```

3. Get the name of the port you want to intercept on your service:
   `kubectl get service <service name> --output yaml`.

   For example:

   ```console
   $ kubectl get service example-service --output yaml
   ...
     ports:
     - name: http
       port: 80
       protocol: TCP
       targetPort: http
   ...
   ```

4. Intercept all traffic going to the service in your cluster:
   `telepresence intercept <service-name> --port <local-port>[:<remote-port>] --env-file <path-to-env-file>`.

   - For `--port`: specify the port the local instance of your service is running on. If the intercepted service exposes multiple ports, specify the port you want to intercept after a colon.
   - For `--env-file`: specify a file path for Telepresence to write the environment variables that are set in the pod.
     The example below shows Telepresence intercepting traffic going to service `example-service`. Requests now reach the service on port `http` in the cluster get routed to `8080` on the workstation and write the environment variables of the service to `~/example-service-intercept.env`.

   ```
   $ telepresence intercept example-service --port 8080:http --env-file ~/example-service-intercept.env
   Using Deployment example-service
   intercepted
       Intercept name: example-service
       State         : ACTIVE
       Workload kind : Deployment
       Destination   : 127.0.0.1:8080
       Intercepting  : all TCP connections
   ```

5. <a name="start-local-instance"></a>Start your local environment using the environment variables retrieved in the previous step.

The following are some examples of how to pass the environment variables to your local process:

- **Docker:** enter `docker run` and provide the path to the file using the `--env-file` argument. For more information about Docker run commands, see the [Docker command-line reference documentation](https://docs.docker.com/engine/reference/commandline/run/#env).
- **Visual Studio Code:** specify the path to the environment variables file in the `envFile` field of your configuration.
- **JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.):** use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile).

6. Query the environment in which you intercepted a service and verify your local instance being invoked.
   All the traffic previously routed to your Kubernetes Service is now routed to your local environment

## 🎉 You've Unlocked a Faster Development Workflow for Kubernetes with Telepresence

Now, with Telepresence, you can:

- <div>
   Make changes on the fly and see them reflected when interacting with your remote Kubernetes environment, this is just like hot reloading, but it works across both local and remote environments.
  </div>
- <div>Query services and microservice APIs that are only accessible in your remote cluster's network.</div>
- <div>Set breakpoints in your IDE and re-route remote traffic to your local machine to investigate bugs with realistic user traffic and API calls.</div>

   <Alert severity="info">

  **Didn't work?** Make sure the port you're listening on matches the one you specified when you created your intercept.

   </Alert>

## What’s Next?
- [Learn about the Telepresence architecture.](../reference/architecture)