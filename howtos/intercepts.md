---
description: "Telepresence help you develop Kubernetes services locally without running dependent services or redeploying code updates to your cluster on every change."
---

import Alert from '@material-ui/lab/Alert';
import QSTabs from '../../quick-start/qs-tabs'

# Intercept a Service

Intercepts enable you to test and debug services locally without needing to run dependent services or redeploy code updates to your cluster on every change.  A typical workflow would be to run the service you wish to develop on locally, then start an intercept. Changes to the local code can then be tested immediately along side other services running in the cluster.

When starting an intercept, Telepresence will create a preview URLs. When visiting the preview URL, your request is proxied to your ingress with a special header set.  When the traffic within the cluster requests the service you are intercepting, the [Traffic Manager](../../reference) will proxy that traffic to your laptop.  Other traffic  entering your ingress will use the service running in the cluster as normal.

Preview URLs are all managed through Ambassador Cloud.  You must run `telepresence login` to access Ambassador Cloud and access the preview URL dashboard. From the dashboard you can see all your active intercepts, delete active intercepts, and change them between private and public for collaboration. Private preview URLs can be accessed by anyone else in the GitHub organization you select when logging in. Public URLs can be accessed by anyone who has the link.

While preview URLs selectively proxy traffic to your laptop, you can also run an [intercept without creating a preview URL](#creating-an-intercept-without-a-preview-url), which will proxy all traffic to the service.

<Alert severity="info">For a detailed walk though on creating intercepts using our sample app, follow the <a href="../../quick-start/qs-node/">quick start guide</a>.</Alert>

## Creating an Intercept

The following quick overview on creating an intercept assumes you have a deployment and service accessible publicly by an ingress controller and that you can run a copy of that service on your laptop.  

1. Install Telepresence if needed.

<QSTabs/>

1. In your terminal run `telepresence login`. This logs you into the Ambassador Cloud, which will track your intercepts and let you share them with colleagues. 

  <Alert severity="info">If you are logged in and close the dashboard browser tab, you can quickly reopen it by running <code>telepresence dashboard</code>.</Alert>

2. Return to your terminal and run `telepresence list`.  This will connect to your cluster, install the [Traffic Manager](../../reference/) to proxy the traffic, and return a list of services that Telepresence is able to intercept.

3. Start the service on your laptop and make a change to the code that will be apparent in the browser when the service runs, such as a text or other UI change.

4. In a new terminal window start the intercept. This will proxy requests to the cluster service to your laptop.  It will also generate a preview URL, which will let you access your service from the ingress but with requests to the intercepted service proxied to your laptop.

   The intercept requires you specify the name of the deployment to be
   intercepted and the port on your laptop to proxy to.

   ```
   telepresence intercept ${base_name_of_intercept} --port=${local_TCP_port}
   ```

   The name of the Deployment to be intercepted will default to the
   base name of the intercept that you give, but you can specify a
   different deployment name using the `--deployment` flag:

   ```
   telepresence intercept ${base_name_of_intercept} --deployment=${name_of_deployment} --port=${local_TCP_port}
   ```

   Because you're logged in (from `telepresence login` in step 2), it
   will default to `--preview-url=true`, which will use Ambassador
   Cloud to create a sharable preview URL for this intercept; if you
   hadn't been logged in it would have defaulted to
   `--preview-url=false`.  In order to do this, it will prompt you for
   three options.  For the first, `Ingress`, Telepresence tries to
   intelligently determine the ingress controller deployment and
   namespace for you.  If they are correct, you can hit `enter` to
   accept the defaults.  Set the next two options, `TLS` and `Port`,
   appropriately based on your ingress service.

   Also because you're logged in, it will default to `--mechanism=http
   --http-match=auto` (or just `--http-match=auto`; `--http-match`
   implies `--mechanism=http`); if you hadn't been logged in it would
   have defaulted to `--mechanism=tcp`.  This tells it to do smart
   intercepts and only intercept a subset of HTTP requests, rather
   than just intercepting the entirety of all TCP connections.  This
   is important for working in a shared cluster with teammates, and is
   important for the preview URL functionality.  See `telepresence
   intercept --help` for information on using `--http-match` to
   customize which requests it intercepts.

5. Open the preview URL in your browser. The page that loads will proxy requests to the intercepted service to your laptop. You will also see a banner at the bottom on the page informing that you are viewing a preview URL with your name and org name.

6. Switch back in your browser to the Ambassador Cloud dashboard page and refresh it to see your preview URL listed. Click the box to expand out options where you can disable authentication or remove the preview.
  
7. Stop the intercept with the `leave` command and `quit` to stop the daemon.  Finally, use `uninstall --everything` to remove the Traffic Manager and Agents from your cluster.

   ```
   telepresence leave ${full_name_of_intercept}
   telepresence quit
   telepresence uninstall --everything
   ```

   The resulting intercept might have a full name that is different
   than the base name that you gave to `telepresence intercept` in
   step 4; see the section [Specifing a namespace for an
   intercept](#specifying-a-namespace-for-an-intercept) for more
   information.

## Specifying a namespace for an intercept

The namespace of the intercepted deployment is specified using the `--namespace` option. When this option is used, and `--deployment` is not used, then the given name is interpreted as the name of the deployment and the name of the intercept will be constructed from that name and the namespace.

  ```
  telepresence intercept hello --namespace myns --port 9000
  ```

This will intercept a Deployment named "hello" and name the intercept
"hello-myns".  In order to remove the intercept, you will need to run
`telepresence leave hello-mydns` instead of just `telepresence leave
hello`.

The name of the intercept will be left unchanged if the deployment is specified.

  ```
  telepresence intercept myhello --namespace myns --deployment hello --port 9000
  ```
This will intercept a deployment named "hello" and name the intercept "myhello".

## Importing Environment Variables

Telepresence can import the environment variables from the pod that is being intercepted, see [this doc](../../reference/environment/) for more details.

## Creating an Intercept Without a Preview URL

If you *are not* logged into Ambassador Cloud, the following command will intercept all traffic bound to the service and proxy it to your laptop. This includes traffic coming through your  ingress controller, so use this option carefully as to not disrupt production environments.

```
telepresence intercept ${base_name_of_intercept} --port=${local_TCP_port}
```

If you *are* logged into Ambassador Cloud, setting the `preview-url` flag to `false` is necessary.

```
telepresence intercept ${base_name_of_intercept} --port=${local_TCP_port} --preview-url=false
```

This will output a header that you can set on your request for that traffic to be intercepted:

```
$ telepresence intercept <base name of intercept> --port=<local TCP port> --preview-url=false
Using deployment <name of deployment>
intercepted
    Intercept name: <full name of intercept>
    State         : ACTIVE
    Destination   : 127.0.0.1:<local TCP port>
    Intercepting  : HTTP requests that match all of:
      header("x-telepresence-intercept-id") ~= regexp("<uuid unique to you>:<full name of intercept>")
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

Finally, run `telepresence leave [name of intercept]` to stop the intercept.
