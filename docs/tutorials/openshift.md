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

{% include getting-started-part-1.md cluster="OpenShift" command="oc" %}

To find the address of the `Service` run:

... XXX ...

{% include getting-started-part-2.md %}
