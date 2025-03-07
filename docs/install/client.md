---
title: Install client
hide_table_of_contents: true
---


import Platform from '@site/src/components/Platform';

# Client Installation

Install the Telepresence client on your workstation by running the commands below for your OS.

<Platform.Provider>
<Platform.TabGroup>
<Platform.MacOSTab>

## Install with brew:
```shell
brew install telepresenceio/telepresence/telepresence-oss
```

## OR download the binary for your platform

### AMD (Intel) Macs

```shell
# 1. Download the binary.
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

### ARM (Apple Silicon) Macs

```shell
# 1. Ensure that no old binary exists. This is very important because Silicon macs track the executable's signature
# and just updating it in place will not work.
sudo rm -f /usr/local/bin/telepresence

# 2. Download the binary.
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-arm64 -o /usr/local/bin/telepresence

# 3. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```shell
# 1. Download the latest binary (~95 MB):
# AMD
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64 -o /usr/local/bin/telepresence

# ARM
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

We've developed a Powershell script to simplify the process of installing telepresence. Here are the commands you can execute:

### Windows AMD64

```powershell
# To install Telepresence, run the following commands
# from PowerShell as Administrator.

# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~50 MB):
Invoke-WebRequest https://github.com/telepresenceio/telepresence/releases/download/v2.21.3/telepresence-windows-amd64.zip -OutFile telepresence.zip

# 2. Unzip the telepresence.zip file to the desired directory, then remove the zip file:
Expand-Archive -Path telepresence.zip -DestinationPath telepresenceInstaller/telepresence
Remove-Item 'telepresence.zip'
cd telepresenceInstaller/telepresence

# 3. Run the install-telepresence.ps1 to install telepresence's dependencies. It will install telepresence to
# C:\telepresence by default, but you can specify a custom path by passing in -Path C:\my\custom\path
powershell.exe -ExecutionPolicy bypass -c " . '.\install-telepresence.ps1';"

# 4. Remove the unzipped directory:
cd ../..
Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force

# 5. Telepresence is now installed and you can use telepresence commands in PowerShell.
```

### Windows ARM64

```powershell
# To install Telepresence, run the following commands
# from PowerShell as Administrator.

# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~50 MB):
Invoke-WebRequest https://github.com/telepresenceio/telepresence/releases/download/v2.21.3/telepresence-windows-arm64.zip -OutFile telepresence.zip

# 2. Unzip the telepresence.zip file to the desired directory, then remove the zip file:
Expand-Archive -Path telepresence.zip -DestinationPath telepresenceInstaller/telepresence
Remove-Item 'telepresence.zip'
cd telepresenceInstaller/telepresence

# 3. Run the install-telepresence.ps1 to install telepresence's dependencies. It will install telepresence to
# C:\telepresence by default, but you can specify a custom path by passing in -Path C:\my\custom\path
powershell.exe -ExecutionPolicy bypass -c " . '.\install-telepresence.ps1';"

# 4. Remove the unzipped directory:
cd ../..
Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force

# 5. Telepresence is now installed and you can use telepresence commands in PowerShell.
```

</Platform.WindowsTab>
</Platform.TabGroup>

> [!TIP]
> What's Next?
> Follow one of our [quick start guides](../quick-start.md) to start using Telepresence, either with our sample app or in your own environment.

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS (including older nightly builds), replacing `x.y.z` with the versions you want.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# AMD (Intel) Macs
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-amd64

# ARM (Apple Silicon) Macs
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-arm64
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
# AMD
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64

# ARM
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```
# Windows AMD64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64.zip

# Windows ARM64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-arm64.zip
```

</Platform.WindowsTab>
</Platform.TabGroup>
</Platform.Provider>
