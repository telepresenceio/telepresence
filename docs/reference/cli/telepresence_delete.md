---
title: telepresence delete
description: Tear down the state described by a manifest
hide_table_of_contents: true
---

Tear down the state described by a manifest

## Synopsis:

Tear down the state described by a manifest: a declarative YAML description
of a Telepresence connection and its attachments (intercept, replace,
ingest, or wiretap). Attachments are removed in reverse manifest order; an
attachment that's already absent is reported but is not an error. The
connection is only disconnected if the manifest declares one.

### Usage:
```
  telepresence delete -f &lt;manifest&gt; [flags]
```

### Flags:
```
  -f, --filename string   The manifest file to delete. Use &quot;-&quot; to read from stdin
  -h, --help              help for delete
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file
      --format string     Set the output format, supported values are 'json', 'yaml', 'json-stream', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
