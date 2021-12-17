---
description: "Start using Telepresence in your own environment. Follow these steps to intercept your service in your cluster."
---

import Alert from '@material-ui/lab/Alert';
import Platform from '@src/components/Platform';
import QSCards from '../quick-start/qs-cards'

# Intercept a service in your own environment

Telepresence enables you to create intercepts to a target Kubernetes workload. Once you have created and intercept, you can code and debug your associated service locally. 

For a detailed walk-though on creating intercepts using our sample app, follow the [quick start guide](../../quick-start/demo-node/).


## Prerequisites

Before you begin, you need to have [Telepresence installed](<../../install/), and either the Kubernetes command-line tool, [`kubectl`](https://kubernetes.io/docs/tasks/tools/install-kubectl/), or the OpenShift Container Platform command-line interface, [`oc`](https://docs.openshift.com/container-platform/4.2/cli_reference/openshift_cli/getting-started-cli.html#cli-installing-cli_cli-developer-commands). This document uses kubectl in all example commands. OpenShift users can substitute oc [commands instead](https://docs.openshift.com/container-platform/4.1/cli_reference/developer-cli-commands.html).

This guide assumes you have a Kubernetes deployment and service accessible publicly by an ingress controller, and that you can run a copy of that service on your laptop.


## Intercept your service with a global intercept

With Telepresence, you can create [global intercepts](../../concepts/intercepts/?intercept=global) that intercept all traffic going to a service in your cluster and route it to your local environment instead. 

1. Connect to your cluster with `telepresence connect` and connect to the Kubernetes API server:

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

   If you have difficulties connecting, make sure you are using Telepresence 2.0.3 or a later version. Check your version by entering `telepresence version` and [upgrade if needed](../../install/upgrade/).


2. Enter `telepresence list` and make sure the service you want to intercept is listed. For example:

   ```console
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
      * For `--port`: specify the port the local instance of your service is running on. If the intercepted service exposes multiple ports, specify the port you want to intercept after a colon.
      * For `--env-file`: specify a file path for Telepresence to write the environment variables that are set in the pod. 
       The example below shows Telepresence intercepting traffic going to service `example-service`. Requests now reach the service on port `http` in the cluster get routed to `8080` on the workstation and write the environment variables of the service to `~/example-service-intercept.env`.
       ```console
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
   * **Docker:** enter `docker run` and provide the path to the file using the `--env-file` argument. For more information about Docker run commands, see the [Docker command-line reference documentation](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file).
   * **Visual Studio Code:** specify the path to the environment variables file in the `envFile` field of your configuration.
   * **JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.):** use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile).

6. Query the environment in which you intercepted a service and verify your local instance being invoked.
   All the traffic previously routed to your Kubernetes Service is now routed to your local environment

You can now:
- Make changes on the fly and see them reflected when interacting with
  your Kubernetes environment.
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

   <Alert severity="info">

    **Didn't work?** Make sure the port you're listening on matches the one you specified when you created your intercept.

   </Alert>
