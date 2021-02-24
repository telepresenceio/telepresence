import Alert from '@material-ui/lab/Alert';
import QSTabs from './qs-tabs'
import QSCards from './qs-cards'

# Telepresence Quick Start - Python using FastAPI

<Alert severity="info">While Telepresence works with any language, this guide uses a sample app written in Python using the FastAPI framework. We have versions in <a href="../qs-go/">Go</a>, <a href="../">Node</a>, and <a href="../qs-python/">Python using Flask</a> if you prefer.</Alert>

## Prerequisites
You’ll need `kubectl` installed and configured to use a Kubernetes cluster, preferably an empty test cluster.  You must have RBAC permissions in the cluster to create and update deployments and services.

If you have used Telepresence previously, please first reset your Telepresence deployment with:
`telepresence uninstall --everything`.

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
    Connected to context default (https://<cluster-public-IP>)
  ```

  <Alert severity="info"> macOS users: If you receive an error when running Telepresence that the developer cannot be verified, open <b>System Preferences → Security & Privacy → General</b>. Click <b>Open Anyway</b> at the bottom to bypass the security block. Then retry the <code>telepresence connect</code> command.</Alert>

2. Test that Telepresence is working properly by connecting to the Kubernetes API server:  
`curl -ik https://kubernetes.default`

  ```
  $ curl -ik https://kubernetes.default
    
    HTTP/1.1 401 Unauthorized
    Cache-Control: no-cache, private
    Content-Type: application/json
    Www-Authenticate: Basic realm="kubernetes-master"
    Date: Tue, 09 Feb 2021 23:21:51 GMT
    Content-Length: 165  
    
    ...

  ```
<Alert severity="info">The 401 response is expected.  What's important is that you were able to contact the API.</Alert>

<Alert severity="success"><b>Congratulations! You’ve just accessed your remote Kubernetes API server, as if you were on the same network!</b> With Telepresence, you’re able to use any tool that you have locally to connect to any service in the cluster.</Alert>

## 3. Install a sample FastAPI application

Your local workstation may not have the compute or memory resources necessary to run all the services in a multi-service application. In this example, we’ll show you how Telepresence can give you a fast development loop, even in this situation.

<Alert severity="info">While Telepresence works with any language, this guide uses a sample app written in Python using the FastAPI framework. We have versions in <a href="../qs-go/">Go</a>, <a href="../">Node</a>, and <a href="../qs-python/">Python with Flask</a> if you prefer.</Alert>

1. Start by installing a sample application that consists of multiple services:  
`kubectl apply -f https://raw.githubusercontent.com/datawire/edgey-corp-python-fastapi/main/k8s-config/edgey-corp-web-app-no-mapping.yaml`

  ```
  $ kubectl apply -f https://raw.githubusercontent.com/datawire/edgey-corp-python-fastapi/main/k8s-config/edgey-corp-web-app-no-mapping.yaml
    
    deployment.apps/dataprocessingservice created
    service/dataprocessingservice created
    ...  

  ```

2. Give your cluster a few moments to deploy the sample application.

  Use `kubectl get pods` to check the status of your pods:  

  ```
  $ kubectl get pods
    
    NAME                                         READY   STATUS    RESTARTS   AGE
    verylargedatastore-855c8b8789-z8nhs          1/1     Running   0          78s
    verylargejavaservice-7dfddbc95c-696br        1/1     Running   0          78s
    dataprocessingservice-5f6bfdcf7b-qvd27       1/1     Running   0          79s
  ```

3. Once all the pods are in a `Running` state, go to the frontend service in your browser at [http://verylargejavaservice.default:8080](http://verylargejavaservice.default:8080).

4. You should see the EdgyCorp WebApp with a <span style="color:green" class="bold">green</span> title and <span style="color:green" class="bold">green</span> pod in the diagram.

<Alert severity="success"><b>Congratulations, you can now access services running in your cluster by name from your laptop!</b></Alert>

## 4. Set up a local development environment
You will now download the repo containing the services' code and run the DataProcessingService service locally. This version of the code has the UI color set to <span style="color:blue" class="bold">blue</span> instead of <span style="color:green" class="bold">green</span>.

<Alert severity="info">Confirm first that nothing is running locally on port 3000! If <code>curl localhost:3000</code> returns <code>Connection refused</code> then you should be good to go.</Alert>

1. Clone the web app’s GitHub repo:  
`git clone https://github.com/datawire/edgey-corp-python-fastapi.git`

  ```
  $ git clone https://github.com/datawire/edgey-corp-python-fastapi.git
    
    Cloning into 'edgey-corp-python-fastapi'...
    remote: Enumerating objects: 441, done.
    ...
  ```

2. Change into the repo directory, then into DataProcessingService:  
`cd edgey-corp-python-fastapi/DataProcessingService/`

3. Install the dependencies and start the Python server.  You may need to use `pip3` and `python3` if you have Python 3 installed.  
`pip install fastapi uvicorn requests && python app.py`

  ```
  $ pip install fastapi uvicorn requests && python app.py
    
    Collecting fastapi
    ...
    Application startup complete.

  ```

  <Alert severity="info"><a href="https://www.python.org/downloads/">Install Python from here</a> if needed.</Alert>

4. In a **new terminal window**, curl the service running locally to confirm it’s set to <span style="color:blue" class="bold">blue</span>:  
`curl localhost:3000/color`

  ```
  $ curl localhost:3000/color
    
    “blue”
  ```

<Alert severity="success"><b>Victory, your local service is running a-ok!</b></Alert>

## 5. Intercept all traffic to the service
Next, we’ll create an intercept. An intercept is a rule that tells Telepresence where to send traffic. In this example, we will send all traffic destined for the DataProcessingService to the version of the DataProcessingService running locally instead: 

1. Start the intercept with the `intercept` command, setting the service name and port:  
`telepresence intercept dataprocessingservice --port 3000`

  ```
  $ telepresence intercept dataprocessingservice --port 3000
    
    Using deployment dataprocessingservice
    intercepted
        State       : ACTIVE
        Destination : 127.0.0.1:3000
        Intercepting: all connections
  ```

2. Go to the frontend service again in your browser. Since the service is now intercepted it can be reached directly by its service name at [http://verylargejavaservice:8080](http://verylargejavaservice:8080). You will now see the <span style="color:blue" class="bold">blue</span> elements in the app.

<Alert severity="success"><b>The frontend’s request to DataProcessingService is being intercepted and rerouted to the Python FastAPI server on your laptop!</b></Alert>

## 6. Make a code change
We’ve now set up a local development environment for the DataProcessingService, and we’ve created an intercept that sends traffic in the cluster to our local environment. We can now combine these two concepts to show how we can quickly make and test changes.

1. Open `edgey-corp-python-fastapi/DataProcessingService/app.py` in your editor and change `DEFAULT_COLOR` on line 17 from `blue` to `orange`. Save the file and the Python server will auto reload.

2. Now, visit [http://verylargejavaservice:8080](http://verylargejavaservice:8080) again in your browser. You will now see the <span style="color:orange">orange</span> elements in the application.

<Alert severity="success"><b>We’ve just shown how we can edit code locally, and immediately see these changes in the cluster.</b> Normally, this process would require a container build, push to registry, and deploy. With Telepresence, these changes happen instantly.</Alert>

## 7. Create a Preview URL
Create preview URLs to do selective intercepts, meaning only traffic coming from the preview URL will be intercepted, so you can easily share the services you’re working on with your teammates.

1. Clean up your previous intercept by removing it:  
`telepresence leave dataprocessingservice`

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
`telepresence intercept dataprocessingservice --port 3000`  
  You will be asked for your ingress; specify the front end service: `verylargejavaservice.default`  
  Then when asked for the port, type `8080`.  
  Finally, type `n` for “Use TLS”.

  ```
    $ telepresence intercept dataprocessingservice --port 3000
      
      Confirm the ingress to use for preview URL access
      Ingress service.namespace ? verylargejavaservice.default
      Port ? 8080
      Use TLS y/n ? n
      Using deployment dataprocessingservice
      intercepted
          State       : ACTIVE
          Destination : 127.0.0.1:3000
          Intercepting: HTTP requests that match all of:
            header("x-telepresence-intercept-id") ~= regexp("86cb4a70-c7e1-1138-89c2-d8fed7a46cae:dataprocessingservice")
          Preview URL : https://<random-subdomain>.preview.edgestack.me  
  ```

4. Wait a moment for the intercept to start; it will also output a preview URL.  Go to this URL in your browser, it will be the <span style="color:orange" class="bold">orange</span> version of the app.

5. Go again to [http://verylargejavaservice:8080](http://verylargejavaservice:8080) and it’s still <span style="color:green" class="bold">green</span>.

Normal traffic coming to your app gets the <span style="color:green" class="bold">green</span> cluster service, but traffic coming from the preview URL goes to your laptop and gets the <span style="color:orange" class="bold">orange</span> local service!

<Alert severity="success"><b>The Preview URL now shows exactly what is running on your local laptop -- in a way that can be securely shared with anyone you work with.</b></Alert>

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

<QSCards/>
