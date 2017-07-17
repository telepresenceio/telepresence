"""
Unit tests (in-memory, small units of code).
"""

import yaml

from . import telepresence

COMPLEX_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: my-nginx
spec:
  replicas: 3
  template:
    metadata:
      labels:
        app: nginx
    spec:
      volumes:
      - name: secret-volume
        secret:
          secretName: nginxsecret
      - name: configmap-volume
        configMap:
          name: nginxconfigmap
      containers:
      - name: dontchange
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
      - name: nginxhttps
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        imagePullPolicy: Latest
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
"""

SWAPPED_DEPLOYMENT = """\
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: my-nginx
  labels:
    telepresence: random_id_123
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: nginx
        telepresence: random_id_123
    spec:
      volumes:
      - name: secret-volume
        secret:
          secretName: nginxsecret
      - name: configmap-volume
        configMap:
          name: nginxconfigmap
      containers:
      - name: dontchange
        image: ymqytw/nginxhttps:1.5
        command: ["/home/auto-reload-nginx.sh"]
        args: ["lalalal"]
        terminationMessagePolicy: "File"
        workingDir: "/somewhere/over/the/rainbow"
        ports:
        - containerPort: 443
        - containerPort: 80
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
          initialDelaySeconds: 30
          timeoutSeconds: 1
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
      - name: new_name
        image: datawire/telepresence-k8s:0.777
        terminationMessagePolicy: "FallbackToLogsOnError"
        imagePullPolicy: "IfNotPresent"
        ports:
        - containerPort: 443
        - containerPort: 80
        volumeMounts:
        - mountPath: /etc/nginx/ssl
          name: secret-volume
        - mountPath: /etc/nginx/conf.d
          name: configmap-volume
"""


def test_swap_deployment_changes():
    """
    The modified Deployment used to swap out an existing Deployment replaces
    all values that might break our own image.
    """
    original = yaml.safe_load(COMPLEX_DEPLOYMENT)
    expected = yaml.safe_load(SWAPPED_DEPLOYMENT)
    assert telepresence.new_swapped_deployment(
        original, "nginxhttps", "random_id_123",
        "datawire/telepresence-k8s:0.777", False, False,
        "new_name",
    ) == expected
