---
description: "Install Telepresence and learn to use it to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

import Alert from '@material-ui/lab/Alert';
import QSCards from './qs-cards';
import { DownloadDemo } from '../../../../../src/components/Docs/DownloadDemo';
import { UserInterceptCommand } from '../../../../../src/components/Docs/Telepresence';

# Telepresence Quick Start - React

<div class="docs-article-toc">
<h3>Contents</h3>

* [1. Download the demo cluster archive](#1-download-the-demo-cluster-archive)
* [2. Test Telepresence](#2-test-telepresence)
* [3. Set up the sample application](#3-set-up-the-sample-application)
* [4. Test app](#4-test-app)
* [5. Run a service on your laptop](#5-run-a-service-on-your-laptop)
* [6. Make a code change](#6-make-a-code-change)
* [7. Intercept all traffic to the service](#7-intercept-all-traffic-to-the-service)
* [What's next?](#img-classos-logo-srcimageslogopng-whats-next)

</div>

In this guide we'll give you **everything you need in a preconfigured demo cluster:** the Telepresence CLI, a config file for connecting to your demo cluster, and code to run a cluster service locally.

<Alert severity="info">
    While Telepresence works with any language, this guide uses a sample app with a frontend written in React. We have a version with a <a href="../demo-node/">Node.js backend</a> if you prefer.
</Alert>

<!--
<Alert severity="info">
    <strong>Already have a cluster?</strong> Switch over to a <a href="../qs-node">version of this guide</a> that takes you though the same steps using your own cluster.
</Alert>
-->

## 1. Download the demo cluster archive

1. <DownloadDemo />

2.  Extract the archive file, open the `ambassador-demo-cluster` folder, and run the installer script (the commands below might vary based on where your browser saves downloaded files).

  <Alert severity="info">
    This step will also install some dependency packages onto your laptop using npm, you can see those packages at <code>ambassador-demo-cluster/edgey-corp-nodejs/DataProcessingService/package.json</code>.
  </Alert>

  ```
  cd ~/Downloads
  unzip ambassador-demo-cluster.zip -d ambassador-demo-cluster
  cd ambassador-demo-cluster
  ./install.sh
  # type y to install the npm dependencies when asked
  ```

3. Confirm that your `kubectl` is configured to use the demo cluster by getting the status of the cluster nodes, you should see a single node named `tpdemo-prod-...`:
  `kubectl get nodes`

  ```
   $ kubectl get nodes

    NAME               STATUS   ROLES                  AGE     VERSION
    tpdemo-prod-1234   Ready    control-plane,master   5d10h   v1.20.2+k3s1
  ```

4. Confirm that the Telepresence CLI is now installed (we expect to see the daemons are not running yet):
`telepresence status`

  ```
  $ telepresence status

    Root Daemon: Not running
    User Daemon: Not running
  ```

  <Alert severity="info">
    <strong>macOS users:</strong> If you receive an error when running Telepresence that the developer cannot be verified, open <strong>System Preferences → Security & Privacy → General</strong>. Click <strong>Open Anyway</strong> at the bottom to bypass the security block. Then retry the <code>telepresence status</code> command.
  </Alert>

<Alert severity="success">
    You now have Telepresence installed on your workstation and a Kubernetes cluster configured in your terminal!
</Alert>

## 2. Test Telepresence

Telepresence connects your local workstation to a remote Kubernetes cluster.

1. Connect to the cluster (this requires **root** privileges and will ask for your password):
`telepresence connect`

  ```
  $ telepresence connect

    Launching Telepresence Daemon
    ...
    Connected to context default (https://<cluster-public-IP>)
  ```

2. Test that Telepresence is working properly by connecting to the Kubernetes API server:
`curl -ik https://kubernetes.default`

  ```
  $ curl -ik https://kubernetes.default

    HTTP/1.1 401 Unauthorized
    Cache-Control: no-cache, private
    Content-Type: application/json
    ...

  ```

  <Alert severity="info">
    The 401 response is expected.  What's important is that you were able to contact the API.
  </Alert>

<Alert severity="success">
    <strong>Congratulations!</strong> You’ve just accessed your remote Kubernetes API server, as if you were on the same network! With Telepresence, you’re able to use any tool that you have locally to connect to any service in the cluster.
</Alert>

## 3. Set up the sample application

Your local workstation may not have the compute or memory resources necessary to run all the services in a multi-service application. In this example, we’ll show you how Telepresence can give you a fast development loop, even in this situation.

<!--
We'll use a sample app that is already installed in your demo cluster.  Let's take a quick look at it's architecture before continuing.
-->

1. Clone the emojivoto app:
`git clone https://github.com/datawire/emojivoto.git`

1. Deploy the app to your cluster:
`kubectl apply -k emojivoto/kustomize/deployment`

1. Change the kubectl namespace:
`kubectl config set-context --current --namespace=emojivoto`

1. List the Services:
`kubectl get svc`

  ```
  $ kubectl get svc

    NAME         TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)             AGE
    emoji-svc    ClusterIP   10.43.162.236   <none>        8080/TCP,8801/TCP   29s
    voting-svc   ClusterIP   10.43.51.201    <none>        8080/TCP,8801/TCP   29s
    web-app      ClusterIP   10.43.242.240   <none>        80/TCP              29s
    web-svc      ClusterIP   10.43.182.119   <none>        8080/TCP            29s
  ```

1. Since you’ve already connected Telepresence to your cluster, you can access the frontend service in your browser at [http://web-app.emojivoto](http://web-app.emojivoto).  This is the namespace qualified DNS name in the form of `service.namespace`.

<Alert severity="success">
  <strong>Congratulations</strong>, you can now access services running in your cluster by name from your laptop!
</Alert>

## 4. Test app

1. Vote for some emojis and see how the [leaderboard](http://web-app.emojivoto/leaderboard) changes.

1. There is one emoji that causes an error when you vote for it. Vote for 🍩 and the leaderboard does not actually update. Also an error is shown on the browser dev console:
`GET http://web-svc.emojivoto:8080/api/vote?choice=:doughnut: 500 (Internal Server Error)`

<Alert severity="info">
    Open the dev console in <strong>Chrome or Firefox</strong> with Option + ⌘ + J (macOS) or Shift + CTRL + J (Windows/Linux).<br/>
    Open the dev console in <strong>Safari</strong> with Option + ⌘ + C.
</Alert>

The error is on a backend service, so **we can add an error page to notify the user** while the bug is fixed.

## 5. Run a service on your laptop

Now start up the `web-app` service on your laptop. We'll then make a code change and intercept this service so that we can see the immediate results of a code change to the service.

1. **In a <u>new</u> terminal window**, change into the repo directory and build the application:

  `cd <cloned repo location>/emojivoto`
  `make web-app-local`

  ```
  $ make web-app-local

    ...
    webpack 5.34.0 compiled successfully in 4326 ms
    ✨  Done in 5.38s.
  ```

2. Change into the service's code directory and start the server:

  `cd emojivoto-web-app`
  `yarn webpack serve`

  ```
  $ yarn webpack serve

    ...
    ℹ ｢wds｣: Project is running at http://localhost:8080/
    ...
    ℹ ｢wdm｣: Compiled successfully.
  ```

4. Access the application at [http://localhost:8080](http://localhost:8080) and see how voting for the 🍩 is generating the same error as the application deployed in the cluster.

<Alert severity="success">
    <strong>Victory</strong>, your local React server is running a-ok!
</Alert>

## 6. Make a code change
We’ve now set up a local development environment for the app.  Next we'll make and locally test a code change to the app to improve the issue with voting for 🍩.

1. In the terminal running webpack, stop the server with `Ctrl+c`.

1. In your preferred editor open the file `emojivoto/emojivoto-web-app/js/components/Vote.jsx` and replace the `render()` function (lines 83 to the end) with [this highlighted code snippet](https://github.com/datawire/emojivoto/blob/main/assets/Vote-fixed.jsx#L83-L149).

1. Run webpack to fully recompile the code then start the server again:

  `yarn webpack`
  `yarn webpack serve`

1. Reload the browser tab showing [http://localhost:8080](http://localhost:8080) and vote for 🍩. Notice how you see an error instead, improving the user experience.

## 7. Intercept all traffic to the service
Next, we’ll create an intercept. An intercept is a rule that tells Telepresence where to send traffic. In this example, we will send all traffic destined for the app to the version running locally instead.

  <Alert severity="warning">
    This command <strong>must</strong> be run in the terminal window where you ran the script because the script set environment variables to access the demo cluster.  Those variables will only will apply to that terminal session.
  </Alert>

1. Start the intercept with the `intercept` command, setting the workload name (a Deployment in this case), namespace, and port:
`telepresence intercept web-app --namespace emojivoto --port 8080`

  ```
  $ telepresence intercept web-app --namespace emojivoto --port 8080

    Using deployment web-app
    intercepted
        Intercept name: web-app-emojivoto
        State         : ACTIVE
    ...
  ```

2. Go to the frontend service again in your browser at [http://web-app.emojivoto](http://web-app.emojivoto). Voting for 🍩 should now show an error message to the user.

<Alert severity="success">
    The <code>web-app</code> Deployment is being <strong>intercepted and rerouted</strong> to the server on your laptop!
</Alert>

<Alert severity="success">
  We’ve just shown how we can edit code locally, and <strong>immediately</strong> see these changes in the cluster.
  <br />
  Normally, this process would require a container build, push to registry, and deploy.
  <br />
  With Telepresence, these changes happen instantly.
</Alert>

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

<QSCards/>
