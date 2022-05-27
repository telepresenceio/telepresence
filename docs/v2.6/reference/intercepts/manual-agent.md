import Alert from '@material-ui/lab/Alert';

# Manually injecting the Traffic Agent

You can directly modify your workload's YAML configuration to add the Telepresence Traffic Agent and enable it to be intercepted.

When you use a Telepresence intercept for the first time on a Pod, the [Telepresence Mutating Webhook](../../cluster-config/#mutating-webhook)
will automatically inject a Traffic Agent sidecar into it. There might be some situations where this approach cannot be used, such
as very strict company security policies preventing it.

<Alert severity="warning">
Although it is possible to manually inject the Traffic Agent, it is not the recommended approach to making a workload interceptable,
try the Mutating Webhook before proceeding.
</Alert>

## Procedure

You can manually inject the agent into Deployments, StatefulSets, or ReplicaSets. The example on this page
uses the following Deployment and Service. It's a prerequisite that they have been applied to the cluster:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: "my-service"
  labels:
    service: my-service
spec:
  replicas: 1
  selector:
    matchLabels:
      service: my-service
  template:
    metadata:
      labels:
        service: my-service
    spec:
      containers:
        - name: echo-container
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
          resources: {}
---
apiVersion: v1
kind: Service
metadata:
  name: "my-service"
spec:
  type: ClusterIP
  selector:
    service: my-service
  ports:
    - port: 80
      targetPort: 8080
```

### 1. Generating the YAML

First, generate the YAML for the traffic-agent configmap entry. It's important that the generated file have
the same name as the service, and no extension:

```console
$ telepresence genyaml config --workload my-service -o /tmp/my-service
$ cat /tmp/my-service-config.yaml
agentImage: docker.io/datawire/tel2:2.6.0
agentName: my-service
containers:
- Mounts: null
  envPrefix: A_
  intercepts:
  - agentPort: 9900
    containerPort: 8080
    protocol: TCP
    serviceName: my-service
    servicePort: 80
    serviceUID: f6680334-10ef-4703-aa4e-bb1f9d1665fd
  mountPoint: /tel_app_mounts/echo-container
  name: echo-container
logLevel: info
managerHost: traffic-manager.ambassador
managerPort: 8081
manual: true
namespace: default
workloadKind: Deployment
workloadName: my-service
```

Next, generate the YAML for the traffic-agent container:

```console
$ telepresence genyaml container --config /tmp/my-service -o /tmp/my-service-agent.yaml
$ cat /tmp/my-service-agent.yaml 
args:
- agent
env:
- name: _TEL_AGENT_POD_IP
  valueFrom:
    fieldRef:
      apiVersion: v1
      fieldPath: status.podIP
image: docker.io/datawire/tel2:2.6.0-beta.12
name: traffic-agent
ports:
- containerPort: 9900
  protocol: TCP
readinessProbe:
  exec:
    command:
    - /bin/stat
    - /tmp/agent/ready
resources: {}
volumeMounts:
- mountPath: /tel_pod_info
  name: traffic-annotations
- mountPath: /etc/traffic-agent
  name: traffic-config
- mountPath: /tel_app_exports
  name: export-volume
  name: traffic-annotations
```

Next, generate the init-container

```console
$ telepresence genyaml initcontainer --config /tmp/my-service -o /tmp/my-service-init.yaml
$ cat /tmp/my-service-init.yaml 
args:
- agent-init
image: docker.io/datawire/tel2:2.6.0-beta.12
name: tel-agent-init
resources: {}
securityContext:
  capabilities:
    add:
    - NET_ADMIN
volumeMounts:
- mountPath: /etc/traffic-agent
  name: traffic-config
```

Next, generate the YAML for the volumes:

```console
$ telepresence genyaml volume --workload my-service -o /tmp/my-service-volume.yaml
$ cat /tmp/my-service-volume.yaml 
- downwardAPI:
    items:
    - fieldRef:
        apiVersion: v1
        fieldPath: metadata.annotations
      path: annotations
  name: traffic-annotations
- configMap:
    items:
    - key: my-service
      path: config.yaml
    name: telepresence-agents
  name: traffic-config
- emptyDir: {}
  name: export-volume

```

<Alert severity="info">
Enter `telepresence genyaml container --help` or `telepresence genyaml volume --help` for more information about these flags.
</Alert>

### 2. Creating (or updating) the configmap

The generated configmap entry must be insterted into the `telepresence-agents` `ConfigMap` in the same namespace as the
modified `Deployment`. If the `ConfigMap` doesn't exist yet, it can be created using the following command:

```console
$ kubectl create configmap telepresence-agents --from-file=/tmp/my-service
```

If it already exists, new entries can be added under the `Data` key using `kubectl edit configmap telepresence-agents`.

### 3. Injecting the YAML into the Deployment

You need to add the `Deployment` YAML you genereated to include the container and the volume. These are placed as elements
of `spec.template.spec.containers`,  `spec.template.spec.initContainers`, and `spec.template.spec.volumes` respectively. 
You also need to modify `spec.template.metadata.annotations` and add the annotation
`telepresence.getambassador.io/manually-injected: "true"`.  These changes should look like the following:

```diff
 apiVersion: apps/v1
 kind: Deployment
 metadata:
   name: "my-service"
   labels:
     service: my-service
 spec:
   replicas: 1
   selector:
     matchLabels:
       service: my-service
   template:
     metadata:
       labels:
         service: my-service
+      annotations:
+        telepresence.getambassador.io/manually-injected: "true"
     spec:
      containers:
        - name: echo-container
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
          resources: {}
+        - args:
+            - agent
+          env:
+            - name: _TEL_AGENT_POD_IP
+              valueFrom:
+                fieldRef:
+                  apiVersion: v1
+                  fieldPath: status.podIP
+          image: docker.io/datawire/tel2:2.6.0-beta.12
+          name: traffic-agent
+          ports:
+            - containerPort: 9900
+              protocol: TCP
+          readinessProbe:
+            exec:
+              command:
+                - /bin/stat
+                - /tmp/agent/ready
+          resources: { }
+          volumeMounts:
+            - mountPath: /tel_pod_info
+              name: traffic-annotations
+            - mountPath: /etc/traffic-agent
+              name: traffic-config
+            - mountPath: /tel_app_exports
+              name: export-volume
+      initContainers:
+        - args:
+            - agent-init
+          image: docker.io/datawire/tel2:2.6.0-beta.12
+          name: tel-agent-init
+          resources: { }
+          securityContext:
+            capabilities:
+              add:
+                - NET_ADMIN
+          volumeMounts:
+            - mountPath: /etc/traffic-agent
+              name: traffic-config
+      volumes:
+        - downwardAPI:
+            items:
+              - fieldRef:
+                  apiVersion: v1
+                  fieldPath: metadata.annotations
+                path: annotations
+          name: traffic-annotations
+        - configMap:
+            items:
+              - key: my-service
+                path: config.yaml
+            name: telepresence-agents
+          name: traffic-config
+        - emptyDir: { }
+          name: export-volume
```
