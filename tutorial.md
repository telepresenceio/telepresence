---
description: "Install Telepresence and learn to use it to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

import Alert from '@material-ui/lab/Alert';

# Telepresence Quick Start

In this guide you will explore some of the key features of Telepresence. First, you will install the Telepresence CLI and set up a test cluster with a demo web app. Then, you will run one of the app's services on your laptop, using Telepresence to intercept requests to the service on the cluster and see your changes live via a preview URL.

## Prerequisites

It is recommended to use an empty development cluster for this guide.  You must have access via RBAC to create and update deployments and services in the cluster.  You must also have [Node.js installed](https://nodejs.org/en/download/package-manager/) on your laptop to run the demo app code.

Finally, you will need the Telepresence CLI.  Run the commands for
your OS to install it and log in to Ambassador Cloud in your browser.
Follow the prompts to log in with GitHub then select your
organization.  You will be redirected to the Ambassador Cloud
dashboard; later you will manage your preview URLs here.

### <img class="os-logo" src="../../images/apple.png"/> macOS

```shell
# Intel Macs

# Install via brew:
brew install datawire/blackbird/telepresence

# OR Install manually:
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/latest/telepresence \
-o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# 3. Login with the CLI:
telepresence login

# Apple silicon Macs

# Install via brew:
brew install datawire/blackbird/telepresence-arm64

# OR Install manually:
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/arm64/latest/telepresence \
-o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# 3. Login with the CLI:
telepresence login
```

<Alert severity="info" variant="outlined">If you receive an error saying the developer cannot be verified, open <b>System Preferences → Security & Privacy → General</b>.  Click <b>Open Anyway</b> at the bottom to bypass the security block. Then retry the <code>telepresence login</code> command.</Alert>

If you are in an environment where Telepresence cannot launch a local
browser for you to interact with, you will need to pass the
[`--apikey` flag to `telepresence
login`](../../reference/client/login/).

### <img class="os-logo" src="../../images/linux.png"/> Linux

```shell
# 1. Download the latest binary (~50 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/linux/amd64/latest/telepresence \
-o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# 3. Login with the CLI:
telepresence login
```

If you are in an environment where Telepresence cannot launch a local
browser for you to interact with, you will need to pass the
[`--apikey` flag to `telepresence
login`](../../reference/client/login/).

## Cluster Setup

1. You will use a sample Java app for this guide. Later, after deploying the app into your cluster, we will review its architecture. Start by cloning the repo:

  ```
  git clone https://github.com/datawire/amb-code-quickstart-app.git
  ```

2. Install [Edge Stack](../../../../../../products/edge-stack/) to use as an ingress controller for your cluster. We need an ingress controller to allow access to the web app from the internet.

  Change into the repo directory, then into `k8s-config`, and apply the YAML files to deploy Edge Stack.

  ```
  cd amb-code-quickstart-app/k8s-config
  kubectl apply -f 1-aes-crds.yml && kubectl wait --for condition=established --timeout=90s crd -lproduct=aes
  kubectl apply -f 2-aes.yml && kubectl wait -n ambassador deploy -lproduct=aes --for condition=available --timeout=90s
  ```

3. Install the web app by applying its manifest:

  ```
  kubectl apply -f edgy-corp-web-app.yaml
  ```

4. Wait a few moments for the external load balancer to become available, then retrieve its IP address:

  ```
  kubectl get service -n ambassador ambassador -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
  ```

<table style="border-collapse: collapse; border: none; padding: 5px; line-height: 29px">
<tr style="background:transparent; border: none; padding: 5px">
    <td style="border: none; padding: 5px; width:65%"><ol start="5"><li>Wait until all the pods start, then access the the Edgy Corp web app in your browser at <code>http://&lt;load-balancer-ip/&gt;</code>. Be sure you use <code>http</code>, not <code>https</code>! <br/>You should see the landing page for the web app with an architecture diagram. The web app is composed of three services, with the frontend <code>VeryLargeJavaService</code> dependent on the two backend services.</li></ol></td>
    <td style="border: none; padding: 5px"><img src="../../images/tp-tutorial-1.png"/></td>
</tr>
</table>

## Developing with Telepresence

Now that your app is all wired up you're ready to start doing development work with Telepresence. Imagine you are a Java developer and first on your to-do list for the day is a change on the `DataProcessingNodeService`. One thing this service does is set the color for the title and a pod in the diagram. The production version of the app on the cluster uses <span style="color:green" class="bold">green</span> elements, but you want to see a version with these elements set to <span style="color:blue" class="bold">blue</span>.

The `DataProcessingNodeService` service is dependent on the `VeryLargeJavaService` and `VeryLargeDataStore` services to run. Local development would require one of the two following setups, neither of which is ideal.

First, you could run the two dependent services on your laptop. However, as their names suggest, they are too large to run locally. This option also doesn't scale well. Two services isn't a lot to manage, but more complex apps requiring many more dependencies is not feasible to manage running on your laptop.

Second, you could run everything in a development cluster. However, the cycle of writing code then waiting on containers to build and deploy is incredibly disruptive. The lengthening of the [inner dev loop](../concepts/devloop) in this way can have a significant impact on developer productivity.

## Intercepting a Service

Alternatively, you can use Telepresence's `intercept` command to proxy traffic bound for a service to your laptop. This will let you test and debug services on code running locally without needing to run dependent services or redeploy code updates to your cluster on every change. It also will generate a preview URL, which loads your web app from the cluster ingress but with requests to the intercepted service proxied to your laptop.

1. You started this guide by installing the Telepresence CLI and
   logging in to Ambassador Cloud.  The Cloud dashboard is used to
   manage your intercepts and share them with colleagues.  You must be
   logged in to create personal intercepts as we are going to do here.

   <Alert severity="info" variant="outlined">Run <code>telepresence dashboard</code> if you are already logged in and just need to reopen the dashboard.</Alert>

2. In your terminal and run `telepresence list`.  This will connect to your cluster, install the [Traffic Manager](../reference/#architecture) to proxy the traffic, and return a list of services that Telepresence is able to intercept.

3. Navigate up one directory to the root of the repo then into `DataProcessingNodeService`. Install the Node.js dependencies and start the app passing the `blue` argument, which is used by the app to set the title and pod color in the diagram you saw earlier.

  ```
  cd ../DataProcessingNodeService
  npm install
  node app -c blue
  ```

4. In a new terminal window start the intercept with the command below. This will proxy requests to the `DataProcessingNodeService` service to your laptop.  It will also generate a preview URL, which will let you view the app with the intercepted service in your browser.

  The intercept requires you specify the name of the deployment to be intercepted and the port to proxy.

  ```
  telepresence intercept dataprocessingnodeservice --port 3000
  ```

  You will be prompted with a few options. Telepresence tries to intelligently determine the deployment and namespace of your ingress controller.  Hit `enter` to accept the default value of `ambassador.ambassador` for `Ingress`.  For simplicity's sake, our app uses 80 for the port and does *not* use TLS, so use those options when prompted for the `port` and `TLS` settings. Your output should be similar to this:

  ```
  $ telepresence intercept dataprocessingnodeservice --port 3000
  To create a preview URL, telepresence needs to know how cluster
  ingress works for this service.  Please Select the ingress to use.

  1/4: What's your ingress' layer 3 (IP) address?
       You may use an IP address or a DNS name (this is usually a
       "service.namespace" DNS name).

         [no default]: verylargejavaservice.default

  2/4: What's your ingress' layer 4 address (TCP port number)?

         [no default]: 8080

  3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?

         [default: n]:

  4/4: If required by your ingress, specify a different layer 5 hostname
       (TLS-SNI, HTTP "Host" header) to access this service.

         [default: verylargejavaservice.default]:

  Using Deployment dataprocessingservice
  intercepted
      Intercept name  : dataprocessingservice
      State           : ACTIVE
      Workload kind   : Deployment
      Destination     : 127.0.0.1:3000
      Intercepting    : HTTP requests that match all of:
        header("x-telepresence-intercept-id") ~= regexp("86cb4a70-c7e1-1138-89c2-d8fed7a46cae:dataprocessingservice")
      Preview URL     : https://<random-subdomain>.preview.edgestack.me
      Layer 5 Hostname: verylargejavaservice.default
  ```

<table style="border-collapse: collapse; border: none; padding: 5px; line-height: 29px">
<tr style="background:transparent; border: none; padding: 5px">
    <td style="border: none; padding: 5px; width:65%"><ol start="5"><li>Open the preview URL in your browser to see the intercepted version of the app. The Node server on your laptop replies back to the cluster with the <span style="color:blue" class="bold">blue</span> option enabled; you will see a blue title and blue pod in the diagram. Remember that previously these elements were <span style="color:green" class="bold">green</span>.<br />You will also see a banner at the bottom on the page informing that you are viewing a preview URL with your name and org name.</li></ol></td>
    <td style="border: none; padding: 5px"><img src="../../images/tp-tutorial-2.png"/></td>
</tr>
</table>

<table style="border-collapse: collapse; border: none; padding: 5px; line-height: 29px">
<tr style="background:transparent; border: none; padding: 5px">
    <td style="border: none; padding: 5px; width:65%"><ol start="6"><li>Switch back in your browser to the dashboard page and refresh it to see your preview URL listed. Click the box to expand out options where you can disable authentication or remove the preview.<br/>If there were other developers in your organization also creating preview URLs, you would see them here as well.</li></ol></td>
    <td style="border: none; padding: 5px"><img src="../../images/tp-tutorial-3.png"/></td>
</tr>
</table>

This diagram demonstrates the flow of requests using the intercept.  The laptop on the left visits the preview URL, the request is redirected to the cluster ingress, and requests to and from the `DataProcessingNodeService` by other pods are proxied to the developer laptop running Telepresence.

![Intercept Architecture](../../images/tp-tutorial-4.png)

7. Clean up your environment by first typing `Ctrl+C` in the terminal running Node. Then stop the intercept with the `leave` command and `quit` to stop the daemon.  Finally, use `uninstall --everything` to remove the Traffic Manager and Agents from your cluster.

  ```
  telepresence leave dataprocessingnodeservice
  telepresence quit
  telepresence uninstall --everything
  ```

8. Refresh the dashboard page again and you will see the intercept was removed after running the `leave` command.  Refresh the browser tab with the preview URL and you will see that it has been disabled.

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

Telepresence and preview URLS open up powerful possibilities for [collaborating](../howtos/preview-urls) with your colleagues and others outside of your organization.

Learn more about how Telepresence handles [outbound sessions](../howtos/outbound), allowing locally running services to interact with cluster services without an intercept.

Read the [FAQs](../faqs) to learn more about uses cases and the technical implementation of Telepresence.
