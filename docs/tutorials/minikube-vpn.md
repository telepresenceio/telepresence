# Minikube VPN access

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

### Transparently connecting to Minikube

In this tutorial you'll see how Telepresence allows you to get transparent access to Minikube networking from a local process outside of Minikube.
This allows you to use your local tools on your laptop to communicate with processes inside Minikube.

You should start by running a service in the cluster:

```console
$ kubectl config use-context minikube
$ kubectl run myservice --image=datawire/hello-world --port=8000 --expose
$ kubectl get service myservice
NAME        CLUSTER-IP   EXTERNAL-IP   PORT(S)    AGE
myservice   10.0.0.12    <none>        8000/TCP   1m
```

It may take a minute or two for the pod running the server to be up and running, depending on how fast your cluster is.

You can now run a local shell using Telepresence that can access that service, even though the process is local but the service is running inside Minikube:

```console
$ telepresence --run-shell
@minikube|$ curl http://myservice:8000/
Hello, world!
```

You also have access to the same environment variables a pod in the minikube cluster would have:

```console
@minikube|$ env | grep MYSERVICE_
MYSERVICE_SERVICE_HOST=10.0.0.12
MYSERVICE_SERVICE_PORT=8000
```

(This will not work if the hello world pod hasn't started yet... if so, try again.)

Telepresence will also allow services within minikube to [access a process running your host machine](kubernetes-rapid.html).

{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}