---
Description: "How to get Linkerd meshed services working with Telepresence"
---

# Using Telepresence with Linkerd

## Introduction
Getting started with Telepresence on Linkerd services is as simple as adding an annotation to your Deployment:

```yaml
spec:
  template:
    metadata:
      annotations:
        config.linkerd.io/skip-outbound-ports: "8081"
```

The local system and the Traffic Agent connect to the Traffic Manager using its gRPC API on port 8081. Telling Linkerd to skip that port allows the Traffic Agent sidecar to fully communicate with the Traffic Manager, and therefore the rest of the Telepresence system.

## Prerequisites
1. [Telepresence binary](../../install)
2. Linkerd control plane [installed to cluster](https://linkerd.io/2.10/tasks/install/)
3. Kubectl
4. [Working ingress controller](https://www.getambassador.io/docs/edge-stack/latest/howtos/linkerd2)

## Deploy
Save and deploy the following YAML. Note the `config.linkerd.io/skip-outbound-ports` annotation in the metadata of the pod template.

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: quote
spec:
  replicas: 1
  selector:
    matchLabels:
      app: quote
  strategy:
    type: RollingUpdate
  template:
    metadata:
      annotations:
        linkerd.io/inject: "enabled"
        config.linkerd.io/skip-outbound-ports: "8081,8022,6001"
      labels:
        app: quote
    spec:
      containers:
      - name: backend
        image: docker.io/datawire/quote:0.4.1
        ports:
        - name: http
          containerPort: 8000
        env:
        - name: PORT
          value: "8000"
        resources:
          limits:
            cpu: "0.1"
            memory: 100Mi
```

## Connect to Telepresence
Run `telepresence connect` to connect to the cluster.  Then `telepresence list` should show the `quote` deployment as `ready to intercept`:

```
$ telepresence list

  quote: ready to intercept (traffic-agent not yet installed)
```

## Run the intercept
Run `telepresence intercept quote --port 8080:80` to direct traffic from the `quote` deployment to port 8080 on your local system.  Assuming you have something listening on 8080, you should now be able to see your local service whenever attempting to access the `quote` service.
