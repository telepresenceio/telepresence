---
description: "Telepresence uses Preview URLs to help you collaborate on developing Kubernetes services with teammates."
---

import Alert from '@material-ui/lab/Alert';

# Share development environments with preview URLs

Telepresence can generate sharable preview URLs. This enables you to work on a copy of your service locally, and share that environment with a teammate for pair programming. While using preview URLs, Telepresence will route only the requests coming from that preview URL to your local environment. Requests to the ingress are routed to your cluster as usual.

Preview URLs are protected behind authentication through Ambassador Cloud, and, access to the URL is only available to users in your organization. You can make the URL publicly accessible for sharing with outside collaborators.

## Creating a preview URL

1. Connect to Telepresence and enter the `telepresence list` command in your CLI to verify the service is listed. 
Telepresence only supports Deployments, ReplicaSets, and StatefulSet workloads with a label that matches a Service. 

2. Enter `telepresence login` to launch Ambassador Cloud in your browser.

 If you are in an environment you can't launch Telepresence in your local browser, enter   If you are in an environment where Telepresence cannot launch in a local browser, pass the [`--apikey` flag to `telepresence login`](../../reference/client/login/).

3. Start the intercept with `telepresence intercept <service-name> --port <TCP-port> --env-file <path-to-env-file> `and adjust the flags as follows:
 Start the intercept:
 * **port:** specify the port the local instance of your service is running on. If the intercepted service exposes multiple ports, specify the port you want to intercept after a colon.
 * **env-file:** specify a file path for Telepresence to write the environment variables that are set in the pod. 

4. Answer the question prompts.
   * **IWhat's your ingress' IP address?**: whether the ingress controller is expecting TLS communication on the specified port.
   * **What's your ingress' TCP port number?**: the port your ingress controller is listening to. This is often 443 for TLS ports, and 80 for non-TLS ports.
   * **Does that TCP port on your ingress use TLS (as opposed to cleartext)?**: whether the ingress controller is expecting TLS communication on the specified port.
   * **If required by your ingress, specify a different hostname (TLS-SNI, HTTP "Host" header) to be used in requests.**: if your ingress controller routes traffic based on a domain name (often using the `Host` HTTP header), enter that value here.

   The example below shows a preview URL for `example-service` which listens on port 8080.  The preview URL for ingress will use the `ambassador` service in the `ambassador` namespace on port `443` using TLS encryption and the hostname `dev-environment.edgestack.me`:

   ```console
$ telepresence intercept example-service --port 8080 --env-file ~/ex-svc.env

     To create a preview URL, telepresence needs to know how cluster
     ingress works for this service.  Please Confirm the ingress to use.

     1/4: What's your ingress' IP address?
          You may use an IP address or a DNS name (this is usually a
          "service.namespace" DNS name).

            [default: -]: ambassador.ambassador

     2/4: What's your ingress' TCP port number?

            [default: -]: 80

     3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?

            [default: n]: y

     4/4: If required by your ingress, specify a different hostname
          (TLS-SNI, HTTP "Host" header) to be used in requests.

            [default: ambassador.ambassador]: dev-environment.edgestack.me

     Using deployment example-service
     intercepted
         Intercept name         : example-service
         State                  : ACTIVE
         Destination            : 127.0.0.1:8080
         Service Port Identifier: http
         Intercepting           : HTTP requests that match all of:
           header("x-telepresence-intercept-id") ~= regexp("<intercept id>:example-service")
         Preview URL            : https://<random domain name>.preview.edgestack.me
         Layer 5 Hostname       : dev-environment.edgestack.me
   ```

5. Start your local environment using the environment variables retrieved in the previous step.

  Here are some examples of how to pass the environment variables to your local process:
   * **Docker:** enter `docker run` and provide the path to the file using the `--env-file` argument. For more information about Docker run commands, see the [Docker command-line reference documentation](https://docs.docker.com/engine/reference/commandline/run/#set-environment-variables--e---env---env-file).
   * **Visual Studio Code:** specify the path to the environment variables file in the `envFile` field of your configuration.
   * **JetBrains IDE (IntelliJ, WebStorm, PyCharm, GoLand, etc.):** use the [EnvFile plugin](https://plugins.jetbrains.com/plugin/7861-envfile).

6. Go to the Preview URL generated from the intercept.
Traffic is now intercepted from your preview URL without impacting other traffic from your Ingress.

  <Alert severity="info">
    <strong>Didn't work?</strong> It might be because you have services in between your ingress controller and the service you are intercepting that do not propagate the <code>x-telepresence-intercept-id</code> HTTP Header. Read more on <a href="../../concepts/context-prop">context propagation</a>.
  </Alert>

7. Make a request on the URL you would usually query for that environment.  Don't route a request to your laptop.

  Normal traffic coming into the cluster through the Ingress (i.e. not coming from the preview URL) routes to services in the cluster like normal.

8. Share with a teammate.

   You can collaborate with teammates by sending your preview URL to them. Once your teammate logs in, they must select the same identity provider and org as you are using. This authorizes their access to the preview URL. When they visit the preview URL, they see the intercepted service running on your laptop. 
   You can now collaborate with a teammate to debug the service on the shared intercept URL without impacting the production environment.

## Sharing a preview URL with people outside your team

To collaborate with someone outside of your identity provider's organization:
Log into [Ambassador Cloud](https://app.getambassador.io/cloud/).
 navigate to your service's intercepts, select the preview URL details, and click **Make Publicly Accessible**.  Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on your laptop.

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard. Removing the preview URL either from the dashboard or by running `telepresence preview remove <intercept-name>` also removes all access to the preview URL.

## Change access restrictions

To collaborate with someone outside of your identity provider's organization, you must make your preview URL publicly accessible.

1. Go to [Ambassador Cloud](https://app.getambassador.io/cloud/).
2. Select the service you want to share and open the service details page.
3. Click the **Intercepts** tab and expand the preview URL details.
4. Click **Make Publicly Accessible**.

Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on a local environment.

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard.

## Remove a preview URL from an Intercept

To delete a preview URL and remove all access to the intercepted service,

1. Go to [Ambassador Cloud](https://app.getambassador.io/cloud/)
2. Click on the service you want to share and open the service details page.
3. Click the **Intercepts** tab and expand the preview URL details.
4. Click **Remove Preview**.

Alternatively, you can remove a preview URL with the following command:
`telepresence preview remove <intercept-name>`
