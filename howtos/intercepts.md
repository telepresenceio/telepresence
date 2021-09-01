---
description: "Start using Telepresence in your own environment. Follow these steps to intercept your service in your cluster."
---

import Alert from '@material-ui/lab/Alert';
import QSTabs from '../quick-start/qs-tabs'
import QSCards from '../quick-start/qs-cards'

# Intercept a service in your own environment

<div class="docs-article-toc">

### Contents

* [Prerequisites](#prerequisites)
* [1. Install the Telepresence CLI](#1-install)
* [2. Test Telepresence](#2-test)
* [3. Intercept your service with a global intercept](#3-global-intercept)
* [4. Intercept your service with a personal intercept and a preview URL](#4-personal-intercept)
* [What's next?](#whats-next)

</div>

<Alert severity="info">

  For a detailed walk-though on creating intercepts using our sample
  app, follow the [quick start guide](../../quick-start/qs-node/).

</Alert>

## Prerequisites

You’ll need [`kubectl`](https://kubernetes.io/docs/tasks/tools/install-kubectl/) or `oc` installed
and set up
([Linux](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/#verify-kubectl-configuration) /
 [macOS](https://kubernetes.io/docs/tasks/tools/install-kubectl-macos/#verify-kubectl-configuration) /
 [Windows](https://kubernetes.io/docs/tasks/tools/install-kubectl-windows/#verify-kubectl-configuration))
to use a Kubernetes cluster, preferably an empty test cluster.  This
document uses `kubectl` in all example commands, but OpenShift
users should have no problem substituting in the `oc` command instead.

If you have used Telepresence previously, please first reset your
Telepresence deployment with: `telepresence uninstall --everything`.

This guide assumes you have a Kubernetes deployment and service
accessible publicly by an ingress controller and that you can run a
copy of that service on your laptop.

## 1. Install the Telepresence CLI {#1-install}

<QSTabs/>

## 2. Test Telepresence {#2-test}

Telepresence connects your local workstation to a remote Kubernetes
cluster.

1. Connect to the cluster:
   `telepresence connect`

   ```console
   $ telepresence connect
   Launching Telepresence Daemon
   ...
   Connected to context default (https://<cluster public IP>)
   ```

   <Alert severity="info">

    macOS users: If you receive an error when running Telepresence that
    the developer cannot be verified, open

    > System Preferences → Security & Privacy → General

    Click **Open Anyway** at the bottom to bypass the security block.
    Then retry the `telepresence connect` command.

   </Alert>

2. Test that Telepresence is working properly by connecting to the
   Kubernetes API server: `curl -ik https://kubernetes.default`

   <Alert severity="info">

    **Didn't work?** Make sure you are using Telepresence 2.0.3 or
    greater, check with `telepresence version` and
    [upgrade](../../install/upgrade/) if needed.

   </Alert>

   ```console
   $ curl -ik https://kubernetes.default
   HTTP/1.1 401 Unauthorized
   Cache-Control: no-cache, private
   Content-Type: application/json
   ...

   ```

   <Alert severity="info">

    The 401 response is expected.  What's important is that you were
    able to contact the API.

   </Alert>

<Alert severity="success">

  **Congratulations!** You’ve just accessed your remote Kubernetes API
  server as if you were on the same network!  With Telepresence,
  you’re able to use any tool that you have locally to connect to any
  service in the cluster.

</Alert>

## 3. Intercept your service with a global intercept {#3-global-intercept}

In this section, we will go through the steps required for you to
create a [global intercept](../../concepts/intercepts/#global-intercept) that
intercepts all traffic going to a service in your cluster and route it
to your local environment instead.  In the [next
section](#4-personal-intercept), we will instead create a personal
intercept that is often more useful than a global intercept.

1. List the services that you can intercept with `telepresence list`
   and make sure the one you want to intercept is listed.

   For example, this would confirm that `example-service` can be intercepted by Telepresence:

   ```console
   $ telepresence list
   ...
   example-service: ready to intercept (traffic-agent not yet installed)
   ...
   ```

2. Get the name of the port you want to intercept on your service:
   `kubectl get service <service name> --output yaml`.

   For example, this would show that the port `80` is named `http` in
   the `example-service`:

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

3. Intercept all traffic going to the service in your cluster:
    `telepresence intercept <service-name> --port <local-port>[:<remote-port>] --env-file <path-to-env-file>`.

   - For the `--port` argument, specify the port on which your local
     instance of your service will be running.
     + If the service you are intercepting exposes more than one port,
       specify the one you want to intercept after a colon.
   - For the `--env-file` argument, specify the path to a file on
     which Telepresence should write the environment variables that
     your service is currently running with. This is going to be
     useful as we start our service.

   For the example below, Telepresence will intercept traffic going to
   service `example-service` so that requests reaching it on port
   `http` in the cluster get routed to `8080` on the workstation and
   write the environment variables of the service to
   `~/example-service-intercept.env`.

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

4. <a name="start-local-instance"></a>Start your local environment
   using the environment variables retrieved in the previous step.

   Here are a few options to pass the environment variables to your
   local process:
   - with `docker run`, provide the path to the file using the [`--env-file` argument](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file)
   - with JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.) use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile)
   - with Visual Studio Code, specify the path to the environment variables file in the `envFile` field of your configuration

5. Query the environment in which you intercepted a service the way
   you usually would and see your local instance being invoked.

   <Alert severity="info">

    **Didn't work?** Make sure the port you're listening on matches
    the one specified when creating your intercept.

   </Alert>

   <Alert severity="success">

    **Congratulations!** All the traffic usually going to your
    Kubernetes Service is now being routed to your local environment!

   </Alert>

You can now:
- Make changes on the fly and see them reflected when interacting with
  your Kubernetes environment.
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

## 4. Intercept your service with a personal intercept and a preview URL {#4-personal-intercept}

When working on a development environment with multiple engineers, you
don't want your intercepts to impact your teammates.  Telepresence
offers a solution to this: instead of creating a global intercept, you
can create a [personal
intercept](../../concepts/intercepts/#personal-intercept) that only
interepts a subset of the traffic going to the service.  This is the
default if you are [logged in to Ambassador Cloud with
Telepresence](../../reference/client/login/).  Additionally if you are
logged in, then by default Telpresence will talk to Ambassador Cloud
to generate a "preview URL" that is set up such that traffic going
through that URL gets intercepted and sent to your local environment.
The rest of the traffic, the traffic not coming through the preview
URL (and not containing the special header that the preview URL uses),
will be routed to your cluster as usual.

1. Clean up your previous intercept by removing it:
   `telepresence leave <service name>`

2. Log in to Ambassador Cloud, a web interface for managing and
   sharing preview URLs:

   ```console
   $ telepresence login
   Launching browser authentication flow...
   <web browser opens, log in and choose your organization>
   Login successful.
   ```

   If you are in an environment where Telepresence cannot launch a
   local browser for you to interact with, you will need to pass the
   [`--apikey` flag to `telepresence
   login`](../../reference/client/login/).

3. Start the intercept again:
   `telepresence intercept <service-name> --port <local-port>[:<remote-port>] --env-file <path-to-env-file>`

   You will be asked for the following information:
   1. **Ingress layer 3 address**: This would usually be the internal
      address of your ingress controller in the format
      `<service-name>.namespace`.  For example, if you have a service
      `ambassador-edge-stack` in the `ambassador` namespace, you would
      enter `ambassador-edge-stack.ambassador`.
   2. **Ingress port**: The port on which your ingress controller is
      listening (often 80 for non-TLS and 443 for TLS).
   3. **Ingress TLS encryption**: Whether the ingress controller is
      expecting TLS communication on the specified port.
   4. **Ingress layer 5 hostname**: If your ingress controller routes
      traffic based on a domain name (often using the `Host` HTTP
      header), this is the value you would need to enter here.

   <Alert severity="info">

    Telepresence supports any ingress controller, not just
    [Ambassador Edge Stack](https://www.getambassador.io/docs/edge-stack/latest/tutorials/getting-started/).

   </Alert>

   For the example below, you will create a preview URL that will send
   traffic to the `ambassador` service in the `ambassador` namespace
   on port `443` using TLS encryption and setting the `Host` HTTP
   header to `dev-environment.edgestack.me`:

   ```console
   $ telepresence intercept example-service --port 8080:http --env-file ~/example-service-intercept.env
   To create a preview URL, telepresence needs to know how cluster
   ingress works for this service.  Please Confirm the ingress to use.

   1/4: What's your ingress' layer 3 (IP) address?
        You may use an IP address or a DNS name (this is usually a
        "service.namespace" DNS name).

          [default: -]: ambassador.ambassador

   2/4: What's your ingress' layer 4 address (TCP port number)?

          [default: -]: 443

   3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?

          [default: n]: y

   4/4: If required by your ingress, specify a different layer 5 hostname
        (TLS-SNI, HTTP "Host" header) to access this service.

          [default: ambassador.ambassador]: dev-environment.edgestack.me

   Using Deployment example-service
   intercepted
       Intercept name         : example-service
       State                  : ACTIVE
       Workload kind          : Deployment
       Destination            : 127.0.0.1:8080
       Service Port Identifier: http
       Intercepting           : HTTP requests that match all of:
         header("x-telepresence-intercept-id") ~= regexp("<intercept id>:example-service")
       Preview URL            : https://<random domain name>.preview.edgestack.me
       Layer 5 Hostname       : dev-environment.edgestack.me
   ```

4. Start your local service as [in the previous
   step](#start-local-instance).

5. Go to the preview URL printed after doing the intercept and see
   that your local service is processing the request.

   <Alert severity="info">

    **Didn't work?** It might be because you have services in between
    your ingress controller and the service you are intercepting that
    do not propagate the `x-telepresence-intercept-id` HTTP Header.
    Read more on [context propagation](../../concepts/context-prop).

   </Alert>

6. Make a request on the URL you would usually query for that
   environment.  The request should not be routed to your laptop.

   Normal traffic coming into the cluster through the Ingress
   (i.e. not coming from the preview URL) will route to services in
   the cluster like normal.

<Alert severity="success">

  **Congratulations!** You have now only intercepted traffic coming
  from your preview URL, without impacting your teammates.

</Alert>

You can now:
- Make changes on the fly and see them reflected when interacting with
  your Kubernetes environment.
- Query services only exposed in your cluster's network.
- Set breakpoints in your IDE to investigate bugs.

...and all of this **without impacting your teammates!**
## <img class="os-logo" src="../../images/logo.png"/> What's Next? {#whats-next}

<QSCards/>
