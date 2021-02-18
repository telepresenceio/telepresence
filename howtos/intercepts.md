---
description: "Telepresence help you develop Kubernetes services locally without running dependent services or redeploying code updates to your cluster on every change."
---

import Alert from '@material-ui/lab/Alert';

# Intercepts

Intercepts enable you to test and debug services locally without needing to run dependent services or redeploy code updates to your cluster on every change.  A typical workflow would be to run the service you wish to develop on locally, then start an intercept. Changes to the local code can then be tested immediately along side other services running in the cluster.

When starting an intercept, Telepresence will create a preview URLs. When visiting the preview URL, your request is proxied to your ingress with a special header set.  When the traffic within the cluster requests the service you are intercepting, the [Traffic Manager](../../reference) will proxy that traffic to your laptop.  Other traffic  entering your ingress will use the service running in the cluster as normal.

Preview URLs are all managed through Ambassador Cloud.  You must run `telepresence login` to access Ambassador Cloud and access the preview URL dashboard. From the dashboard you can see all your active intercepts, delete active intercepts, and change them between private and public for collaboration. Private preview URLs can be accessed by anyone else in the GitHub organization you select when logging in. Public URLs can be accessed by anyone who has the link.

While preview URLs selectively proxy traffic to your laptop, you can also run an [intercept without creating a preview URL](#creating-an-intercept-without-a-preview-url), which will proxy all traffic to the service.

<Alert severity="info">For a detailed walk though on creating intercepts, follow the <a href="../../tutorial/">Telepresence tutorial</a>.</Alert>

## Creating an Intercept

The following quick overview on creating an intercept assumes you have a deployment and service accessible publicly by an ingress controller and that you can run a copy of that service on your laptop.  

1. In your terminal run `telepresence login`. This logs you into the Ambassador Cloud, which will track your intercepts and let you share them with colleagues. 

  <Alert severity="info">If you are logged in and close the dashboard browser tab, quickly reopen it by running <code>telepresence dashboard</code>.</Alert>

2. Return to your terminal and run `telepresence list`.  This will connect to your cluster, install the [Traffic Manager](../../reference/) to proxy the traffic, and return a list of services that Telepresence is able to intercept.

3. Start the service on your laptop and make a change to the code that will be apparent in the browser when the service runs, such as a text or other UI change.

4. In a new terminal window start the intercept. This will proxy requests to the cluster service to your laptop.  It will also generate a preview URL, which will let you access your service from the ingress but with requests to the intercepted service proxied to your laptop.

  The intercept requires you specify the name of the deployment to be intercepted and the port to proxy. 

  ```
  telepresence intercept [name of deployment] --port [TCP port]
  ```

  You will be prompted with three options. For the first, `Ingress`, Telepresence tries to intelligently determine the ingress controller deployment and namespace for you.  If they are correct, you can hit `enter` to accept the defaults.  Set the next two options, `TLS` and `Port`, appropriately based on your service.

5. Open the preview URL in your browser. The page that loads will proxy requests to the intercepted service to your laptop. You will also see a banner at the bottom on the page informing that you are viewing a preview URL with your name and org name.

6. Switch back in your browser to the Ambassador Cloud dashboard page and refresh it to see your preview URL listed. Click the box to expand out options where you can disable authentication or remove the preview.
  
7. Clean up your environment by first typing `Ctrl+C` in the terminal running Node. Then stop the intercept with the `leave` command and `quit` to stop the daemon.  Finally, use `uninstall --everything` to remove the Traffic Manager and Agents from your cluster.

  ```
  telepresence leave [name of deployment]
  telepresence quit
  telepresence uninstall --everything
  ```

## Importing Environment Variables

Telepresence can import the environment variables from the pod that is being intercepted, see [this doc](../../reference/environment.md) for more details.

## Creating an Intercept Without a Preview URL

If you *are not* logged into Ambassador Cloud, the following command will intercept all traffic bound to the service and proxy it to your laptop. This includes traffic coming through your  ingress controller, so use this option carefully as to not disrupt production environments.

```
telepresence intercept [name of deployment] --port [TCP port] 
```

If you *are* logged into Ambassador Cloud, setting the `preview-url` flag to `false` is necessary.

```
telepresence intercept [name of deployment] --port [TCP port] --preview-url=false
```

This will output a header that you can set on your request for that traffic to be intercepted:

```
$ telepresence intercept [name of deployment] --port [TCP port] --preview-url=false
Using deployment [name of deployment]
intercepted
    State       : ACTIVE
    Destination : 127.0.0.1:[TCP port]
    Intercepting: HTTP requests that match all of:
      header("x-telepresence-intercept-id") ~= regexp("71d5e134-1163-4d17-1138-e35624c1e9419:[name of deployment]")
```

Run `telepresence status` to see the list of active intercepts.

```
$ telepresence status
Connected
  Context:       default (https://<cluster-public-ip>)
  Proxy:         ON (networking to the cluster is enabled)
  Intercepts:    1 total
    dataprocessingnodeservice: <your-laptop-name>
```

Finally, run `telepresence leave [name of deployment]` to stop the intercept.