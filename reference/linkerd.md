---
Description: "How to get LinkerD meshed services working with Telepresence 2"
---

# Using Telepresence on LinkerD meshed services

## Introduction
Getting started with Telepresence 2 on LinkerD services is as simple as adding an annotation: `config.linkerd.io/skip-outbound-ports: "8081,8022,6001"`.  The traffic-agent uses port 8081 for its API, uses 8022 for sshfs, and 6001 for the actual tunnel between traffic-manager and the local system.  Telling LinkerD to skip these ports allows the traffic-agent sidecar to fully communicate with the traffic-manager, and therefore the rest of the telepresence system.

## Prerequisites
1. Telepresence 2 binary
2. LinkerD Control Plane [installed to cluster](https://linkerd.io/2.10/tasks/install/)
3. Kubectl
4. [Working Ingress controller](../../../edge-stack/latest/howtos/linkerd2.md)

## Deploy
Save and deploy the following YAML, note the `config.linkerd.io/skip-outbound-ports` annotation in the metadata of the pod template.
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
Run `telepresence connect` to connect to the cluster.  A followup `telepresence list` should show the `quote` deployment as `ready to intercept`.

```sh
telepresence list
quote: ready to intercept (traffic-agent not yet installed)
```

## Run the Intercept
Run `telepresence intercept quote --port 8080:80` to direct traffic from the `quote` deployment to port 8080 on your local system.  Assuming you have somthing listening on 8080, you should now be able to see your local service whenever attempting to access the `quote` service.
