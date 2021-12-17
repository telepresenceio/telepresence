import Alert from '@material-ui/lab/Alert';
import { ClusterConfig } from '../../../../../src/components/Docs/Telepresence';

# Cluster-side configuration

For the most part, Telepresence doesn't require any special
configuration in the cluster and can be used right away in any
cluster (as long as the user has adequate [RBAC permissions](../rbac)
and the cluster's server version is `1.17.0` or higher).

However, some advanced features do require some configuration in the
cluster.

## TLS

In this example, other applications in the cluster expect to speak TLS to your
intercepted application (perhaps you're using a service-mesh that does
mTLS).

In order to use `--mechanism=http` (or any features that imply
`--mechanism=http`) you need to tell Telepresence about the TLS
certificates in use.

Tell Telepresence about the certificates in use by adjusting your
[workload's](../intercepts/#supported-workloads) Pod template to set a couple of
annotations on the intercepted Pods:

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
+      annotations:
+        "getambassador.io/inject-terminating-tls-secret": "your-terminating-secret"  # optional
+        "getambassador.io/inject-originating-tls-secret": "your-originating-secret"  # optional
     spec:
+      serviceAccountName: "your-account-that-has-rbac-to-read-those-secrets"
       containers:
```

- The `getambassador.io/inject-terminating-tls-secret` annotation
  (optional) names the Kubernetes Secret that contains the TLS server
  certificate to use for decrypting and responding to incoming
  requests.

  When Telepresence modifies the Service and workload port
  definitions to point at the Telepresence Agent sidecar's port
  instead of your application's actual port, the sidecar will use this
  certificate to terminate TLS.

- The `getambassador.io/inject-originating-tls-secret` annotation
  (optional) names the Kubernetes Secret that contains the TLS
  client certificate to use for communicating with your application.

  You will need to set this if your application expects incoming
  requests to speak TLS (for example, your
  code expects to handle mTLS itself instead of letting a service-mesh
  sidecar handle mTLS for it, or the port definition that Telepresence
  modified pointed at the service-mesh sidecar instead of at your
  application).

  If you do set this, you should to set it to the
  same client certificate Secret that you configure the Ambassador
  Edge Stack to use for mTLS.

It is only possible to refer to a Secret that is in the same Namespace
as the Pod.

The Pod will need to have permission to `get` and `watch` each of
those Secrets.

Telepresence understands `type: kubernetes.io/tls` Secrets and
`type: istio.io/key-and-cert` Secrets; as well as `type: Opaque`
Secrets that it detects to be formatted as one of those types.

## Air gapped cluster

If your cluster is on an isolated network such that it cannot
communicate with Ambassador Cloud, then some additional configuration
is required to acquire a license key in order to use personal
intercepts.

### Create a license

1. <ClusterConfig /> 

2. Generate a new license (if one doesn't already exist) by clicking *Generate New License*.

3. You will be prompted for your Cluster ID. Ensure your
kubeconfig context is using the cluster you want to create a license for then
run this command to generate the Cluster ID:

  ```
  $ telepresence current-cluster-id

    Cluster ID: <some UID>
  ```

4. Click *Generate API Key* to finish generating the license.

5. On the licenses page, download the license file associated with your cluster.

### Add license to cluster
There are two separate ways you can add the license to your cluster: manually creating and deploying
the license secret or having the helm chart manage the secret

You only need to do one of the two options.

#### Manual deploy of license secret

1. Use this command to generate a Kubernetes Secret config using the license file:

  ```
  $ telepresence license -f <downloaded-license-file>

    apiVersion: v1
    data:
      hostDomain: <long_string>
      license: <longer_string>
    kind: Secret
    metadata:
      creationTimestamp: null
      name: systema-license
      namespace: ambassador
  ```

2. Save the output as a YAML file and apply it to your
cluster with `kubectl`.

3. When deploying the `traffic-manager` chart, you must add the additional values when running `helm install` by putting
the following into a file (for the example we'll assume it's called license-values.yaml)

  ```
  licenseKey:
    # This mounts the secret into the traffic-manager
    create: true
    secret:
      # This tells the helm chart not to create the secret since you've created it yourself
      create: false
  ```

4. Install the helm chart into the cluster

  ```
  helm install traffic-manager -n ambassador datawire/telepresence --create-namespace -f license-values.yaml
  ```

5. Ensure that you have the docker image for the Smart Agent (datawire/ambassador-telepresence-agent:1.11.0)
pulled and in a registry your cluster can pull from.

6. Have users use the `images` [config key](../config/#images) keys so telepresence uses the aforementioned image for their agent.

#### Helm chart manages the secret

1. Get the jwt token from the downloaded license file

  ```
  $ cat ~/Downloads/ambassador.License_for_yourcluster
  eyJhbGnotarealtoken.butanexample
  ```

2. Create the following values file, substituting your real jwt token in for the one used in the example below.
(for this example we'll assume the following is placed in a file called license-values.yaml)

  ```
  licenseKey:
    # This mounts the secret into the traffic-manager
    create: true
    # This is the value from the license file you download. this value is an example and will not work
    value: eyJhbGnotarealtoken.butanexample
    secret:
      # This tells the helm chart to create the secret
      create: true
  ```

3. Install the helm chart into the cluster

  ```
  helm install traffic-manager charts/telepresence -n ambassador --create-namespace -f license-values.yaml
  ```

Users will now be able to use preview intercepts with the
`--preview-url=false` flag.  Even with the license key, preview URLs
cannot be used without enabling direct communication with Ambassador
Cloud, as Ambassador Cloud is essential to their operation.

If using Helm to install the server-side components, see the chart's [README](https://github.com/telepresenceio/telepresence/tree/release/v2/charts/telepresence) to learn how to configure the image registry and license secret.

Have clients use the [skipLogin](../config/#cloud) key to ensure the cli knows it is operating in an
air-gapped environment.

## Mutating Webhook

By default, Telepresence updates the intercepted workload (Deployment, StatefulSet, ReplicaSet)
template to add the [Traffic Agent](../architecture/#traffic-agent) sidecar container and update the
port definitions. If you use GitOps workflows (with tools like ArgoCD) to automatically update your
cluster so that it reflects the desired state from an external Git repository, this behavior can make
your workload out of sync with that external desired state.

To solve this issue, you can use Telepresence's Mutating Webhook alternative mechanism. Intercepted
workloads will then stay untouched and only the underlying pods will be modified to inject the Traffic
Agent sidecar container and update the port definitions.

Simply add the `telepresence.getambassador.io/inject-traffic-agent: enabled` annotation to your
workload template's annotations:

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
+      annotations:
+        telepresence.getambassador.io/inject-traffic-agent: enabled
     spec:
       containers:
```

### Service Port Annotation

A service port annotation can be added to the workload to make the Mutating Webhook select a specific port
in the service. This is necessary when the service has multiple ports.

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
       annotations:
         telepresence.getambassador.io/inject-traffic-agent: enabled
+        telepresence.getambassador.io/inject-service-port: https
     spec:
       containers:
```

### Service Name Annotation

A service name annotation can be added to the workload to make the Mutating Webhook select a specific Kubernetes service.
This is necessary when the workload is exposed by multiple services.

```diff
 spec:
   template:
     metadata:
       labels:
         service: your-service
       annotations:
         telepresence.getambassador.io/inject-traffic-agent: enabled
+        telepresence.getambassador.io/inject-service-name: my-service
     spec:
       containers:
```

### Note on Numeric Ports

If the <code>targetPort</code> of your intercepted service is pointing at a port number, in addition to
injecting the Traffic Agent sidecar, Telepresence will also inject an <code>initContainer</code> that will
reconfigure the pod's firewall rules to redirect traffic to the Traffic Agent.

<Alert severity="info">
Note that this <code>initContainer</code> requires `NET_ADMIN` capabilities.
If your cluster administrator has disabled them, you will be unable to use numeric ports with the agent injector.
</Alert>

<Alert severity="info">
This requires the Traffic Agent to run as GID <code>7777</code>. By default, this is disabled on openshift clusters.
To enable running as GID <code>7777</code> on a specific openshift namespace, run:
<code>oc adm policy add-scc-to-group anyuid system:serviceaccounts:$NAMESPACE</code>
</Alert>

If you need to use numeric ports without the aforementioned capabilities, you can [manually install the agent](../intercepts/manual-agent)

For example, the following service is using a numeric port, so Telepresence would inject an initContainer into it:
```yaml
apiVersion: v1
kind: Service
metadata:
  name: your-service
spec:
  type: ClusterIP
  selector:
    service: your-service
  ports:
    - port: 80
      targetPort: 8080
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: your-service
  labels:
    service: your-service
spec:
  replicas: 1
  selector:
    matchLabels:
      service: your-service
  template:
    metadata:
      annotations:
        telepresence.getambassador.io/inject-traffic-agent: enabled
      labels:
        service: your-service
    spec:
      containers:
        - name: your-container
          image: jmalloc/echo-server
          ports:
            - containerPort: 8080
```
