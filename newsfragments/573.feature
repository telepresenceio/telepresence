Various points in the Kubernetes stack have timeouts for idle connections.
This includes the Kubelet, the API server, or even an ELB that might be in front of everything.
Telepresence now avoids those timeouts by periodically sending data through its open connections.
In some cases, this will prevent sessions from ending abruptly due to a lost connection.
