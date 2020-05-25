package edgectl

import (
	"testing"
)

func TestAmbInstallationKIND(t *testing.T) {
	// contents of a `kubectl get -n ambassador ambassadorinstallations.getambassador.io -o json`
	// (with some parts removed) in a KIND cluster where we have installed AOSS
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
    "apiVersion": "v1",
    "items": [
        {
            "apiVersion": "getambassador.io/v2",
            "kind": "AmbassadorInstallation",
            "metadata": {
                "annotations": {
                    "kubectl.kubernetes.io/last-applied-configuration": "{\"apiVersion\":\"getambassador.io/v2\",\"kind\":\"AmbassadorInstallation\",\"metadata\":{\"annotations\":{},\"name\":\"ambassador\",\"namespace\":\"ambassador\"},\"spec\":{\"helmRepo\":\"https://github.com/datawire/ambassador-chart/archive/master.zip\",\"helmValues\":{\"deploymentTool\":\"amb-oper-kind\",\"nodeSelector\":{\"ingress-ready\":\"true\"},\"replicaCount\":1,\"service\":{\"ports\":[{\"hostPort\":80,\"name\":\"http\",\"port\":80,\"protocol\":\"TCP\",\"targetPort\":8080},{\"hostPort\":443,\"name\":\"https\",\"port\":443,\"protocol\":\"TCP\",\"targetPort\":8443}],\"type\":\"NodePort\"},\"tolerations\":[{\"effect\":\"NoSchedule\",\"key\":\"node-role.kubernetes.io/master\",\"operator\":\"Equal\"}]},\"installOSS\":true}}\n"
                },
                "creationTimestamp": "2020-05-20T14:51:39Z",
                "finalizers": [
                    "uninstall-amb-operator-release"
                ],
                "generation": 1,
                "name": "ambassador",
                "namespace": "ambassador",
                "resourceVersion": "2404",
                "selfLink": "/apis/getambassador.io/v2/namespaces/ambassador/ambassadorinstallations/ambassador",
                "uid": "f67c7377-d80d-4141-93f4-6c6c6a8c0b55"
            },
            "spec": {
                "helmRepo": "https://github.com/datawire/ambassador-chart/archive/master.zip",
                "helmValues": {
                    "deploymentTool": "amb-oper-kind",
                    "nodeSelector": {
                        "ingress-ready": "true"
                    },
                    "replicaCount": 1,
                    "service": {
                        "ports": [
                            {
                                "hostPort": 80,
                                "name": "http",
                                "port": 80,
                                "protocol": "TCP",
                                "targetPort": 8080
                            },
                            {
                                "hostPort": 443,
                                "name": "https",
                                "port": 443,
                                "protocol": "TCP",
                                "targetPort": 8443
                            }
                        ],
                        "type": "NodePort"
                    },
                    "tolerations": [
                        {
                            "effect": "NoSchedule",
                            "key": "node-role.kubernetes.io/master",
                            "operator": "Equal"
                        }
                    ]
                },
                "installOSS": true
            },
            "status": {
                "conditions": [
                    {
                        "lastTransitionTime": "2020-05-20T14:51:53Z",
                        "message": "-------------------------------------------------------------------------------\n  Congratulations! You've successfully installed Ambassador!\n\n-------------------------------------------------------------------------------\nTo get the IP address of Ambassador, run the following commands:\n  export NODE_PORT=$(kubectl get --namespace ambassador -o jsonpath=\"{.spec.ports[0].nodePort}\" services ambassador)\n  export NODE_IP=$(kubectl get nodes --namespace ambassador -o jsonpath=\"{.items[0].status.addresses[0].address}\")\n  echo http://$NODE_IP:$NODE_PORT\n\nFor help, visit our Slack at https://d6e.co/slack or view the documentation online at https://www.getambassador.io.\n",
                        "reason": "InstallSuccessful",
                        "status": "True",
                        "type": "Deployed"
                    }
                ],
                "deployedRelease": {
                    "appVersion": "1.4.3",
                    "flavor": "OSS",
                    "manifest": "---\n# Source: ambassador/templates/serviceaccount.yaml\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: ambassador\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/part-of: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    product: aes\n---\n# Source: ambassador/templates/crds-rbac.yaml\napiVersion: rbac.authorization.k8s.io/v1beta1\nkind: ClusterRole\nmetadata:\n  name: ambassador-crds\n  labels:\n    app.kubernetes.io/name: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    product: aes\nrules:\n  - apiGroups: [ \"apiextensions.k8s.io\" ]\n    resources:\n    - customresourcedefinitions\n    resourceNames:\n    - authservices.getambassador.io\n    - mappings.getambassador.io\n    - modules.getambassador.io\n    - ratelimitservices.getambassador.io\n    - tcpmappings.getambassador.io\n    - tlscontexts.getambassador.io\n    - tracingservices.getambassador.io\n    - kubernetesendpointresolvers.getambassador.io\n    - kubernetesserviceresolvers.getambassador.io\n    - consulresolvers.getambassador.io\n    - filters.getambassador.io\n    - filterpolicies.getambassador.io\n    - ratelimits.getambassador.io\n    - hosts.getambassador.io\n    - logservices.getambassador.io\n    verbs: [\"get\", \"list\", \"watch\", \"delete\"]\n---\n# Source: ambassador/templates/rbac.yaml\napiVersion: rbac.authorization.k8s.io/v1beta1\nkind: ClusterRole\nmetadata:\n  name: ambassador\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/part-of: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    product: aes\nrules:\n  - apiGroups: [\"\"]\n    resources:\n    - namespaces\n    - services\n    - secrets\n    - endpoints\n    verbs: [\"get\", \"list\", \"watch\"]\n\n  - apiGroups: [ \"getambassador.io\" ]\n    resources: [ \"*\" ]\n    verbs: [\"get\", \"list\", \"watch\", \"update\", \"patch\", \"create\", \"delete\" ]\n\n  - apiGroups: [ \"apiextensions.k8s.io\" ]\n    resources: [ \"customresourcedefinitions\" ]\n    verbs: [\"get\", \"list\", \"watch\"]\n\n  - apiGroups: [ \"networking.internal.knative.dev\"]\n    resources: [ \"clusteringresses\" ]\n    verbs: [\"get\", \"list\", \"watch\"]\n\n  - apiGroups: [ \"extensions\", \"networking.k8s.io\" ]\n    resources: [ \"ingresses\" ]\n    verbs: [\"get\", \"list\", \"watch\"]\n\n  - apiGroups: [ \"extensions\", \"networking.k8s.io\" ]\n    resources: [ \"ingresses/status\" ]\n    verbs: [\"update\"]\n---\n# Source: ambassador/templates/crds-rbac.yaml\napiVersion: rbac.authorization.k8s.io/v1beta1\nkind: ClusterRoleBinding\nmetadata:\n  name: ambassador-crds\n  labels:\n    app.kubernetes.io/name: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: ambassador-crds\nsubjects:\n  - name: ambassador\n    namespace: \"ambassador\"\n    kind: ServiceAccount\n---\n# Source: ambassador/templates/rbac.yaml\napiVersion: rbac.authorization.k8s.io/v1beta1\nkind: ClusterRoleBinding\nmetadata:\n  name: ambassador\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/part-of: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    product: aes\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: ambassador\nsubjects:\n  - name: ambassador\n    namespace: ambassador\n    kind: ServiceAccount\n---\n# Source: ambassador/templates/admin-service.yaml\napiVersion: v1\nkind: Service\nmetadata:\n  name: ambassador-admin\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/part-of: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    # Hard-coded label for Prometheus Operator ServiceMonitor\n    service: ambassador-admin\n    product: aes\nspec:\n  type: ClusterIP\n  ports:\n    - port: 8877\n      targetPort: admin\n      protocol: TCP\n      name: ambassador-admin\n  selector:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/instance: ambassador\n---\n# Source: ambassador/templates/service.yaml\napiVersion: v1\nkind: Service\nmetadata:\n  name: ambassador\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/part-of: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    app.kubernetes.io/component: ambassador-service\n    product: aes\nspec:\n  type: NodePort\n  ports:\n    - name: http\n      port: 80\n      targetPort: 8080\n      protocol: TCP\n    - name: https\n      port: 443\n      targetPort: 8443\n      protocol: TCP\n  selector:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/instance: ambassador\n---\n# Source: ambassador/templates/deployment.yaml\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: ambassador\n  namespace: ambassador\n  labels:\n    app.kubernetes.io/name: ambassador\n    app.kubernetes.io/part-of: ambassador\n    helm.sh/chart: ambassador-6.3.6\n    app.kubernetes.io/instance: ambassador\n    app.kubernetes.io/managed-by: amb-oper-manifest\n    product: aes\nspec:\n  replicas: 1\n  selector:\n    matchLabels:\n      app.kubernetes.io/name: ambassador\n      app.kubernetes.io/instance: ambassador\n  strategy:\n    type: RollingUpdate\n  template:\n    metadata:\n      labels:\n        app.kubernetes.io/name: ambassador\n        app.kubernetes.io/part-of: ambassador\n        app.kubernetes.io/instance: ambassador\n        app.kubernetes.io/managed-by: amb-oper-manifest\n        product: aes\n      annotations:\n        checksum/config: 01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b\n    spec:\n      securityContext:\n        runAsUser: 8888\n      serviceAccountName: ambassador\n      volumes:\n        - name: ambassador-pod-info\n          downwardAPI:\n            items:\n              - fieldRef:\n                  fieldPath: metadata.labels\n                path: labels\n      containers:\n        - name: ambassador\n          image: \"quay.io/datawire/ambassador:1.4.3\"\n          imagePullPolicy: IfNotPresent\n          ports:\n            - name: http\n              containerPort: 8080\n              protocol: TCP\n              hostPort: 80\n            - name: https\n              containerPort: 8443\n              protocol: TCP\n              hostPort: 443\n            - name: admin\n              containerPort: 8877\n          env:\n            - name: HOST_IP\n              valueFrom:\n                fieldRef:\n                  fieldPath: status.hostIP\n            - name: AMBASSADOR_NAMESPACE\n              valueFrom:\n                fieldRef:\n                  fieldPath: metadata.namespace\n          livenessProbe:\n            httpGet:\n              path: /ambassador/v0/check_alive\n              port: admin\n            initialDelaySeconds: 30\n            periodSeconds: 3\n            failureThreshold: 3\n          readinessProbe:\n            httpGet:\n              path: /ambassador/v0/check_ready\n              port: admin\n            initialDelaySeconds: 30\n            periodSeconds: 3\n            failureThreshold: 3\n          volumeMounts:\n            - name: ambassador-pod-info\n              mountPath: /tmp/ambassador-pod-info\n              readOnly: true\n          resources:\n            {}\n      nodeSelector:\n        ingress-ready: \"true\"\n      tolerations:\n        - effect: NoSchedule\n          key: node-role.kubernetes.io/master\n          operator: Equal\n      imagePullSecrets:\n        []\n      dnsPolicy: ClusterFirst\n      hostNetwork: false\n",
                    "name": "ambassador",
                    "version": "6.3.6"
                },
                "lastCheckTime": "2020-05-20T14:51:47Z"
            }
        }
    ],
    "kind": "List",
    "metadata": {
        "resourceVersion": "",
        "selfLink": ""
    }
}
`,
	}

	ambInst, err := findAmbassadorInstallation(k)
	if err != nil {
		t.Fatalf("Could not get AmbassadorInstallation: %s", err)
	}
	if ambInst.IsEmpty() {
		t.Fatalf("AmbassadorInstallation is empty")
	}
	if !ambInst.IsInstalled() {
		t.Fatalf("AmbassadorInstallation is not installed")
	}
	iv, err := ambInst.GetInstalledVersion()
	if err != nil {
		t.Fatalf("Could not get AmbassadorInstallation version: %s", err)
	}
	if iv != "1.4.3" {
		t.Fatalf("AmbassadorInstallation is not version 1.4.3 but %s", iv)
	}
	if !ambInst.GetInstallOSS() {
		t.Fatalf("AmbassadorInstallation is not OSS")
	}
	if err := ambInst.SetInstallOSS(false); err != nil {
		t.Fatalf("Could not set OSS=false: %s", err)
	}
	if ambInst.GetInstallOSS() {
		t.Fatalf("AmbassadorInstallation is still OSS")
	}

	cond := ambInst.LastCondition()
	res, ok := cond["reason"]
	if !ok {
		t.Fatalf("AmbassadorInstallation does not have a 'reason' in the last condition")
	}
	if res.(string) != "InstallSuccessful" {
		t.Fatalf("AmbassadorInstallation was not installed succesfully")
	}

}
