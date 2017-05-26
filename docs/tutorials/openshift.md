---
layout: doc
weight: 2
title: "Get started with OpenShift"
categories: tutorials
---

<link rel="stylesheet" href="{{ "/css/mermaid.css" | prepend: site.baseurl }}">
<script src="{{ "/js/mermaid.min.js" | prepend: site.baseurl }}"></script>
<script>mermaid.initialize({
   startOnLoad: true,
   cloneCssStyles: false,
 });
</script>

You will need the following available on your machine:

* `oc` command line tool (here's the [installation instructions](https://docs.openshift.org/latest/cli_reference/get_started_cli.html)).
* Access to your OpenShift cluster, with local credentials on your machine.
  You can test this by running `oc get pod` - if this works you're all set.

**Note**: if you don't have a testing OpenShift cluster available we recommend using [minishift](https://docs.openshift.org/latest/minishift/index.html) over a free OpenShift Online account, since the latter only has limited resources available.

{% include getting-started-part-1.md cluster="OpenShift" command="oc" deployment="DeploymentConfig" %}

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

{% include getting-started-part-2.md cluster="OpenShift" command="oc" deployment="DeploymentConfig" %}

```console
$ oc delete dc,service,route,imagestream hello-world
```

Telepresence can do much more than this: see the reference section of the documentation, on the left, for details.
