---
title: telepresence detach
description: Remove existing attachment
hide_table_of_contents: true
---

Remove existing attachment

### Usage:
```
  telepresence detach [flags] &lt;attachment_name&gt;
```

### Flags:
```
  -c, --container string   Container name
  -h, --help               help for detach
  -n, --namespace string   Namespace of the ingest. Required to disambiguate ingests with the same workload name across mapped namespaces
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
