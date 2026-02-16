---
title: Upgrade client
description: "How to upgrade your installation of Telepresence and install previous versions."
hide_table_of_contents: true
---

import Platform from '@site/src/components/Platform';

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available. Running the same commands used for installation will replace your current version with the latest.

Before upgrading your CLI, you must stop any live Telepresence processes by issuing `telepresence quit -s` (or `telepresence quit -ur`
if your current version is less than 2.8.0).

<Platform.Provider>
<Platform.TabGroup>
<Platform.MacOSTab>

## Upgrade with the package installer (Recommended)

Download and install the latest `.pkg` for your architecture from the [install page](client.md). The installer
will replace the previous version and restart the root daemon service.

## OR upgrade with Homebrew

```shell
brew upgrade telepresenceio/telepresence/telepresence-oss
```

## OR upgrade by downloading the binary manually

### Intel Macs

```shell
# 1. Download the binary.
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

### Apple Silicon Macs

```shell
# 1. Ensure that no old binary exists. This is very important because Apple Silicon macs track the executable's
# signature and just updating it in place will not work.
sudo rm -f /usr/local/bin/telepresence

# 2. Download the binary.
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-arm64 -o /usr/local/bin/telepresence

# 3. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```
</Platform.MacOSTab>
<Platform.GNULinuxTab>

## Upgrade with a package manager (Recommended)

Download and install the latest `.deb` or `.rpm` package using the same commands as the initial
[installation](client.md). The package manager will handle replacing the previous version and restarting the
root daemon service.

## OR upgrade by downloading the binary manually

```shell
# 1. Download the latest binary (~95 MB):
### AMD64
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64 -o /usr/local/bin/telepresence

### ARM64
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

## Upgrade with the setup installer (Recommended)

Download and run the latest setup installer from the [install page](client.md). The installer will replace the
previous version and restart the root daemon service.

## OR upgrade by downloading manually

Download the latest zip from the [install page](client.md) and follow the manual installation steps, which will
replace the existing installation.

</Platform.WindowsTab>
</Platform.TabGroup>
</Platform.Provider>

The Telepresence CLI contains an embedded Helm chart. See [Install/Uninstall the Traffic Manager](manager.md) if you want to also upgrade
the Traffic Manager in your cluster.
