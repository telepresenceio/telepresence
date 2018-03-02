# Installing Telepresence

{% import "../macros.html" as macros %}
{{ macros.installSpecific("reference-page") }}

### Dependencies

If you install Telepresence using a pre-built package, dependencies other than [`kubectl`][k] are handled by the package manager. If you install from source, you will also need to install the following software.

[k]: https://kubernetes.io/docs/tasks/tools/install-kubectl/

- `kubectl` (OpenShift users can use `oc`)
- Python 3.5 or newer
- OpenSSH (the `ssh` command)
- `sshfs`
- `conntrack` on Linux
- `torsocks` for the inject-tcp method
- Docker for the container method
- `socat` for the container method
