---
description: "Install Telepresence and learn to use it to intercept services running in your Kubernetes cluster, speeding up local development and debugging."
---

import Alert from '@material-ui/lab/Alert';
import QSTabs from './qs-tabs'
import QSCards from './qs-cards'

# Telepresence Quick Start

<div class="docs-article-toc">
<h3>Contents</h3>

* [1. Download the demo cluster archive](#1-download-the-demo-cluster-archive)
* [2. Test Telepresence](#2-test-telepresence)
* [3. Check out the sample application](#3-check-out-the-sample-application)
* [4. Run a service on your laptop](#4-run-a-service-on-your-laptop)
* [5. Intercept all traffic to the service](#5-intercept-all-traffic-to-the-service)
* [6. Make a code change](#6-make-a-code-change)
* [7. Create a preview URL](#7-create-a-preview-url)
* [What's next?](#img-classos-logo-srcimageslogopng-whats-next)

</div>

In this guide we'll give you **everything you need in a preconfigured demo cluster:** the Telepresence CLI, a config file for connecting to your demo cluster, and code to run a cluster service locally. 

<Alert severity="info">
    While Telepresence works with any language, this guide uses a sample app written in Node.js. We have a version in <a href="../demo-react/">React</a> if you prefer.
</Alert>

<Alert severity="info">
    <strong>Already have a cluster?</strong> Switch over to a <a href="../qs-node">version of this guide</a> that takes you though the same steps using your own cluster.
</Alert>

## 1. Download the demo cluster archive

1. <a href="https://app.getambassador.io/cloud/demo-cluster-download-popup" onClick={(e) => {window.open('https://app.getambassador.io/cloud/demo-cluster-download-popup', 'ambassador-cloud-demo-cluster', 'menubar=no,location=no,resizable=yes,scrollbars=yes,status=no,width=550,height=750'); e.preventDefault(); }} target="_blank">Sign in to Ambassador Cloud to download your demo cluster archive.</a>  The archive contains all the tools and configurations you need to complete this guide.

2.  Extract the archive file, open the `ambassador-demo-cluster` folder, and run the installer script (the commands below might vary based on where your browser saves downloaded files).

  <Alert severity="info">
    This step will also install some dependency packages onto your laptop using npm, you can see those packages at <code>ambassador-demo-cluster/edgey-corp-nodejs/DataProcessingService/package.json</code>.
  </Alert>

  ```
  cd ~/Downloads
  unzip ambassador-demo-cluster.zip -d ambassador-demo-cluster
  cd ambassador-demo-cluster
  ./install.sh
  ```
 
3. The demo cluster we provided already has a demo app running. List the app's services:  
  `kubectl get services`

  ```
   $ kubectl get services
    
    NAME                    TYPE        CLUSTER-IP      EXTERNAL-IP   PORT(S)    AGE
    kubernetes              ClusterIP   10.43.0.1       <none>        443/TCP    14h
    dataprocessingservice   ClusterIP   10.43.159.239   <none>        3000/TCP   14h
    verylargejavaservice    ClusterIP   10.43.223.61    <none>        8080/TCP   14h
    verylargedatastore      ClusterIP   10.43.203.19    <none>        8080/TCP   14h
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
    You now have Telepresence installed on your workstation and a Kubernetes cluster configured in your terminal.
</Alert>

## 2. Test Telepresence

Telepresence connects your local workstation to a remote Kubernetes cluster.

1. Connect to the cluster (this requires root privileges and will ask for your password):  
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

## 3. Check out the sample application

Your local workstation may not have the compute or memory resources necessary to run all the services in a multi-service application. In this example, we’ll show you how Telepresence can give you a fast development loop, even in this situation.

We'll use a sample app that is already installed in your demo cluster.  Let's take a quick look at it's architecture before continuing.

1. Use `kubectl get pods` to check the status of your pods:

  ```
  $ kubectl get pods
    
    NAME                                         READY   STATUS    RESTARTS   AGE
    verylargedatastore-855c8b8789-z8nhs          1/1     Running   0          78s
    verylargejavaservice-7dfddbc95c-696br        1/1     Running   0          78s
    dataprocessingservice-5f6bfdcf7b-qvd27       1/1     Running   0          79s
  ```

2. Since you’ve already connected Telepresence to your cluster, you can access the frontend service in your browser at http://verylargejavaservice.default:8080.

3. You should see the EdgyCorp WebApp with a <strong style="color:green">green</strong> title and <strong style="color:green">green</strong> pod in the diagram.

<Alert severity="success">
  <strong>Congratulations</strong>, you can now access services running in your cluster by name from your laptop!
</Alert>

## 4. Run a service on your laptop

Now start up the DataProcessingService service on your laptop. This version of the code has the UI color set to <strong style="color:blue">blue</strong> instead of <strong style="color:green">green</strong>.

1. **In a <u>new</u> terminal window**, go the demo application directory in the extracted archive folder:
  `cd edgey-corp-nodejs/DataProcessingService`

2. Start the application:
  `npm start`

  ```
  $ npm start
    
    ...
    Welcome to the DataProcessingService!
    { _: [] }
    Server running on port 3000
  ```

4. **Back in your <u>previous</u> terminal window**, curl the service running locally to confirm it’s set to <strong style="color:blue">blue</strong>:  
`curl localhost:3000/color`

  ```
  $ curl localhost:3000/color
    
    "blue"
  ```

<Alert severity="success">
    <strong>Victory</strong>, your local Node server is running a-ok!
</Alert>

## 5. Intercept all traffic to the service
Next, we’ll create an intercept. An intercept is a rule that tells Telepresence where to send traffic. In this example, we will send all traffic destined for the DataProcessingService to the version of the DataProcessingService running locally instead:

1. Start the intercept with the `intercept` command, setting the service name and port:  
`telepresence intercept dataprocessingservice --port 3000`

  <Alert severity="info">
    <strong>Didn't work?</strong> Make sure you are working in the terminal window where you ran the script because it sets environment variables to access the demo cluster.  Those variables will only will apply to that terminal session.
  </Alert>

  ```
  $ telepresence intercept dataprocessingservice --port 3000
    
    Using deployment dataprocessingservice
    intercepted
        Intercept name: dataprocessingservice
        State         : ACTIVE
    ...
  ```

2. Go to the frontend service again in your browser at [http://verylargejavaservice:8080](http://verylargejavaservice:8080). You will now see the <strong style="color:blue">blue</strong> elements in the app.

<Alert severity="success">
    The frontend’s request to DataProcessingService is being <strong>intercepted and rerouted</strong> to the Node server on your laptop!
</Alert>

## 6. Make a code change
We’ve now set up a local development environment for the DataProcessingService, and we’ve created an intercept that sends traffic in the cluster to our local environment. We can now combine these two concepts to show how we can quickly make and test changes.

1. Open `edgey-corp-nodejs/DataProcessingService/app.js` in your editor and change line 6 from `blue` to `orange`. Save the file and the Node server will auto reload.

2. Now visit [http://verylargejavaservice:8080](http://verylargejavaservice:8080) again in your browser. You will now see the <strong style="color:orange">orange</strong> elements in the application. The frontend `verylargejavaservice` is still running on the cluster, but it's request to the `DataProcessingService` for retrieve the color to show is being proxied by Telepresence to your laptop.

<Alert severity="success">
  We’ve just shown how we can edit code locally, and <strong>immediately</strong> see these changes in the cluster.
  <br />
  Normally, this process would require a container build, push to registry, and deploy.
  <br />
  With Telepresence, these changes happen instantly.
</Alert>

## 7. Create a Preview URL
Create preview URLs to do selective intercepts, meaning only traffic coming from the preview URL will be intercepted, so you can easily share the services you’re working on with your teammates.

1. Clean up your previous intercept by removing it:  
`telepresence leave dataprocessingservice`

2. Login to Ambassador Cloud, a web interface for managing and sharing preview URLs:
`telepresence login`

  This opens your browser; login with your preferred identity provider and choose your org.

  ```
  $ telepresence login
    Launching browser authentication flow...
    <browser opens, login>
    Login successful.
  ```

3. Start the intercept again:  
`telepresence intercept dataprocessingservice --port 3000`

   You will be asked for your ingress layer 3 address; specify the front end service: `verylargejavaservice.default`
   Then when asked for the port, type `8080`, for "use TLS", type `n`.  The default for the fourth value is correct so hit enter to accept it

  ```
  $ telepresence intercept dataprocessingservice --port 3000
    
    To create a preview URL, telepresence needs to know how cluster
    ingress works for this service.  Please Select the ingress to use.
    
    1/4: What's your ingress' layer 3 (IP) address?
         You may use an IP address or a DNS name (this is usually a
         "service.namespace" DNS name).
    
           [no default]: verylargejavaservice.default
    
    2/4: What's your ingress' layer 4 address (TCP port number)?
    
           [no default]: 8080
    
    3/4: Does that TCP port on your ingress use TLS (as opposed to cleartext)?
    
           [default: n]: n
    
    4/4: If required by your ingress, specify a different layer 5 hostname
         (TLS-SNI, HTTP "Host" header) to access this service.
    
           [default: verylargejavaservice.default]:
    
    Using deployment dataprocessingservice
    intercepted
        Intercept name  : dataprocessingservice
        State           : ACTIVE
        Destination     : 127.0.0.1:3000
        Intercepting    : HTTP requests that match all of:
          header("x-telepresence-intercept-id") ~= regexp("86cb4a70-c7e1-1138-89c2-d8fed7a46cae:dataprocessingservice")
        Preview URL     : https://<random-subdomain>.preview.edgestack.me
        Layer 5 Hostname: verylargejavaservice.default
  ```

4. Wait a moment for the intercept to start; it will also output a preview URL.  Go to this URL in your browser, it will be the <strong style="color:orange">orange</strong> version of the app.

5. Now go again to [http://verylargejavaservice:8080](http://verylargejavaservice:8080), it’s still <strong style="color:green">green</strong>.

Normal traffic coming to your app gets the <strong style="color:green">green</strong> cluster service, but traffic coming from the preview URL goes to your laptop and gets the <strong style="color:orange">orange</strong> local service!

<Alert severity="success">
  The <strong>Preview URL</strong> now shows exactly what is running on your local laptop -- in a way that can be securely shared with anyone you work with.
</Alert>

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

<QSCards/>
