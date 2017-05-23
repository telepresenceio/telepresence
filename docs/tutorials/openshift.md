---
layout: doc
weight: 2
title: "Get started with OpenShift"
categories: tutorials
---

A full tutorial will be coming soon.
In the meantime, you can try the Kubernetes tutorial, with the following differences:

* You need `oc` installed rather than `kubectl`.

Note also that:

* OpenShift uses `DeploymentConfig` rather than `Deployment` objects.
* OpenShift will not run containers as root, so you can't listen on ports <1024.
