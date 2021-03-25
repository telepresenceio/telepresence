---
description: "Telepresence uses Preview URLs to help you collaborate on developing Kubernetes services with teammates."
---

import Alert from '@material-ui/lab/Alert';

# Share Dev Environments with Preview URLs

Telepresence can generate sharable preview URLs, allowing you to work on a copy of your service locally and share that environment directly with a teammate for pair programming. While using preview URLs Telepresence will route only the requests coming from that preview URL to your local environment; requests to the ingress will be routed to your cluster as usual.

Preview URLs are protected behind authentication via Ambassador Cloud, ensuring that only users in your organization can view them. A preview URL can also be set to allow public access for sharing with outside collaborators.

## Prerequisites

You should have the Telepresence CLI [installed](../../install/) on your laptop.

If you have Telepresence already installed and have used it previously, please first reset it with `telepresence uninstall --everything`.

You will need a service running in your cluster that you would like to intercept.

<Alert severity="info">
Need a sample app to try with preview URLs?  Check out the <a href="../../quick-start/qs-node/">quick start</a>. It has a multi-service app to install in your cluster with instructions to create a preview URL for that app.
</Alert>

## Creating a Preview URL

1. List the services that you can intercept with `telepresence list` and make sure the one you want is listed.

2. Login to Ambassador Cloud where you can manage and share preview URLs:  
`telepresence login`
    
  ```
  $ telepresence login
    
     Launching browser authentication flow...
     <browser opens, login and choose your org>
     Login successful.
   ```

3. Start the intercept:  
`telepresence intercept <service-name> --port <TCP-port> --env-file <path-to-env-file>`

  For `--port`, specify the port on which your local instance of your service will be running. If the service you are intercepting exposes more than one port, specify the one you want to intercept after a colon.

  For `--env-file`, specify a file path where Telepresence will write the environment variables that are set in the Pod. This is going to be useful as we start our service locally.

   You will be asked for the following information:
   1. **Ingress layer 3 address**: This would usually be the internal address of your ingress controller in the format `<service name>.namespace `. For example, if you have a service `ambassador-edge-stack` in the `ambassador` namespace, you would enter `ambassador-edge-stack.ambassador`.
   2. **Ingress port**: The port on which your ingress controller is listening (often 80 for non-TLS and 443 for TLS).
   3. **Ingress TLS encryption**: Whether the ingress controller is expecting TLS communication on the specified port.
   4. **Ingress layer 5 hostname**: If your ingress controller routes traffic based on a domain name (often using the `Host` HTTP header), enter that value here.

   For the example below, you will create a preview URL for `example-service` which listens on port 8080.  The preview URL for ingress will use the `ambassador` service in the `ambassador` namespace on port `443` using TLS encryption and the hostname `dev-environment.edgestack.me`:
   
   ```
   $ telepresence intercept example-service --port 8080 --env-file ~/ex-svc.env
     
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
           header("x-telepresence-intercept-id") ~= regexp("<intercept-id>:example-service")
         Preview URL      : https://<random-domain-name>.preview.edgestack.me
         Layer 5 Hostname : dev-environment.edgestack.me
   ```

4. Start your local environment using the environment variables retrieved in the previous step.

  Here are a few options to pass the environment variables to your local process:
   - with `docker run`, provide the path to the file using the [`--env-file` argument](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file)
   - with JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.) use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile)
   - with Visual Studio Code, specify the path to the environment variables file in the `envFile` field of your configuration

5. Go to the preview URL that was provided after starting the intercept (the next to last line in the terminal output above). Your local service will be processing the request.

  <Alert severity="success">
    <strong>Success!</strong> You have intercepted traffic coming from your preview URL without impacting other traffic from your Ingress.
  </Alert>

  <Alert severity="info">
    <strong>Didn't work?</strong> It might be because you have services in between your ingress controller and the service you are intercepting that do not propagate the <code>x-telepresence-intercept-id</code> HTTP Header. Read more on <a href="../../concepts/context-prop">context propagation</a>.
  </Alert>

6. Make a request on the URL you would usually query for that environment.  The request should **not** be routed to your laptop.

  Normal traffic coming into the cluster through the Ingress (i.e. not coming from the preview URL) will route to services in the cluster like normal.

7. Share with a teammate.

  You can collaborate with teammates by sending your preview URL to them. They will be asked to log in to Ambassador Cloud if they are not already. Upon login they must select the same identity provider and org as you are using; that is how they are authorized to access the preview URL.  When they visit the preview URL, they will see the intercepted service running on your laptop.
  
<Alert severity="success">
  <strong>Congratulations!</strong> You have now created a dev environment and shared it with a teammate!  While you and your partner work together to debug your service, the production version remains unchanged to the rest of your team until you commit your changes.
</Alert>

## Sharing a Preview URL Publicly

To collaborate with someone outside of your identity provider's organization, you must go to [Ambassador Cloud](https://app.getambassador.io/cloud/preview/), select the preview URL, and click **Make Publicly Accessible**.  Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on your laptop. 

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard. Removing the intercept either from the dashboard or by running `telepresence leave <service-name>` also removes all access to the preview URL.