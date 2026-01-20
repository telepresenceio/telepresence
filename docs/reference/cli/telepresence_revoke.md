---
title: telepresence revoke
description: Revoke an intercept by intercept ID. The intercept ID must be in the format <session_id>:<intercept_name>
hide_table_of_contents: true
---

Revoke an intercept by intercept ID. The intercept ID must be in the format <session_id>:<intercept_name>

## Synopsis:

Revoke an intercept by intercept ID. This is an administrative operation that
requires RBAC permissions to modify the "traffic-manager" configmap.

### Usage:
```
  telepresence revoke <intercept_id> [flags]
```

### Flags:
```
  -h, --help   help for revoke
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default "$HOME/.config/telepresence/config.yml")
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
