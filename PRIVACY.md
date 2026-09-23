# Privacy Policy

By default, Telepresence sends anonymous usage reports: from the CLI and
user daemon on your workstation, and from the traffic-manager running in
your cluster. For exactly what a report contains and every way to opt
out, see
[Usage reporting](https://telepresence.io/docs/reference/config#usage) in
the reference documentation. In short:

- A `usage-opt-out` marker file next to the workstation's system-wide
  `config.yml` disables reporting unconditionally, machine-wide.
- The client setting `usage.enabled: false` in `config.yml` (per-user or
  machine-wide) stops the CLI and user daemon from sending reports.
- The Helm chart setting `usage.enabled` (or an empty
  `usage.collectorAddress`) stops the traffic-manager from sending
  reports.

No other data leaves your workstation or cluster except what your own
`telepresence` commands send to the Kubernetes cluster you connect it to.
