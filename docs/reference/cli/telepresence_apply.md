---
title: telepresence apply
description: Bring the workstation to the state described by a manifest
hide_table_of_contents: true
---

Bring the workstation to the state described by a manifest

## Synopsis:

Bring the workstation to the state described by a manifest: a declarative
YAML description of a Telepresence connection and its attachments (intercept,
replace, ingest, or wiretap). Attachments are established in manifest order;
attachments already present with a matching spec are left untouched, and
attachments whose spec has drifted are re-created.

### Usage:
```
  telepresence apply -f &lt;manifest&gt; [flags]
```

### Flags:
```
      --dry-run           Report what apply would do, without changing anything
  -f, --filename string   The manifest file to apply. Use &quot;-&quot; to read from stdin
  -h, --help              help for apply
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
