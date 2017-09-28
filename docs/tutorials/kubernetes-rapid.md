# Rapid development with Kubernetes

{% import "../macros.html" as macros %}
{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "top") }}

### Rapid development with Telepresence

Imagine you're developing a new Kubernetes service.
Typically the way you'd test is by changing the code, rebuilding the image, pushing the image to a Docker registry, and then redeploying the Kubernetes `Deployment`.
This can be slow.

Or, you can use Telepresence.
Telepresence will proxy a remote `Deployment` to a process running on your machine.
That means you can develop locally, editing code as you go, but test your service inside the Kubernetes cluster.

Let's say you're working on the following minimal server, `helloworld.py`:

```python
#!/usr/bin/env python3

from http.server import BaseHTTPRequestHandler, HTTPServer

class RequestHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-type', 'text/plain')
        self.end_headers()
        self.wfile.write(b"Hello, world!\n")
        return

httpd = HTTPServer(('', 8080), RequestHandler)
httpd.serve_forever()
```

You start a proxy inside your Kubernetes cluster that will forward requests from the cluster to your local process, and in the resulting shell you start the web server:

```
localhost$ telepresence --new-deployment hello-world --expose 8080
localhost$ python3 helloworld.py
```

This will create a new `Deployment` and `Service` named `hello-world`, which will listen on port 8080 and forward traffic to the process on your machine on port 8080.

You can see this if you start a container inside the Kubernetes cluster and connect to that `Service`.
In a new terminal run:

```console
localhost$ kubectl --restart=Never run -i -t --image=alpine console /bin/sh
kubernetes# wget -O - -q http://hello-world:8080/
Hello, world!
```

Now, switch back to the other terminal, kill `helloworld.py` and edit it so it returns a different string.
For example:

```console
python3 helloworld.py
^C
localhost$ sed s/Hello/Goodbye/g -i helloworld.py
localhost$ grep Goodbye helloworld.py
        self.wfile.write(b"Goodbye, world!\n")
localhost$ python3 helloworld.py
```

Now that we've restarted our local process with new code, we can send it another query from the other terminal where we have a shell running inside a Kubernetes pod:

```console
kubernetes# wget -O - -q http://hello-world:8080/
Goodbye, world!
kubernetes# exit
```

And there you have it: you edit your code locally, and changes are reflected immediately to clients inside the Kubernetes cluster without having to redeploy, create Docker images, and so on.

{{ macros.install("https://kubernetes.io/docs/tasks/tools/install-kubectl/", "kubectl", "Kubernetes", "bottom") }}

{{ macros.tutorialFooter(page.title, file.path, book['baseUrl']) }}