# Telemetry

Telepresence submits telemetry to Ambassador Labs' systems.
These metrics help us understand usage and improve the product; they include information on the user's operating system version, but no identifying information.

The following metrics are collected:

|              Metric Name              |                                         Description                                                                                        |
| ------------------------------------- | -----------------------------------------------------------------------------------------------------------------------------------------  |
| `intercept_fail`                      | An attempt to create an intercept has failed. Includes an `error` trait detailing the error.                                               |
| `intercept_success`                   | An attempt to create an intercept has succeeded.                                                                                           |
| `preview_domain_create_fail`          | An attempt to create an intercept with a preview URL has failed. Includes an `error` trait                                                 |
| `Used legacy syntax`                  | A [legacy command](https://www.telepresence.io/docs/latest/install/migrate-from-legacy/#using-legacy-telepresence-commands) has been used  |
| `incluster_dns_query`                 | Telepresence has attempted to resolve a DNS query to a cluster service (e.g. `kubernetes.default`). Inclues a `had_results` trait.         |
| `connect`                             | Telepresence has attempted to connect to the cluster.                                                                                      |
| `connecting_traffic_manager`          | Telepresence has attempted to connect to the Traffic Manager.                                                                              |
| `finished_connecting_traffic_manager` | Telepresence has succeeded at connecting to the Traffic Manager.                                                                           |
| `login_failure`                       | A `telepresence login` has failed. Includes an `error` trait detailing the error, and a `method` trait detailing the login method.         |
| `login_interrupted`                   | A `telepresence login` has been interrupted by the user, includes a `method` trait detailing the login method.                             |
| `login_success`                       | A `telepresence login` has succeded, includes a `method` trait detailing the login method.                                                 |
| `used_gather_logs`                    | A `telepresence gather-logs` command has been used.                                                                                        |
| `vpn_diag_error`                      | A `telepresence test-vpn` command has been used and has resulted in an error.                                                              |
| `vpn_diag_fail`                       | A `telepresence test-vpn` command has been used; no error, but it reports a misconfigured network. Includes traits detailing the failure.  |
| `vpn_diag_pass`                       | A `telepresence test-vpn` command has been used and reported no misconfigurations.                                                         |
