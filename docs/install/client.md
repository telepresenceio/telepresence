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

## Install with the package installer (Recommended)

The package installer sets up the root daemon as a system service via launchd, eliminating the need for elevated
privileges when using Telepresence.

Download the appropriate installer for your architecture:
- [telepresence-darwin-amd64.pkg](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64.pkg) (Intel Macs)
- [telepresence-darwin-arm64.pkg](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-arm64.pkg) (Apple Silicon Macs)

Double-click the downloaded `.pkg` file and follow the installation prompts. You can optionally deselect the root
daemon component during installation if you prefer to run it manually.

### Allowing the system extension

After installation, macOS will block the root daemon from running until you explicitly allow it. You need to:

1. Open **System Settings**
2. Go to **General > Login Items & Extensions**
3. Find **Tada AB** in the list and enable it

The macOS installer is signed and distributed by Tada AB, a Swedish company founded by the lead maintainer of the
Telepresence open source project. This approval is required because the root daemon manages virtual network
interfaces and DNS on your workstation, which macOS treats as a privileged operation.

> [!NOTE]
> If you skip this step, the root daemon service will not start and Telepresence will fall back to requesting
> elevated privileges each time you run `telepresence connect`.

## OR install with Homebrew

```shell
brew install telepresenceio/telepresence/telepresence-oss
```

> [!NOTE]
> Homebrew installs the standalone binary only. The root daemon is not installed as a system service, so
> Telepresence will request elevated privileges when connecting.

## OR download the binary manually

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

## Install using a package manager (Recommended)

The package installers set up the root daemon as a systemd service, eliminating the need for elevated privileges
when using Telepresence.

### Debian/Ubuntu (.deb)

```shell
# Download the latest .deb package
# AMD64
curl -fLO https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64.deb
# ARM64
curl -fLO https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64.deb

# Install the package
sudo apt install ./telepresence-linux-*.deb
```

### Fedora/RHEL (.rpm)

```shell
# Download the latest .rpm package
# AMD64
curl -fLO https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64.rpm
# ARM64
curl -fLO https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64.rpm

# Install the package
sudo dnf install ./telepresence-linux-*.rpm
```

### Viewing service logs

The root daemon service logs to the systemd journal:

```shell
journalctl -u telepresence-rootd
```

> [!NOTE]
> If you get a permission error, your user needs to be in a group that can read the journal.
> On Fedora/RHEL, add yourself to the `wheel` group. On Debian/Ubuntu, use `systemd-journal` or `adm`:
> ```shell
> sudo usermod -aG systemd-journal $USER
> ```
> Log out and back in for the change to take effect.

## OR download the binary manually

```shell
# 1. Download the latest binary (~95 MB):
# AMD64
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64 -o /usr/local/bin/telepresence

# ARM64
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

> [!NOTE]
> Installing the standalone binary does not set up the root daemon as a system service. Telepresence will
> request elevated privileges when connecting.

</Platform.GNULinuxTab>
<Platform.WindowsTab>

## Install using the MSI (Recommended)

The MSI installs `telepresence.exe`, sets up the root daemon as a Windows service (eliminating the need for
elevated privileges when using Telepresence), adds Telepresence to the system PATH, and installs
[wintun](https://www.wintun.net/) for the VIF. You can deselect the "Telepresence Network Service" feature
during installation if you prefer to run the daemon manually.

Download and run the installer for your architecture:
- [telepresence-windows-amd64.msi](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-amd64.msi)
- [telepresence-windows-arm64.msi](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-arm64.msi)

During installation, you can optionally configure the daemon port and log level. The defaults work for most users.

The MSI does not install [WinFsp](https://winfsp.dev/rel/) or
[SSHFS-Win](https://github.com/winfsp/sshfs-win/releases); install both separately if you need volume mounts.
WinFsp publishes an arm64 build; SSHFS-Win offers only an x64 build, which runs under emulation on Windows on
ARM but is untested there. The installer warns about missing prerequisites unless it is run silently.

For Intune or Group Policy deployment, push all three packages and run the MSI with
`msiexec /i telepresence-windows-amd64.msi /qn` (or the arm64 MSI on ARM devices).

## OR install manually using PowerShell

### Windows AMD64

```powershell
# To install Telepresence, run the following commands
# from PowerShell as Administrator.

# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~60 MB):
$ProgressPreference = 'SilentlyContinue'
Invoke-WebRequest https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-amd64.zip -OutFile telepresence.zip

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

# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~60 MB):
$ProgressPreference = 'SilentlyContinue'
Invoke-WebRequest https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-arm64.zip -OutFile telepresence.zip

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

> [!NOTE]
> Manual installation does not set up the root daemon as a Windows service or install WinFSP/SSHFS-Win.
> Telepresence will request elevated privileges when connecting, and volume mounts will not work without
> WinFSP and SSHFS-Win installed separately.

## Verifying the download

`telepresence.exe` and both MSIs are Authenticode-signed. Check a file's signature with PowerShell:

```powershell
Get-AuthenticodeSignature .\telepresence.exe | Format-List Status, SignerCertificate, TimeStamperCertificate
Get-AuthenticodeSignature .\telepresence-windows-amd64.msi | Format-List Status, SignerCertificate, TimeStamperCertificate
Get-AuthenticodeSignature .\telepresence-windows-arm64.msi | Format-List Status, SignerCertificate, TimeStamperCertificate
```

`Status` must read `Valid`. The signer certificate is issued through the SignPath Foundation; admins can
write an allow rule (for example in AppLocker or WDAC) against the signer certificate's subject shown by
`SignerCertificate`. The same information is available without PowerShell: right-click the file, choose
**Properties**, and open the **Digital Signatures** tab.

## Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io/), certificate by
[SignPath Foundation](https://signpath.org/). The Windows release binaries and installers are
Authenticode-signed with it.

- Committers and reviewers: [telepresence-maintainers](https://github.com/orgs/telepresenceio/teams/telepresence-maintainers) ([@thallgren](https://github.com/thallgren), [@bgruszka](https://github.com/bgruszka), [@njayp](https://github.com/njayp), [@breland-openai](https://github.com/breland-openai))
- Approvers: [administrators](https://github.com/orgs/telepresenceio/teams/administrators) ([@khussey](https://github.com/khussey), [@thallgren](https://github.com/thallgren))
- Privacy policy: [PRIVACY.md](https://github.com/telepresenceio/telepresence/blob/release/v2/PRIVACY.md)

</Platform.WindowsTab>
</Platform.TabGroup>

> [!TIP]
> What's Next?
> Follow one of our [quick start guides](../quick-start.md) to start using Telepresence, either with our sample app or in your own environment.

## Uninstalling

<Platform.TabGroup>
<Platform.MacOSTab>

If you installed using the package installer, run the uninstall script:

```shell
sudo telepresence-uninstall
```

This removes the Telepresence binaries and the root daemon launchd service.

If you installed with Homebrew:

```shell
brew uninstall telepresenceio/telepresence/telepresence-oss
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

Use your package manager to remove Telepresence:

```shell
# Debian/Ubuntu
sudo apt remove telepresence

# Fedora/RHEL
sudo dnf remove telepresence
```

This stops and removes the root daemon systemd service and the Telepresence binaries.

</Platform.GNULinuxTab>
<Platform.WindowsTab>

Open **Settings > Apps > Installed apps**, find Telepresence, and select **Uninstall**.

This removes the Telepresence binaries and the root daemon Windows service. WinFsp and SSHFS-Win, if
installed, must be uninstalled separately.

</Platform.WindowsTab>
</Platform.TabGroup>

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS (including older nightly builds), replacing `X.Y.Z` with the version you want.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-amd64

# Apple Silicon Macs
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-arm64

# Package installers (available from v2.27.0)
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-amd64.pkg
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-arm64.pkg
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
# AMD64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64.deb
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64.rpm

# ARM64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64.deb
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64.rpm
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```
# Windows AMD64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64.zip
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64.msi
# MSI installer, available from v2.32.0
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64-setup.exe
# setup installer, available from v2.27.0 through v2.31.x

# Windows ARM64
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-arm64.zip
https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-arm64.msi
# MSI installer, available from v2.32.0
```

</Platform.WindowsTab>
</Platform.TabGroup>
</Platform.Provider>