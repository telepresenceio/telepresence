---
name: Bug report
about: Create a report to help us improve Telepresence

---

**Describe the bug**
A clear and concise description of what the bug is.

Additionally, if you are using Telepresence 2.4.4 and above, please use
`telepresence gather-logs` to create a zip file of all logs for Telepresence's
components (root and user daemons, traffic-manager, and traffic-agents) and
attach it to this issue. See an example command below:
```
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

**Additional context**
Add any other context about the problem here.
