---
description: "Telepresence uses Preview URLs to help you collaborate on developing Kubernetes services with teammates."
---

# Share Dev Environments with Preview URLs

Ambassador can generates sharable preview URLs, so you can work on a copy of your service locally and share that copy directly with a teammate for pair programming. While using preview URLs, Telepresence will route only the requests coming from that preview URL to your local environment; the rest will be routed to your cluster as usual.

Preview URLs are protected behind authentication via Ambassador Cloud, ensuring that only users in your organization can view them.  A preview URL can also be set to allow public access, for sharing with outside collaborators.

## Prerequisites

If you have used Telepresence previously, please first reset it with `telepresence uninstall --everything`.

You will need a service running in your cluster that you would like to intercept.

## Creating a Preview URL

1. List the services that you can intercept with `telepresence list` and make sure the one you want to intercept is listed.

2. Login to Ambassador Cloud, a web interface for managing and sharing preview URLs:  
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

  For `--env-file`, specify the path to a file on which Telepresence should write the environment variables that your service is currently running with. This is going to be useful as we start our service.

   You will be asked for the following information:
   1. **Ingress layer 3 address**: This would usually be the internal address of your ingress controller in the format `<service name>.namespace `. For example, if you have a service `ambassador-edge-stack` in the `ambassador` namespace, you would enter `ambassador-edge-stack.ambassador`.
   2. **Ingress port**: The port on which your ingress controller is listening (often 80 for non-TLS and 443 for TLS).
   3. **Ingress TLS encryption**: Whether the ingress controller is expecting TLS communication on the specified port.
   4. **Ingress layer 5 hostname**: If your ingress controller routes traffic based on a domain name (often using the `Host` HTTP header), this is the value you would need to enter here.

   For the example below, you will create a preview URL that will send traffic to the `ambassador` service in the `ambassador` namespace on port `443` using TLS encryption and the hostname `dev-environment.edgestack.me`:
   
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
           header("x-telepresence-intercept-id") ~= regexp("<intercept id>:example-service")
         Preview URL      : https://<random-domain-name>.preview.edgestack.me
         Layer 5 Hostname : dev-environment.edgestack.me
   ```

4. Start your local environment using the environment variables retrieved in the previous step.<a name="start-local-instance"></a>

  Here are a few options to pass the environment variables to your local process:
   - with `docker run`, provide the path to the file using the [`--env-file` argument](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file)
   - with JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.) use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile)
   - with Visual Studio Code, specify the path to the environment variables file in the `envFile` field of your configuration

5. Go to the preview URL printed after doing the intercept and see that your local service is processing the request.

    <Alert severity="info">
    <strong>Didn't work?</strong> It might be because you have services in between your ingress controller and the service you are intercepting that do not propagate the <code>x-telepresence-intercept-id</code> HTTP Header. Read more on <a href="../../concepts/context-prop">context propagation</a>.
    </Alert>

6. Make a request on the URL you would usually query for that environment.  The request should not be routed to your laptop.

   Normal traffic coming into the cluster through the Ingress (i.e. not coming from the preview URL) will route to services in the cluster like normal.

<Alert severity="success">
    <strong>Congratulations!</strong> You have now only intercepted traffic coming from your Preview URL, without impacting your teammates.
</Alert>

7. Share with a teammate.

  You can collaborate with teammates by sending your preview URL to them via Slack or however you communicate. They will be asked to login if they are not already logged into Ambassador Cloud. Upon login they must select the same identity provider and org as you are using, that is how Ambassador Cloud authorizes them to access the preview URL.  When they visit the preview URL, they will see the intercepted service running on your laptop.
  

## Sharing a Preview URL Publicly

To collaborate with someone outside of your identity provider's organization, you must go to [Ambassador Cloud](https://app.getambassador.io/cloud/preview/) (or run `telepresence dashboard`), select the preview URL, and click **Make Publicly Accessible**.  Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on your laptop. Your laptop must be online and running the service for them to see the live intercept.

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard. Removing the intercept either from the dashboard or by running `telepresence leave <service-name>` also removes all access to the preview URL.