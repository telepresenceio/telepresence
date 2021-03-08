---
description: "Telepresence help you develop Kubernetes services locally without running dependent services or redeploying code updates to your cluster on every change."
---

import Alert from '@material-ui/lab/Alert';
import QSTabs from '../quick-start/qs-tabs'
import QSCards from '../quick-start/qs-cards'

# Intercept a Service in Your Own Environment

<div class="docs-article-toc">
<h3>Contents</h3>

* [Prerequisites](#prerequisites)
* [1. Install the Telepresence CLI](#1-install-the-telepresence-cli)
* [2. Test Telepresence](#2-test-telepresence)
* [3. Intercept all traffic](#3-intercept-all-traffic)
* [4. Intercept traffic coming from a Preview URL](#4-intercept-traffic-coming-from-a-preview-url)
* [What's next?](#img-classos-logo-srcimageslogopng-whats-next)

</div>

Intercepts let you test and debug services locally without also needing to run dependent services.  A typical workflow would be to start the service you wish to develop on locally, then start an intercept. Changes to the local code can then be tested immediately as traffic is proxied to and from the dependent services still running in the cluster.

<!--
When starting an intercept, Telepresence will create a preview URLs. When visiting the preview URL, your request is proxied to your ingress with a special header set.  When the traffic within the cluster requests the service you are intercepting, the [Traffic Manager](../../reference) will proxy that traffic to your laptop.  Other traffic  entering your ingress will use the service running in the cluster as normal.

Preview URLs are all managed through Ambassador Cloud.  You must run `telepresence login` to access Ambassador Cloud and access the preview URL dashboard. From the dashboard you can see all your active intercepts, delete active intercepts, and change them between private and public for collaboration. Private preview URLs can be accessed by anyone else in the GitHub organization you select when logging in. Public URLs can be accessed by anyone who has the link.
-->

<Alert severity="info">For a detailed walk though on creating intercepts using our sample app, follow the <a href="../../quick-start/qs-node/">quick start guide</a>.</Alert>

## Prerequisites
You’ll need [`kubectl` installed](https://kubernetes.io/docs/tasks/tools/install-kubectl/) and [setup](https://kubernetes.io/docs/tasks/tools/install-kubectl/#verifying-kubectl-configuration) to use a Kubernetes cluster, preferably an empty test cluster.

If you have used Telepresence previously, please first reset your Telepresence deployment with:
`telepresence uninstall --everything`.

This guide assumes you have a Kubernetes deployment and service accessible publicly by an ingress controller and that you can run a copy of that service on your laptop.  

## 1. Install the Telepresence CLI

<QSTabs/>

## 2. Test Telepresence

Telepresence connects your local workstation to a remote Kubernetes cluster.

1. Connect to the cluster:
   `telepresence connect`

  ```
  $ telepresence connect

    Launching Telepresence Daemon
    ...
    Connected to context default (https://<cluster public IP>)
  ```

  <Alert severity="info">
    macOS users: If you receive an error when running Telepresence that the developer cannot be verified, open
    <br />
    <strong>System Preferences → Security & Privacy → General</strong>.
    <br />
    Click <strong>Open Anyway</strong> at the bottom to bypass the security block. Then retry the <code>telepresence connect</code> command.
  </Alert>

2. Test that Telepresence is working properly by connecting to the Kubernetes API server:
   `curl -ik https://kubernetes.default.svc.cluster.local`

  <Alert severity="info">
    <strong>Didn't work?</strong> Make sure you are using Telepresence 2.0.3 or greater, check with <code>telepresence version</code> and upgrade <a href="../../howtos/upgrading/">here</a> if needed.
  </Alert>

  ```
  $ curl -ik https://kubernetes.default.svc.cluster.local

    HTTP/1.1 401 Unauthorized
    Cache-Control: no-cache, private
    Content-Type: application/json
    Www-Authenticate: Basic realm="kubernetes-master"
    Date: Tue, 09 Feb 2021 23:21:51 GMT
    Content-Length: 165

    {
      "kind": "Status",
      "apiVersion": "v1",
      "metadata": {

      },
      "status": "Failure",
      "message": "Unauthorized",
      "reason": "Unauthorized",
      "code": 401
    }%

  ```
<Alert severity="info">
    The 401 response is expected.  What's important is that you were able to contact the API.
</Alert>

<Alert severity="success">
    <strong>Congratulations!</strong> You’ve just accessed your remote Kubernetes API server, as if you were on the same network! With Telepresence, you’re able to use any tool that you have locally to connect to any service in the cluster.
</Alert>

## 3. Intercept all traffic

1. List the services that you can intercept with `telepresence list` and make sure the one you want to intercept is listed.

    For the example below, this would confirm that `example-service` can be intercepted by Telepresence:
    ```
    $ telepresence list
    
    ...
    <service name>: ready to intercept (traffic-agent not yet installed)
    ...
    ```

2. Get the name of the port you want to intercept on your service:  
`kubectl get service <service name> --output yaml`.

   For the example below, port `80` is named `http` in the `example-service`:
    
    ```
    $ kubectl get service example-service --output yaml
    
    ...
      ports:
      - name: http
        port: 80
        protocol: TCP
        targetPort: http
    ...
    ```

3. Intercept all traffic for the service you want to use:  
`telepresence intercept <service name> --port <local port>[:<remote port>] --env-file <path to env file>`

   - For the `--port` argument, specify the port on which your local instance of your service will be running.
     - If the service you are intercepting exposes more than one port, specify the one you want to intercept after a colon.
   - For the `--env-file` argument, specify the path to a file on which Telepresence should write the environment variables that your service is currently running with. This is going to be useful as we start our service.
    
   For the example below, Telepresence will intercept traffic going to service `example-service` so that requests reaching it on port `http` in the cluster get routed to `8080` on the workstation and write the environment variables of the service to `~/example-service-intercept.env`. 

   ```
   $ telepresence intercept example-service --port 8080:http --env-file ~/example-service-intercept.env
     
     Using deployment example-service
     intercepted
         Intercept name: example-service
         State         : ACTIVE
         Destination   : 127.0.0.1:8080
         Intercepting  : all TCP connections
   ```

4. Start your local instance of the service on the previously specified port, passing the
environment variables retrieved in the previous step.<a name="start-local-instance"></a>

  Here are a few options to pass the environment variables to your local process:
   - with `docker run`, provide the path to the file using the [`--env-file` argument](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file)
   - with JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.) use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile)
   - with Visual Studio Code, specify the path to the environment variables file in the `envFile` field of your configuration

5. Query the environment in which you intercepted a service the way you usually would and see your local instance being invoked. You can also add breakpoints to debug what is going on in your code!

<Alert severity="success">
    <strong>Crongatulations!</strong> You can now intercept traffic usually going to your Kuberenetes Service to your workstation instead! 
</Alert>

## 4. Intercept traffic coming from a Preview URL

When working on a development environment with multiple engineers, impacting others' workflows is problematic. By using a preview URL with your intercept, Telepresence can route to your workstation only requests coming from that preview URL and route the other ones to the usual container in your cluster.

1. Clean up your previous intercept by removing it:  
`telepresence leave <service name>`

2. Login to Ambassador Cloud, a web interface for managing and sharing preview URLs:  
`telepresence login`

  This opens your browser; login with your GitHub account and choose your org.
    
  ```
  $ telepresence login
    
     Launching browser authentication flow...
     <browser opens, login with GitHub>
     Login successful.
   ```

3. Start the intercept again:  
`telepresence intercept <service name> --port <local port>[:<remote port>] --env-file <path to env file>`

   You will be asked for the following information:
   1. **Ingress layer 3 address**: this would usually be the internal address of your ingress controller in the format `<service name>.namespace`. Per example, if you have a service `ambassador-edge-stack` in the `ambassador` namespace, you would enter `ambassador-edge-stack.ambassador`;
   2. **Ingress port**: the port on which your ingress controller is listening (often 80 for non-TLS and 443 for TLS);
   3. **Ingress TLS encryption**: whether the ingress controller is expecting TLS communication on the specified port;
   4. **Ingress layer 5 hostname**: if your ingress controller routes traffic based on a domain name (often using the `Host` HTTP header), this is the value you would need to enter here.
   
   For the example below, this is going to create a preview URL that will send traffic to the `ambassador` service in the `ambassador` namespace on port `443` using TLS encryption and setting the `Host` HTTP header to `dev-environment.edgestack.me`:
   
   ```
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
       
     Using deployment example-service
     intercepted
         Intercept name   : example-service
         State            : ACTIVE
         Destination      : 127.0.0.1:8080
         Service Port Name: http
         Intercepting     : HTTP requests that match all of:
           header("x-telepresence-intercept-id") ~= regexp("<intercept id>:example-service")
         Preview URL      : https://<random domain name>.preview.edgestack.me
         Layer 5 Hostname : dev-environment.edgestack.me
   ```

4. Start your local service as <a href="#start-local-instance">in the previous step</a>.

5. Go to the preview URL printed after doing the intercept and see that your local service is processing the request.

    <Alert severity="info">
    <strong>Didn't work?</strong> It might be because you have services in between your ingress controller and the service 
    you are intercepting that do not propagate the <code>x-telepresence-intercept-id</code> HTTP Header. Read more on <a href="../concepts/context-prop">context propagation</a>.
    </Alert>

6. Make a request on the URL you would usually query for that environment.  The request should not be routed to your laptop.

   Normal traffic coming into the cluster through the Ingress and not coming from this preview URL will go to deployments in the cluster like normal.

<Alert severity="success">
    <strong>Congratulations!</strong> You can now intercept traffic for your Kubernetes Service without impacting the rest of your team!
</Alert>

## <img class="os-logo" src="../../../images/logo.png"/> What's Next?

<QSCards/>
