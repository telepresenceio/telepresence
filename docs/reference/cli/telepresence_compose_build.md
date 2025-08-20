---
title: telepresence compose build
description: Build or rebuild services
hide_table_of_contents: true
---

Build or rebuild services

### Usage:
```
  telepresence compose build [flags] [services]
```

### Flags:
```
  -h, --help   help for build
```

### Compose flags:
```
      --env-file stringArray       Optional environment files
  -f, --file stringArray           Compose configuration files
      --profile stringArray        Profile to enable
      --project-directory string   Specify an alternate working directory (default: the path of the, first specified, Compose file)
      --project-name string        Project name
```

### Compose build flags:
```
      --build-arg stringArray   Set build-time variables for services
      --builder string          Set builder to use
      --check                   Check build configuration
  -m, --memory bytes            Set memory limit for the build container.
      --no-cache                Do not use cache when building the image
      --print                   Print equivalent bake file
      --provenance string       Add a provenance attestation
      --pull                    Always attempt to pull a newer version of
      --push                    Push service images
  -q, --quiet                   Suppress the build output
      --sbom string             Add a SBOM attestation
      --ssh string              Set SSH authentications used when
      --with-dependencies       Also build dependencies (transitively)
```

### Global Flags:
```
      --output string     Set the output format, supported values are 'json', 'yaml', and 'default' (default "default")
      --progress string   Set type of progress output (auto, tty, plain, json, quiet) (default "auto")
      --use string        Match expression that uniquely identifies the daemon container
```
