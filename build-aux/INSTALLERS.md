# Telepresence Installers Specification

This document describes the platform-specific installers that bundle Telepresence with a system service for the root daemon.

## Overview

Telepresence provides two installation methods:
1. **Standalone binaries** - Manual installation, root daemon runs on-demand with elevated privileges
2. **Platform installers** - Include root daemon as a system service, eliminating repeated privilege elevation

All installers are built and published via `.github/workflows/release.yaml`.

## Windows Installer (WiX)

**Location:** `build-aux/wix-installer/`

**Output:** `telepresence-windows-amd64-setup.exe` (WiX bundle/bootstrapper)

**Architecture:** amd64 only (WinFSP and SSHFS-Win installers lack arm64 versions)

### Key Files
- `Makefile` - Build orchestration, downloads binary if not present locally
- `TeleProduct.wxs` - MSI product definition
- `TeleBundle.wxs` - Bundle/bootstrapper definition
- `variables.wxi` - Version and path variables

### CI Build Steps
```yaml
- name: Install WiX Toolset
  run: |
    dotnet tool install --global wix
    wix extension add -g WixToolset.BootstrapperApplications.wixext
    wix extension add -g WixToolset.UI.wixext
    wix extension add -g WixToolset.Util.wixext
- name: Build WiX Installer
  run: make bundle ARCH=amd64
```

### Service Details
The Windows installer registers Telepresence and configures PATH. The root daemon service management is handled differently on Windows (not a persistent service like Unix platforms).

---

## macOS Installer (pkg)

**Location:** `build-aux/pkg-installer/`

**Output:** `telepresence-darwin-{amd64,arm64}.pkg`

**Architecture:** amd64 and arm64

### Key Files
- `build-pkg.sh` - Build script using `pkgbuild` and `productbuild`
- `io.telepresence.rootd.plist` - launchd daemon configuration
- `scripts/postinstall` - Post-installation script
- `scripts/preinstall` - Pre-installation script
- `distribution.xml` - Installer UI/flow definition

### CI Build Steps
```yaml
- name: Build macOS Installer
  run: |
    cd build-aux/pkg-installer
    VERSION="${TELEPRESENCE_VERSION#v}" ARCH=${{ matrix.arch }} ./build-pkg.sh
```

### launchd Service (`io.telepresence.rootd.plist`)
```xml
<key>ProgramArguments</key>
<array>
  <string>/usr/local/bin/telepresence</string>
  <string>rootd</string>
  <string>--logfile</string>  <string>std</string>
  <string>--config</string>   <string>/Library/Application Support/telepresence/config.yml</string>
  <string>--address</string>  <string>:4037</string>
  <string>--managed</string>
</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
```

**Log location:** `/var/log/telepresence-rootd.log`
**Working directory:** `/Library/Caches/telepresence`

---

## Linux Installer (nfpm)

**Location:** `build-aux/systemd-installer/`

**Output:**
- `telepresence-{version}-linux-{amd64,arm64}.deb` (Debian/Ubuntu)
- `telepresence-{version}-linux-{amd64,arm64}.rpm` (Fedora/RHEL)

**Architecture:** amd64 and arm64

### Key Files
- `build-packages.sh` - Build script, handles version parsing and nfpm invocation
- `nfpm.yaml.in` - nfpm configuration template (uses envsubst for variable expansion)
- `telepresence-rootd.service` - systemd unit file
- `postinstall.sh` - Post-installation script (creates directories, enables service)
- `preremove.sh` - Pre-removal script (stops and disables service)
- `postremove.sh` - Post-removal script (cleans up directories)

### CI Build Steps
```yaml
- name: Install nfpm
  run: |
    curl -sfL "https://github.com/goreleaser/nfpm/releases/download/v2.44.1/nfpm_2.44.1_Linux_x86_64.tar.gz" | tar xz -C /tmp
    sudo mv /tmp/nfpm /usr/local/bin/nfpm
- name: Build Linux Packages
  run: |
    cd build-aux/systemd-installer
    VERSION="${TELEPRESENCE_VERSION#v}" ARCH=${{ matrix.arch }} ./build-packages.sh
```

### Version Handling
RPM doesn't allow hyphens in version numbers. The build script splits versions:
- `2.27.0` → version=`2.27.0`, release=`1`
- `2.27.0-rc.1` → version=`2.27.0`, release=`rc.1`
- `2.27.0-test.5` → version=`2.27.0`, release=`test.5`

### nfpm.yaml.in Template
```yaml
name: telepresence
arch: ${ARCH}
platform: linux
version: ${PKG_VERSION}
release: ${PKG_RELEASE}
maintainer: Telepresence Maintainers <telepresence@datawire.io>
description: Fast local Kubernetes development
vendor: Ambassador Labs
homepage: https://www.telepresence.io/
license: Apache-2.0

contents:
  - src: ${BINDIR}/telepresence
    dst: /usr/local/bin/telepresence
  - src: telepresence-rootd.service
    dst: /usr/lib/systemd/system/telepresence-rootd.service

scripts:
  postinstall: postinstall.sh
  preremove: preremove.sh
  postremove: postremove.sh
```

### systemd Service (`telepresence-rootd.service`)
```ini
[Service]
Type=simple
ExecStart=/usr/local/bin/telepresence rootd \
    --logfile managed \
    --config /etc/telepresence/config.yml \
    --address :4037 \
    --managed

User=root
Group=root
WorkingDirectory=/var/cache/telepresence
RuntimeDirectory=telepresence
Restart=always
RestartSec=3

# Logging
StandardOutput=append:/var/log/telepresence/rootd.log
StandardError=append:/var/log/telepresence/rootd.log

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/cache/telepresence /var/log/telepresence
ProtectHome=read-only
PrivateTmp=yes
```

### Directory Structure
| Path | Permissions | Purpose |
|------|-------------|---------|
| `/var/cache/telepresence` | 755 | Working directory |
| `/var/cache/telepresence/rootd` | 755 | Root daemon cache |
| `/var/log/telepresence` | 755 | Log directory |
| `/etc/telepresence` | 755 | Configuration |

### Post-install Behavior
- Creates required directories with 755 permissions
- Runs `systemctl daemon-reload`
- Enables service (`systemctl enable telepresence-rootd`)
- Does NOT start service (user decides when to start)

---

## Root Daemon Communication

All platforms use TCP for client-daemon communication:
- **Address:** `:4037` (localhost port 4037)
- **Flag:** `--address :4037`

The `--managed` flag indicates the daemon is running as a system service.

---

## Release Notes Structure

The GitHub release body separates installer types:

**Installers (with root daemon as a system service):**
- Linux .deb/.rpm
- macOS .pkg
- Windows setup.exe

**Standalone Binaries:**
- Linux/macOS executables
- Windows .zip
