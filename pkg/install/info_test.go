package edgectl

import (
	"testing"
)

func TestNewClusterInfo(t *testing.T) {
	// contents of a `kubectl get nodes -o json` (with some parts removed)
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
    "apiVersion": "v1",
    "items": [
        {
            "apiVersion": "v1",
            "kind": "Node",
            "metadata": {
                "annotations": {
                    "flannel.alpha.coreos.com/backend-data": "{\"VtepMAC\":\"3e:f4:73:ac:f0:4e\"}",
                    "flannel.alpha.coreos.com/backend-type": "vxlan",
                    "flannel.alpha.coreos.com/kube-subnet-manager": "true",
                    "flannel.alpha.coreos.com/public-ip": "192.168.16.2",
                    "node.alpha.kubernetes.io/ttl": "0",
                    "volumes.kubernetes.io/controller-managed-attach-detach": "true"
                },
                "creationTimestamp": "2020-04-23T13:54:48Z",
                "finalizers": [
                    "wrangler.cattle.io/node"
                ],
                "labels": {
                    "beta.kubernetes.io/arch": "amd64",
                    "beta.kubernetes.io/instance-type": "k3s",
                    "beta.kubernetes.io/os": "linux",
                    "k3s.io/hostname": "k3d-k3s-cluster-902-server",
                    "k3s.io/internal-ip": "192.168.16.2",
                    "kubernetes.io/arch": "amd64",
                    "kubernetes.io/hostname": "k3d-k3s-cluster-902-server",
                    "kubernetes.io/os": "linux",
                    "node-role.kubernetes.io/master": "true",
                    "node.kubernetes.io/instance-type": "k3s"
                },
                "name": "k3d-k3s-cluster-902-server",
                "uid": "854803d0-91be-4498-bd1f-4d8c4bee97f8"
            },
            "spec": {
                "podCIDR": "10.42.0.0/24",
                "podCIDRs": [
                    "10.42.0.0/24"
                ],
                "providerID": "k3s://k3d-k3s-cluster-902-server"
            },
            "status": {
                "nodeInfo": {
                    "architecture": "amd64",
                    "bootID": "cf070a50-ef11-4d47-9e83-42ddc84b6d57",
                    "containerRuntimeVersion": "containerd://1.3.3-k3s1",
                    "kernelVersion": "5.4.13-050413-generic",
                    "kubeProxyVersion": "v1.17.3+k3s1",
                    "kubeletVersion": "v1.17.3+k3s1",
                    "machineID": "",
                    "operatingSystem": "linux",
                    "osImage": "Unknown"
                }
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

	ci := NewClusterInfo(k)
	if !ci.isLocal {
		t.Errorf("cluster not recogtnized as a local K3D cluster: %s", ci.name)
	}
	if ci.name != clusterInfoDatabase[clusterK3D].name {
		t.Errorf("cluster not recognized as a K3D cluster: %s", ci.name)
	}
}

func TestGetExistingInstallationEdgectl(t *testing.T) {
	// contents of a `kubectl get deployments -n ambassador -o json` (with some parts removed)
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
    "apiVersion": "v1",
    "items": [
        {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "annotations": {
                    "deployment.kubernetes.io/revision": "1"
                },
                "creationTimestamp": "2020-04-24T14:23:15Z",
                "generation": 1,
                "labels": {
                    "app.kubernetes.io/instance": "ambassador",
                    "app.kubernetes.io/managed-by": "edgectl",
                    "app.kubernetes.io/name": "ambassador-redis",
                    "app.kubernetes.io/part-of": "ambassador",
                    "helm.sh/chart": "ambassador-6.3.2",
                    "product": "aes"
                },
                "name": "ambassador-redis",
                "namespace": "ambassador",
                "resourceVersion": "1139",
                "selfLink": "/apis/apps/v1/namespaces/ambassador/deployments/ambassador-redis",
                "uid": "ed4695b4-bb68-41ad-937b-263e49d1bd64"
            },
            "spec": {
                "progressDeadlineSeconds": 600,
                "replicas": 1,
                "revisionHistoryLimit": 10,
                "selector": {
                    "matchLabels": {
                        "app.kubernetes.io/instance": "ambassador",
                        "app.kubernetes.io/name": "ambassador-redis"
                    }
                },
                "strategy": {
                    "rollingUpdate": {
                        "maxSurge": "25%",
                        "maxUnavailable": "25%"
                    },
                    "type": "RollingUpdate"
                },
                "template": {
                    "metadata": {
                        "creationTimestamp": null,
                        "labels": {
                            "app.kubernetes.io/instance": "ambassador",
                            "app.kubernetes.io/name": "ambassador-redis"
                        }
                    },
                    "spec": {
                        "containers": [
                            {
                                "image": "redis:5.0.1",
                                "imagePullPolicy": "IfNotPresent",
                                "name": "redis",
                                "resources": {},
                                "terminationMessagePath": "/dev/termination-log",
                                "terminationMessagePolicy": "File"
                            }
                        ],
                        "dnsPolicy": "ClusterFirst",
                        "restartPolicy": "Always",
                        "schedulerName": "default-scheduler",
                        "securityContext": {},
                        "terminationGracePeriodSeconds": 30
                    }
                }
            },
            "status": {
                "availableReplicas": 1,
                "conditions": [
                    {
                        "lastTransitionTime": "2020-04-24T14:23:22Z",
                        "lastUpdateTime": "2020-04-24T14:23:22Z",
                        "message": "Deployment has minimum availability.",
                        "reason": "MinimumReplicasAvailable",
                        "status": "True",
                        "type": "Available"
                    },
                    {
                        "lastTransitionTime": "2020-04-24T14:23:15Z",
                        "lastUpdateTime": "2020-04-24T14:23:22Z",
                        "message": "ReplicaSet \"ambassador-redis-8556cbb4c6\" has successfully progressed.",
                        "reason": "NewReplicaSetAvailable",
                        "status": "True",
                        "type": "Progressing"
                    }
                ],
                "observedGeneration": 1,
                "readyReplicas": 1,
                "replicas": 1,
                "updatedReplicas": 1
            }
        },
        {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {
                "annotations": {
                    "deployment.kubernetes.io/revision": "1"
                },
                "creationTimestamp": "2020-04-24T14:23:15Z",
                "generation": 1,
                "labels": {
                    "app.kubernetes.io/instance": "ambassador",
                    "app.kubernetes.io/managed-by": "edgectl",
                    "app.kubernetes.io/name": "ambassador",
                    "app.kubernetes.io/part-of": "ambassador",
                    "helm.sh/chart": "ambassador-6.3.2",
                    "product": "aes"
                },
                "name": "ambassador",
                "namespace": "ambassador",
                "resourceVersion": "1208",
                "selfLink": "/apis/apps/v1/namespaces/ambassador/deployments/ambassador",
                "uid": "0bb43ade-7b47-43e4-b74e-172e478e59cd"
            },
            "spec": {
                "progressDeadlineSeconds": 600,
                "replicas": 1,
                "revisionHistoryLimit": 10,
                "selector": {
                    "matchLabels": {
                        "app.kubernetes.io/instance": "ambassador",
                        "app.kubernetes.io/name": "ambassador"
                    }
                },
                "strategy": {
                    "rollingUpdate": {
                        "maxSurge": "25%",
                        "maxUnavailable": "25%"
                    },
                    "type": "RollingUpdate"
                },
                "template": {
                    "metadata": {
                        "annotations": {
                            "checksum/config": "01ba4719c80b6fe911b091a7c05124b64eeece964e09c058ef8f9805daca546b"
                        },
                        "creationTimestamp": null,
                        "labels": {
                            "app.kubernetes.io/instance": "ambassador",
                            "app.kubernetes.io/managed-by": "edgectl",
                            "app.kubernetes.io/name": "ambassador",
                            "app.kubernetes.io/part-of": "ambassador",
                            "product": "aes"
                        }
                    },
                    "spec": {
                        "containers": [
                            {
                                "env": [
                                    {
                                        "name": "HOST_IP",
                                        "valueFrom": {
                                            "fieldRef": {
                                                "apiVersion": "v1",
                                                "fieldPath": "status.hostIP"
                                            }
                                        }
                                    },
                                    {
                                        "name": "REDIS_URL",
                                        "value": "ambassador-redis:6379"
                                    },
                                    {
                                        "name": "AMBASSADOR_NAMESPACE",
                                        "valueFrom": {
                                            "fieldRef": {
                                                "apiVersion": "v1",
                                                "fieldPath": "metadata.namespace"
                                            }
                                        }
                                    }
                                ],
                                "image": "quay.io/datawire/aes:1.4.2",
                                "imagePullPolicy": "IfNotPresent",
                                "livenessProbe": {
                                    "failureThreshold": 3,
                                    "httpGet": {
                                        "path": "/ambassador/v0/check_alive",
                                        "port": "admin",
                                        "scheme": "HTTP"
                                    },
                                    "initialDelaySeconds": 30,
                                    "periodSeconds": 3,
                                    "successThreshold": 1,
                                    "timeoutSeconds": 1
                                },
                                "name": "ambassador",
                                "ports": [
                                    {
                                        "containerPort": 8080,
                                        "name": "http",
                                        "protocol": "TCP"
                                    },
                                    {
                                        "containerPort": 8443,
                                        "name": "https",
                                        "protocol": "TCP"
                                    },
                                    {
                                        "containerPort": 8877,
                                        "name": "admin",
                                        "protocol": "TCP"
                                    }
                                ],
                                "readinessProbe": {
                                    "failureThreshold": 3,
                                    "httpGet": {
                                        "path": "/ambassador/v0/check_ready",
                                        "port": "admin",
                                        "scheme": "HTTP"
                                    },
                                    "initialDelaySeconds": 30,
                                    "periodSeconds": 3,
                                    "successThreshold": 1,
                                    "timeoutSeconds": 1
                                },
                                "resources": {},
                                "terminationMessagePath": "/dev/termination-log",
                                "terminationMessagePolicy": "File",
                                "volumeMounts": [
                                    {
                                        "mountPath": "/tmp/ambassador-pod-info",
                                        "name": "ambassador-pod-info",
                                        "readOnly": true
                                    },
                                    {
                                        "mountPath": "/.config/ambassador",
                                        "name": "ambassador-edge-stack-secrets",
                                        "readOnly": true
                                    }
                                ]
                            }
                        ],
                        "dnsPolicy": "ClusterFirst",
                        "restartPolicy": "Always",
                        "schedulerName": "default-scheduler",
                        "securityContext": {
                            "runAsUser": 8888
                        },
                        "serviceAccount": "ambassador",
                        "serviceAccountName": "ambassador",
                        "terminationGracePeriodSeconds": 30,
                        "volumes": [
                            {
                                "downwardAPI": {
                                    "defaultMode": 420,
                                    "items": [
                                        {
                                            "fieldRef": {
                                                "apiVersion": "v1",
                                                "fieldPath": "metadata.labels"
                                            },
                                            "path": "labels"
                                        }
                                    ]
                                },
                                "name": "ambassador-pod-info"
                            },
                            {
                                "name": "ambassador-edge-stack-secrets",
                                "secret": {
                                    "defaultMode": 420,
                                    "secretName": "ambassador-edge-stack"
                                }
                            }
                        ]
                    }
                }
            },
            "status": {
                "availableReplicas": 1,
                "conditions": [
                ],
                "observedGeneration": 1,
                "readyReplicas": 1,
                "replicas": 1,
                "updatedReplicas": 1
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

	version, info, err := getExistingInstallation(k)
	if err != nil {
		t.Fatalf("Could not get existing deployments information")
	}
	t.Logf("Existing installation detetced as: version=%s, method=%s", version, info.LongName)
	if info.Method != instEdgectl {
		t.Errorf("existing installation not properly detected: %s", info.LongName)
	}
	if version != "1.4.2" {
		t.Errorf("existing version not properly detected: %s", version)
	}
}

func TestGetExistingInstallationNone(t *testing.T) {
	// contents of a `kubectl get deployments -n ambassador -l app.kubernetes.io/managed-by=WHATEVER -o json`
	// when nothing has been installed
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
    "apiVersion": "v1",
    "items": [],
    "kind": "List",
    "metadata": {
        "resourceVersion": "",
        "selfLink": ""
    }
}
`,
	}

	version, info, err := getExistingInstallation(k)
	if err != nil {
		t.Fatalf("Could not get existing deployments information")
	}
	t.Logf("Existing installation detected as: version=%s, method=%s", version, info.LongName)
	if info.Method != instNone {
		t.Errorf("existing installation not properly detected: %s", info.LongName)
	}
}
