import Alert from '@material-ui/lab/Alert';

# Manually injecting the Traffic Agent

You can directly modify your workload's YAML configuration to add the Telepresence Traffic Agent and enable it to be intercepted.

When you use a Telepresence intercept, Telepresence automatically edits the workload and services when you use
`telepresence uninstall --agent <your_agent_name>`. In some GitOps workflows, you may need to use the
[Telepresence Mutating Webhook](../../cluster-config/#mutating-webhook) to keep intercepted workloads unmodified
while you target changes on specific pods.

<Alert severity="warning">
In situations where you don't have access to the proper permissions for numeric ports, as noted in the Note on numeric ports
section of the documentation, it is possible to manually inject the Traffic Agent. Because this is not the recommended approach
to making a workload interceptable, try the Mutating Webhook before proceeding."
</Alert>

## Procedure

You can manually inject the agent into Deployments, StatefulSets, or ReplicaSets. The example on this page
uses the following Deployment:


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
```

The deployment is being exposed by the following service:

```yaml
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

First, generate the YAML for the traffic-agent container:

```console
$ telepresence genyaml container --container-name echo-container --port 8080 --output - --input deployment.yaml
args:
- agent
env:
- name: TELEPRESENCE_CONTAINER
  value: echo-container
- name: _TEL_AGENT_LOG_LEVEL
  value: info
- name: _TEL_AGENT_NAME
  value: my-service
- name: _TEL_AGENT_NAMESPACE
  valueFrom:
    fieldRef:
      fieldPath: metadata.namespace
- name: _TEL_AGENT_POD_IP
  valueFrom:
    fieldRef:
      fieldPath: status.podIP
- name: _TEL_AGENT_APP_PORT
  value: "8080"
- name: _TEL_AGENT_AGENT_PORT
  value: "9900"
- name: _TEL_AGENT_MANAGER_HOST
  value: traffic-manager.ambassador
image: docker.io/datawire/tel2:2.4.6
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
```

Next, generate the YAML for the volume:

```console
$ telepresence genyaml volume --output - --input deployment.yaml
downwardAPI:
  items:
  - fieldRef:
      fieldPath: metadata.annotations
    path: annotations
name: traffic-annotations
```

<Alert severity="info">
Enter `telepresence genyaml container --help` or `telepresence genyaml volume --help` for more information about these flags.
</Alert>

### 2. Injecting the YAML into the Deployment

You need to add the `Deployment` YAML you genereated to include the container and the volume. These are placed as elements of `spec.template.spec.containers` and `spec.template.spec.volumes` respectively.
You also need to modify `spec.template.metadata.annotations` and add the annotation `telepresence.getambassador.io/manually-injected: "true"`.
These changes should look like the following:

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
+     annotations:
+       telepresence.getambassador.io/manually-injected: "true"
    spec:
      containers:
        - name: echo-container
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
          resources: {}
+       - args:
+         - agent
+         env:
+         - name: TELEPRESENCE_CONTAINER
+           value: echo-container
+         - name: _TEL_AGENT_LOG_LEVEL
+           value: info
+         - name: _TEL_AGENT_NAME
+           value: my-service
+         - name: _TEL_AGENT_NAMESPACE
+           valueFrom:
+             fieldRef:
+               fieldPath: metadata.namespace
+         - name: _TEL_AGENT_POD_IP
+           valueFrom:
+             fieldRef:
+               fieldPath: status.podIP
+         - name: _TEL_AGENT_APP_PORT
+           value: "8080"
+         - name: _TEL_AGENT_AGENT_PORT
+           value: "9900"
+         - name: _TEL_AGENT_MANAGER_HOST
+           value: traffic-manager.ambassador
+         image: docker.io/datawire/tel2:2.4.6
+         name: traffic-agent
+         ports:
+         - containerPort: 9900
+           protocol: TCP
+         readinessProbe:
+           exec:
+             command:
+             - /bin/stat
+             - /tmp/agent/ready
+         resources: {}
+         volumeMounts:
+         - mountPath: /tel_pod_info
+           name: traffic-annotations
+     volumes:
+       - downwardAPI:
+           items:
+           - fieldRef:
+               fieldPath: metadata.annotations
+             path: annotations
+         name: traffic-annotations
```

### 3. Modifying the service

Once the modified deployment YAML has been applied to the cluster, you need to modify the Service to route traffic to the Traffic Agent.
You can do this by changing the exposed `targetPort` to `9900`. The resulting service should look like:

```diff
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
-     targetPort: 8080
+     targetPort: 9900
```
