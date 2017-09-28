# Debug a Kubernetes service locally

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

{{ macros.gettingStartedPart1("Kubernetes")}}

You should start a `Deployment` and publicly exposed `Service` like this:

```console
$ kubectl run hello-world --image=datawire/hello-world --port=8000
$ kubectl expose deployment hello-world --type=LoadBalancer --name=hello-world
```

> **If your cluster is in the cloud** you can find the address of the resulting `Service` like this:
>
> ```console
> $ kubectl get service hello-world
> NAME          CLUSTER-IP     EXTERNAL-IP       PORT(S)          AGE
> hello-world   10.3.242.226   104.197.103.123   8000:30022/TCP   5d
> ```

> If you see `<pending>` under EXTERNAL-IP wait a few seconds and try again.
> In this case the `Service` is exposed at `http://104.197.103.123:8000/`.

> **On `minikube` you should instead** do this to find the URL:
> 
> ```console
> $ minikube service --url hello-world
> http://192.168.99.100:12345/
> ```

{{ macros.gettingStartedPart2("Deployment", "kubectl", "Kubernetes") }}

```console
$ kubectl delete deployment,service hello-world
```

Telepresence can do much more than this: see the reference section of the documentation, on the top-left, for details.

{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}