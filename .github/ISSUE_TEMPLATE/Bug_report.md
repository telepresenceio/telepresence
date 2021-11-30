---
name: Bug report
about: Create a report to help us improve Telepresence

---

**Describe the bug**
A clear and concise description of what the bug is.

Additionally, if you are using Telepresence 2.4.4 and above, please use
`telepresence loglevel debug` to ensure we have the most helpful logs,
reproduce the error, and then run `telepresence gather-logs` to create a
zip file of all logs for Telepresence's components (root and user daemons,
traffic-manager, and traffic-agents) and attach it to this issue. See an
example command below:
```
telepresence loglevel debug

* reproduce the error *

telepresence gather-logs --output-file /tmp/telepresence_logs.zip

# To see all options, run the following command
telepresence gather-logs --help
```


**To Reproduce**
Steps to reproduce the behavior:
1. When I run '...'
2. I see '....'
3. So I look at '....'
4. See error

**Expected behavior**
A clear and concise description of what you expected to happen.

**Versions (please complete the following information):**
 - Output of `telepresence version`
 - Operating system of workstation running `telepresence` commands
 - Kubernetes environment and Version [e.g. Minikube, bare metal, Google Kubernetes Engine]

**VPN-related bugs:**
If you're reporting an issue around telepresence connectivity when using a VPN,
and are running Telepresence 2.4.8 or above, please also attach the output
of `telepresence test-vpn`, and the following information:
 - Which VPN client are you using?
 - Which VPN server are you using?
 - How is your VPN pushing DNS configuration? It may be useful to add the contents of /etc/resolv.conf

**Additional context**
Add any other context about the problem here.
