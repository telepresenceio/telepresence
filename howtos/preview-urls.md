---
description: "Telepresence uses Preview URLs to help you collaborate on developing Kubernetes services with teammates."
---

# Share Development Environments with Preview URLs

Ambassador can generates sharable preview URLs, so you can work on a copy of your service locally and share that copy directly with a teammate for pair programming. While you and your partner work together to debug your service, the production version remains unchanged to the rest of your team until you commit your changes.

Preview URLs are protected behind authentication via Ambassador Cloud, ensuring that only users in your organization can view them.  A preview URL can also be set to allow public access, for sharing with outside collaborators.

## Prerequisites

You must be logged into Ambassdor Cloud, either by running `telepresence login` or by logging in [here](https://app.getambassador.io/cloud/preview/).

xxxx need an intercept

Sharing a preview URL with a teammate requires you both be members of the same organization in the identity provider you use to login to Ambassador Cloud.


## Sharing a Preview URL (With Teammates)

You can collaborate with teammates by sending your preview URL to them via Slack or however you communicate. They will be asked to login if they are not already logged into Ambassador Cloud. When they visit the preview URL, they will see the intercepted service running on your laptop. Your laptop must be online and running the service for them to see the live intercept.

## 4. Create a Preview URL to only intercept certain requests to your service

When working on a development environment with multiple engineers, you don't want your intercepts to impact your 
teammates. Ambassador Cloud automatically generates a Preview URL when creating an intercept if you are logged in. By 
doing so, Telepresence can route only the requests coming from that Preview URL to your local environment; the rest will
be routed to your cluster as usual.

1. Clean up your previous intercept by removing it:  
`telepresence leave <service name>`

2. Login to Ambassador Cloud, a web interface for managing and sharing preview URLs:  
`telepresence login`
    
  ```
  $ telepresence login
    
     Launching browser authentication flow...
     <browser opens, login and choose your org>
     Login successful.
   ```

3. Start the intercept again:  
`telepresence intercept <service-name> --port <local-port>[:<remote-port>] --env-file <path-to-env-file>`

   You will be asked for the following information:
   1. **Ingress layer 3 address**: This would usually be the internal address of your ingress controller in the format `<service name>.namespace`. For example, if you have a service `ambassador-edge-stack` in the `ambassador` namespace, you would enter `ambassador-edge-stack.ambassador`.
   2. **Ingress port**: The port on which your ingress controller is listening (often 80 for non-TLS and 443 for TLS).
   3. **Ingress TLS encryption**: Whether the ingress controller is expecting TLS communication on the specified port.
   4. **Ingress layer 5 hostname**: If your ingress controller routes traffic based on a domain name (often using the `Host` HTTP header), this is the value you would need to enter here.
   
    <Alert severity="info">
        Telepresence supports any ingress controller, not just <a href="../../../tutorials/getting-started/">Ambassador Edge Stack</a>.
    </Alert>

   For the example below, you will create a preview URL that will send traffic to the `ambassador` service in the `ambassador` namespace on port `443` using TLS encryption and setting the `Host` HTTP header to `dev-environment.edgestack.me`:
   
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
    <strong>Didn't work?</strong> It might be because you have services in between your ingress controller and the service you are intercepting that do not propagate the <code>x-telepresence-intercept-id</code> HTTP Header. Read more on <a href="../../concepts/context-prop">context propagation</a>.
    </Alert>

6. Make a request on the URL you would usually query for that environment.  The request should not be routed to your laptop.

   Normal traffic coming into the cluster through the Ingress (i.e. not coming from the preview URL) will route to services in the cluster like normal.

<Alert severity="success">
    <strong>Congratulations!</strong> You have now only intercepted traffic coming from your Preview URL, without impacting your teammates.
</Alert>

## Sharing a Preview URL (With Outside Collaborators)

To collaborate with someone outside of your identity provider's organization, you must go to [Ambassador Cloud](https://app.getambassador.io/cloud/preview/) (or run `telepresence dashboard` to reopen it), select the preview URL, and click **Make Publicly Accessible**.  Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on your laptop. Your laptop must be online and running the service for them to see the live intercept.

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard. Removing the intercept either from the dashboard or by running `telepresence leave <service>` also removes all access to the preview URL.