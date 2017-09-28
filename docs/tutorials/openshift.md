# Get started with OpenShift

## A short introduction: accessing the cluster

1. Install Telepresence (see below).
2. Run a service in the cluster:

   ```console
   $ oc run myservice --image=datawire/hello-world --port=8000 --expose
   $ oc get service myservice
   NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
   myservice   10.0.0.12    <none>        8000/TCP   1m
   ```

   It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.
   
3. You can now run a local process using Telepresence that can access that service, even though the process is local but the service is running in the OpenShift cluster:

   ```console
   $ telepresence --run curl http://myservice:8000/
   Hello, world!
   ```

   (This will not work if the hello world pod hasn't started yet... if so, try again.)

`curl` got access to the cluster even though it's running locally!
In the more extended tutorial that follows you'll see how you can also route traffic *to* a local process from the cluster.

## A longer introduction: exposing a service to the cluster

{% import "../macros.html" as macros %}
{{ macros.install("https://docs.openshift.org/latest/cli_reference/get_started_cli.html", "oc", "OpenShift", "top") }}

{{ macros.gettingStartedPart1("OpenShift")}}

You should start a new application and publicly expose it:

```console
$ oc new-app --docker-image=datawire/hello-world --name=hello-world
$ oc expose service hello-world
```

The service will be running once the following shows a pod with `Running` status that *doesn't* have "deploy" in its name:

```console
$ oc get pod | grep hello-world
hello-world-1-hljbs   1/1       Running   0          3m
```

To find the address of the resulting app you can run:

```console
$ oc get route hello-world
NAME          HOST/PORT
hello-world   example.openshiftapps.com
```

In the above output the address is `http://example.openshiftsapps.com`, but you will get a different value.
It may take a few minutes before this route will be live; in the interim you will get an error page.
If you do wait a minute and try again.

{{ macros.gettingStartedPart2("DeploymentConfig", "oc", "OpenShift") }}

```console
$ oc delete dc,service,route,imagestream hello-world
```

Telepresence can do much more than this: see the reference section of the documentation, on the top-left, for details.

{{ macros.install("https://docs.openshift.org/latest/cli_reference/get_started_cli.html", "oc", "OpenShift", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}