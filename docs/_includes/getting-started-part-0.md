## A short introduction: accessing the cluster

1. Install Telepresence (see below).
2. Run a service in the cluster:

   ```console
   $ {{ include.command }} run myservice --image=datawire/hello-world --port=8000 --expose
   $ {{ include.command }} get service myservice
   NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
   myservice   10.0.0.12    <none>        8000/TCP   1m
   ```

   It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.
   
3. You can now run a local process using Telepresence that can access that service, even though the process is local but the service is running in the {{ include.cluster }} cluster:

   ```console
   $ telepresence -m inject-tcp --new-deployment example --run curl http://myservice:8000/
   Hello, world!
   ```

   (This will not work if the hello world pod hasn't started yet... if so, try again.)

`curl` got access to the cluster even though it's running locally!
In the more extended tutorial that follows you'll see how you can also route traffic *to* a local process from the cluster.

## A longer introduction: exposing a service to the cluster
