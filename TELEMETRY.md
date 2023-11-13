# Telemetry

Telepresence submits telemetry to Ambassador Labs' systems.
These metrics help us understand usage and improve the product; they include information on the user's operating system version, but no identifying information.

The following metrics are collected:

| Metric Name                           | Description                                                                                                                                          |
| ------------------------------------- |------------------------------------------------------------------------------------------------------------------------------------------------------|
| `intercept_fail`                      | An attempt to create an intercept has failed. Includes an `error` trait detailing the error.                                                         |
| `intercept_validation_fail`           | There has been an attempt to creat an invalid intercept. Includes an `error` trait detailing the error.                                              |
| `intercept_success`                   | An attempt to create an intercept has succeeded.                                                                                                     |
| `preview_domain_create_success`       | An attempt to add a preview URL to an intercept has succeeded.                                                                                       |
| `preview_domain_create_fail`          | An attempt to add a preview URL to an intercept has failed. Includes an `error` trait.                                                               |
| `Used legacy syntax`                  | A [legacy command](https://www.telepresence.io/docs/latest/install/migrate-from-legacy/#using-legacy-telepresence-commands) has been used.           |
| `incluster_dns_queries`               | Number of queries made by Telepresence to resolve a name to a cluster service (e.g. `kubernetes.default`). Inclues a `total` and a `failures` trait. |
| `connect`                             | Telepresence has attempted to connect to the cluster. Includes `time_to_connect`, `mapped_namespaces`, and `manager_version`                         |
| `connect_error`                       | Telepresence has failed to connect to the cluster. Includes `error`, `error_type`, `error_category`, `time_to_fail`, and `mapped_namespaces`.        |
| `updated_routes`                      | Telepresence has updated the routes on the client machine. Includes `cluster_`, `also_proxy_`, `never_proxy_` and `allow_conflicting_subnets` traits |
| `login_failure`                       | A `telepresence login` has failed. Includes an `error` trait detailing the error, and a `method` trait detailing the login method.                   |
| `login_interrupted`                   | A `telepresence login` has been interrupted by the user, includes a `method` trait detailing the login method.                                       |
| `login_success`                       | A `telepresence login` has succeded, includes a `method` trait detailing the login method.                                                           |
| `used_gather_logs`                    | A `telepresence gather-logs` command has been used.                                                                                                  |
| `vpn_diag_error`                      | A `telepresence test-vpn` command has been used and has resulted in an error.                                                                        |
| `vpn_diag_fail`                       | A `telepresence test-vpn` command has been used; no error, but it reports a misconfigured network. Includes traits detailing the failure.            |
| `vpn_diag_pass`                       | A `telepresence test-vpn` command has been used and reported no misconfigurations.                                                                   |
| `connector_remove_intercept_success`  | The user daemon has successfully removed an intercept                                                                                                |
| `connector_remove_intercept_fail`     | The user daemon has failed to remove an intercept. Includes an `error` trait describing the failure.                                                 |
| `connector_create_intercept_success`  | The user daemon has successfully created an intercept                                                                                                |
| `connector_create_intercept_fail`     | The user daemon has failed to create an intercept. Includes an `error` trait describing the failure.                                                 |
| `connector_can_intercept_success`     | The user daemon has validated that an intercept can be created.                                                                                      |
| `connector_can_intercept_fail`        | The user daemon has determined that an intercept can't be created. Includes an `error` trait describing the reason.                                  |
| `pro_connector_upgrade_refusal`       | The upgrade to the pro connector was refused by the user. Includes an `first_install` boolean trait.                                                 |
| `pro_connector_upgrade_success`       | The upgrade to the pro connector succeeded. Includes an `first_install` boolean trait.                                                               |
| `pro_connector_upgrade_fail`          | The upgrade to the pro connector failed. Includes an `error` trait describing the failure and a `first_install` boolean trait.                       |
| `helm_install_success`                | helm install success, contains key: "upgrade", value: bool                                                                                           |
| `helm_install_failure`                | helm install failure, contains key: "upgrade", value: bool, contains key: "error", value: string                                                     |
