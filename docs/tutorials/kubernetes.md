---
layout: doc
weight: 1
title: "Debug a service locally"
categories: tutorials
---

<link rel="stylesheet" href="{{ "/css/mermaid.css" | prepend: site.baseurl }}">
<script src="{{ "/js/mermaid.min.js" | prepend: site.baseurl }}"></script>
<script>mermaid.initialize({
   startOnLoad: true,
   cloneCssStyles: false,
 });
</script>

{% include install.md cluster="Kubernetes" command="kubectl" install="https://kubernetes.io/docs/tasks/tools/install-kubectl/" %}

{% include getting-started-part-1.md cluster="Kubernetes" command="kubectl" deployment="Deployment" %}

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

{% include getting-started-part-2.md cluster="Kubernetes" command="kubectl" deployment="Deployment" %}

```console
$ kubectl delete deployment,service hello-world
```

Telepresence can do much more than this: see the reference section of the documentation, on the top-left, for details.
