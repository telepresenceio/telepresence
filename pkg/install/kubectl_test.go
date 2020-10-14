package edgectl

import (
	"testing"
)

func TestVersion(t *testing.T) {
	// contents of a `kubectl version -o json` (with some parts removed)
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
  "clientVersion": {
    "major": "1",
    "minor": "18",
    "gitVersion": "v1.18.2",
    "gitCommit": "52c56ce7a8272c798dbc29846288d7cd9fbae032",
    "gitTreeState": "clean",
    "buildDate": "2020-04-16T11:56:40Z",
    "goVersion": "go1.13.9",
    "compiler": "gc",
    "platform": "linux/amd64"
  },
  "serverVersion": {
    "major": "1",
    "minor": "17",
    "gitVersion": "v1.17.3+k3s1",
    "gitCommit": "5b17a175ce333dfb98cb8391afeb1f34219d9275",
    "gitTreeState": "clean",
    "buildDate": "2020-02-27T07:28:53Z",
    "goVersion": "go1.13.8",
    "compiler": "gc",
    "platform": "linux/amd64"
  }
}
`,
	}

	v, err := k.Version()
	if err != nil {
		t.Fatalf("Could not get versionk: %s", err)
	}
	if v.Client.GitCommit != "52c56ce7a8272c798dbc29846288d7cd9fbae032" {
		t.Errorf("wrong client Git commit: %s", v.Client.GitCommit)
	}
	if v.Server.GitCommit != "5b17a175ce333dfb98cb8391afeb1f34219d9275" {
		t.Errorf("wrong server Git commit: %s", v.Server.GitCommit)
	}
}

func TestGet(t *testing.T) {
	// contents of a `kubectl version -o json` (with some parts removed)
	k := SimpleKubectl{
		// force an output, so no real `kubectl` is run
		output: `
{
    "apiVersion": "v1",
    "kind": "Pod",
    "metadata": {
        "creationTimestamp": "2020-04-24T14:10:18Z",
        "generateName": "coredns-d798c9dd-",
        "labels": {
            "k8s-app": "kube-dns",
            "pod-template-hash": "d798c9dd"
        },
        "name": "coredns-d798c9dd-kfxj9",
        "namespace": "kube-system",
        "ownerReferences": [
            {
                "apiVersion": "apps/v1",
                "blockOwnerDeletion": true,
                "controller": true,
                "kind": "ReplicaSet",
                "name": "coredns-d798c9dd",
                "uid": "8312c292-d36c-430d-83df-c194eb73914e"
            }
        ],
        "resourceVersion": "484",
        "selfLink": "/api/v1/namespaces/kube-system/pods/coredns-d798c9dd-kfxj9",
        "uid": "bb8ad852-8b08-45d6-ae14-22178b1aa942"
    },
    "spec": {
        "containers": [
            {
                "args": [
                    "-conf",
                    "/etc/coredns/Corefile"
                ],
                "image": "coredns/coredns:1.6.3",
                "imagePullPolicy": "IfNotPresent",
                "livenessProbe": {
                    "failureThreshold": 5,
                    "httpGet": {
                        "path": "/health",
                        "port": 8080,
                        "scheme": "HTTP"
                    },
                    "initialDelaySeconds": 60,
                    "periodSeconds": 10,
                    "successThreshold": 1,
                    "timeoutSeconds": 5
                },
                "name": "coredns",
                "ports": [
                    {
                        "containerPort": 53,
                        "name": "dns",
                        "protocol": "UDP"
                    },
                    {
                        "containerPort": 53,
                        "name": "dns-tcp",
                        "protocol": "TCP"
                    },
                    {
                        "containerPort": 9153,
                        "name": "metrics",
                        "protocol": "TCP"
                    }
                ],
                "readinessProbe": {
                    "failureThreshold": 5,
                    "httpGet": {
                        "path": "/ready",
                        "port": 8181,
                        "scheme": "HTTP"
                    },
                    "initialDelaySeconds": 10,
                    "periodSeconds": 10,
                    "successThreshold": 1,
                    "timeoutSeconds": 5
                },
                "resources": {
                    "limits": {
                        "memory": "170Mi"
                    },
                    "requests": {
                        "cpu": "100m",
                        "memory": "70Mi"
                    }
                },
                "securityContext": {
                    "allowPrivilegeEscalation": false,
                    "capabilities": {
                        "add": [
                            "NET_BIND_SERVICE"
                        ],
                        "drop": [
                            "all"
                        ]
                    },
                    "readOnlyRootFilesystem": true
                },
                "terminationMessagePath": "/dev/termination-log",
                "terminationMessagePolicy": "File",
                "volumeMounts": [
                    {
                        "mountPath": "/etc/coredns",
                        "name": "config-volume",
                        "readOnly": true
                    },
                    {
                        "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount",
                        "name": "coredns-token-vhdlb",
                        "readOnly": true
                    }
                ]
            }
        ],
        "dnsPolicy": "Default",
        "enableServiceLinks": true,
        "nodeName": "k3d-k3s-cluster-647-server",
        "nodeSelector": {
            "beta.kubernetes.io/os": "linux"
        },
        "priority": 0,
        "restartPolicy": "Always",
        "schedulerName": "default-scheduler",
        "securityContext": {},
        "serviceAccount": "coredns",
        "serviceAccountName": "coredns",
        "terminationGracePeriodSeconds": 30,
        "tolerations": [
            {
                "key": "CriticalAddonsOnly",
                "operator": "Exists"
            },
            {
                "effect": "NoExecute",
                "key": "node.kubernetes.io/not-ready",
                "operator": "Exists",
                "tolerationSeconds": 300
            },
            {
                "effect": "NoExecute",
                "key": "node.kubernetes.io/unreachable",
                "operator": "Exists",
                "tolerationSeconds": 300
            }
        ],
        "volumes": [
            {
                "configMap": {
                    "defaultMode": 420,
                    "items": [
                        {
                            "key": "Corefile",
                            "path": "Corefile"
                        },
                        {
                            "key": "NodeHosts",
                            "path": "NodeHosts"
                        }
                    ],
                    "name": "coredns"
                },
                "name": "config-volume"
            },
            {
                "name": "coredns-token-vhdlb",
                "secret": {
                    "defaultMode": 420,
                    "secretName": "coredns-token-vhdlb"
                }
            }
        ]
    },
    "status": {
        "containerStatuses": [
            {
                "containerID": "containerd://388965e8e70e4a7d8d681f4b2d2a99d1e734ae654c531545892172e397c9da79",
                "image": "docker.io/coredns/coredns:1.6.3",
                "imageID": "docker.io/coredns/coredns@sha256:cfa7236dab4e3860881fdf755880ff8361e42f6cba2e3775ae48e2d46d22f7ba",
                "lastState": {},
                "name": "coredns",
                "ready": true,
                "restartCount": 0,
                "started": true,
                "state": {
                    "running": {
                        "startedAt": "2020-04-24T14:10:30Z"
                    }
                }
            }
        ],
        "hostIP": "192.168.32.2",
        "phase": "Running",
        "podIP": "10.42.0.5",
        "podIPs": [
            {
                "ip": "10.42.0.5"
            }
        ],
        "qosClass": "Burstable",
        "startTime": "2020-04-24T14:10:18Z"
    }
}
`,
	}

	p, err := k.Get("pods", "coredns-d798c9dd-kfxj9", "kube-system")
	if err != nil {
		t.Fatalf("Could not get versionk: %s", err)
	}
	if p.IsList() {
		t.Fatalf("Get() returned a List")
	}
	labels := p.GetLabels()
	v, ok := labels["k8s-app"]
	if !ok {
		t.Fatalf("Label k8s-app not found")
	}
	if v != "kube-dns" {
		t.Fatalf("Label k8s-app does not have value kube-dns but %s", v)
	}

}
