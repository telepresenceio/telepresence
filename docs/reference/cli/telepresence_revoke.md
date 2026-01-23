---
title: telepresence revoke
description: Revoke an intercept by intercept ID. The intercept ID must be in the format &lt;session_id&gt;:&lt;intercept_name&gt;
hide_table_of_contents: true
---

Revoke an intercept by intercept ID. The intercept ID must be in the format &lt;session_id&gt;:&lt;intercept_name&gt;

## Synopsis:

Revoke an intercept by intercept ID. This is an administrative operation that
requires RBAC permissions to modify the &quot;traffic-manager&quot; configmap.

### Usage:
```
  telepresence revoke &lt;intercept_id&gt; [flags]
```

### Flags:
```
  -h, --help   help for revoke
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
