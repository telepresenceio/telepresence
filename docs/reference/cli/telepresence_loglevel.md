---
title: telepresence loglevel
description: Temporarily change the log-level of the traffic-manager, traffic-agent, and user and root daemons
hide_table_of_contents: true
---

Temporarily change the log-level of the traffic-manager, traffic-agent, and user and root daemons

### Usage:
```
  telepresence loglevel &lt;ERROR,WARN,INFO,DEBUG,TRACE&gt; [flags]
```

### Flags:
```
  -d, --duration duration   The time that the log-level will be in effect (0s means indefinitely) (default 30m0s)
  -h, --help                help for loglevel
  -l, --local-only          Only affect the user and root daemons
  -r, --remote-only         Only affect the traffic-manager and traffic-agents
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
