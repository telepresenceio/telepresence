---
title: telepresence completion
description: Generate a shell completion script
hide_table_of_contents: true
---

Generate a shell completion script

## Synopsis:

To load completions:

### Bash:
```bash
  $ source &lt;(telepresence completion bash)

  # To load completions for each session, execute once:
  # Linux:
  $ telepresence completion bash &gt; /etc/bash_completion.d/telepresence
  # macOS:
  $ telepresence completion bash &gt; $(brew --prefix)/etc/bash_completion.d/telepresence
```

### Zsh:
```zsh

  # If shell completion is not already enabled in your environment,
  # you will need to enable it.  You can execute the following once:

  $ echo &quot;autoload -U compinit; compinit&quot; &gt;&gt; ~/.zshrc

  # To load completions for each session, execute once:
  $ telepresence completion zsh &gt; &quot;${fpath[1]}/_telepresence&quot;

  # You will need to start a new shell for this setup to take effect.
```

### fish:
```fish

  $ telepresence completion fish | source

  # To load completions for each session, execute once:
  $ telepresence completion fish &gt; ~/.config/fish/completions/telepresence.fish
```

### PowerShell:
```powershell

  PS&gt; telepresence completion powershell | Out-String | Invoke-Expression

  # To load completions for every new session, run:
  PS&gt; telepresence completion powershell &gt; telepresence.ps1
  # and source this file from your PowerShell profile.
```

### Usage:
```
  telepresence completion [flags]
```

### Flags:
```
  -h, --help   help for completion
```

### Global Flags:
```
      --config string     Path to the Telepresence configuration file (default &quot;$HOME/.config/telepresence/config.yml&quot;)
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default &quot;default&quot;)
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default &quot;auto&quot;)
      --use string        Match expression that uniquely identifies the daemon container
```
