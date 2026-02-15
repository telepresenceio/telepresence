commit 83c993cb843567ef6267042a48f3c71a2e4ce3e6
Author: Thomas Hallgren <thomas@datawire.io>
Date:   Sun Feb 15 22:36:18 2026 +0100

    Refocus install docs on native package installers
    
    Restructure client install and upgrade documentation to recommend
    native installers (pkg/deb/rpm/wix) as the primary method, with
    Homebrew and manual binary downloads as alternatives. Add macOS
    system extension approval instructions for Tada AB, a dedicated
    uninstall section, and notes about what each install method provides.
    
    Signed-off-by: Thomas Hallgren <thomas@tada.se>

diff --git a/docs/install/client.md b/docs/install/client.md
index 45acccfef1..a91959fabc 100644
--- a/docs/install/client.md
+++ b/docs/install/client.md
@@ -14,24 +14,47 @@ Install the Telepresence client on your workstation by running the commands belo
 <Platform.TabGroup>
 <Platform.MacOSTab>
 
-## Install with brew (Recommended):
-```shell
-brew install telepresenceio/telepresence/telepresence-oss
-```
-
-## OR install using the package installer
+## Install with the package installer (Recommended)
 
-The package installer includes the root daemon as a system service via launchd, eliminating the need for elevated privileges when using Telepresence.
+The package installer sets up the root daemon as a system service via launchd, eliminating the need for elevated
+privileges when using Telepresence.
 
 Download the appropriate installer for your architecture:
 - [telepresence-darwin-amd64.pkg](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64.pkg) (Intel Macs)
 - [telepresence-darwin-arm64.pkg](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-arm64.pkg) (Apple Silicon Macs)
 
-Double-click the downloaded `.pkg` file and follow the installation prompts.
+Double-click the downloaded `.pkg` file and follow the installation prompts. You can optionally deselect the root
+daemon component during installation if you prefer to run it manually.
+
+### Allowing the system extension
+
+After installation, macOS will block the root daemon from running until you explicitly allow it. You need to:
+
+1. Open **System Settings**
+2. Go to **General > Login Items & Extensions**
+3. Find **Tada AB** in the list and enable it
+
+The macOS installer is signed and distributed by Tada AB, a Swedish company founded by the lead maintainer of the
+Telepresence open source project. This approval is required because the root daemon manages virtual network
+interfaces and DNS on your workstation, which macOS treats as a privileged operation.
+
+> [!NOTE]
+> If you skip this step, the root daemon service will not start and Telepresence will fall back to requesting
+> elevated privileges each time you run `telepresence connect`.
+
+## OR install with Homebrew
+
+```shell
+brew install telepresenceio/telepresence/telepresence-oss
+```
+
+> [!NOTE]
+> Homebrew installs the standalone binary only. The root daemon is not installed as a system service, so
+> Telepresence will request elevated privileges when connecting.
 
 ## OR download the binary manually
 
-### AMD (Intel) Macs
+### Intel Macs
 
 ```shell
 # 1. Download the binary.
@@ -41,11 +64,11 @@ sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/dow
 sudo chmod a+x /usr/local/bin/telepresence
 ```
 
-### ARM (Apple Silicon) Macs
+### Apple Silicon Macs
 
 ```shell
-# 1. Ensure that no old binary exists. This is very important because Silicon macs track the executable's signature
-# and just updating it in place will not work.
+# 1. Ensure that no old binary exists. This is very important because Apple Silicon macs track the executable's
+# signature and just updating it in place will not work.
 sudo rm -f /usr/local/bin/telepresence
 
 # 2. Download the binary.
@@ -58,9 +81,10 @@ sudo chmod a+x /usr/local/bin/telepresence
 </Platform.MacOSTab>
 <Platform.GNULinuxTab>
 
-## Install using package manager (Recommended)
+## Install using a package manager (Recommended)
 
-The package installers include the root daemon as a systemd service, eliminating the need for elevated privileges when using Telepresence.
+The package installers set up the root daemon as a systemd service, eliminating the need for elevated privileges
+when using Telepresence.
 
 ### Debian/Ubuntu (.deb)
 
@@ -108,33 +132,39 @@ journalctl -u telepresence-rootd
 
 ```shell
 # 1. Download the latest binary (~95 MB):
-# AMD
+# AMD64
 sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64 -o /usr/local/bin/telepresence
 
-# ARM
+# ARM64
 sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64 -o /usr/local/bin/telepresence
 
 # 2. Make the binary executable:
 sudo chmod a+x /usr/local/bin/telepresence
 ```
 
+> [!NOTE]
+> Installing the standalone binary does not set up the root daemon as a system service. Telepresence will
+> request elevated privileges when connecting.
+
 </Platform.GNULinuxTab>
 <Platform.WindowsTab>
 
 ## Install using the setup installer (Recommended)
 
-The setup installer includes the root daemon as a Windows service, eliminating the need for elevated privileges when using Telepresence. It also bundles WinFSP and SSHFS-Win for volume mount support.
+The setup installer sets up the root daemon as a Windows service, eliminating the need for elevated privileges
+when using Telepresence. It also bundles WinFSP and SSHFS-Win for volume mount support.
 
 Download and run the installer:
 - [telepresence-windows-amd64-setup.exe](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-amd64-setup.exe)
 
+During installation, you can optionally configure the daemon port and log level. The defaults work for most users.
+You can also deselect the "Telepresence Network Service" feature if you prefer to run the daemon manually.
+
 > [!NOTE]
 > The Windows installer is currently only available for AMD64. For ARM64, use the manual installation method below.
 
 ## OR install manually using PowerShell
 
-We've developed a Powershell script to simplify the process of installing telepresence. Here are the commands you can execute:
-
 ### Windows AMD64
 
 ```powershell
@@ -187,6 +217,11 @@ Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force
 # 5. Telepresence is now installed and you can use telepresence commands in PowerShell.
 ```
 
+> [!NOTE]
+> Manual installation does not set up the root daemon as a Windows service or install WinFSP/SSHFS-Win.
+> Telepresence will request elevated privileges when connecting, and volume mounts will not work without
+> WinFSP and SSHFS-Win installed separately.
+
 </Platform.WindowsTab>
 </Platform.TabGroup>
 
@@ -194,30 +229,83 @@ Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force
 > What's Next?
 > Follow one of our [quick start guides](../quick-start.md) to start using Telepresence, either with our sample app or in your own environment.
 
+## Uninstalling
+
+<Platform.TabGroup>
+<Platform.MacOSTab>
+
+If you installed using the package installer, run the uninstall script:
+
+```shell
+sudo telepresence-uninstall
+```
+
+This removes the Telepresence binaries and the root daemon launchd service.
+
+If you installed with Homebrew:
+
+```shell
+brew uninstall telepresenceio/telepresence/telepresence-oss
+```
+
+</Platform.MacOSTab>
+<Platform.GNULinuxTab>
+
+Use your package manager to remove Telepresence:
+
+```shell
+# Debian/Ubuntu
+sudo apt remove telepresence
+
+# Fedora/RHEL
+sudo dnf remove telepresence
+```
+
+This stops and removes the root daemon systemd service and the Telepresence binaries.
+
+</Platform.GNULinuxTab>
+<Platform.WindowsTab>
+
+Open **Settings > Apps > Installed apps**, find Telepresence, and select **Uninstall**.
+
+This removes the Telepresence binaries, the root daemon Windows service, and the bundled WinFSP and SSHFS-Win
+components.
+
+</Platform.WindowsTab>
+</Platform.TabGroup>
+
 ## Installing older versions of Telepresence
 
-Use these URLs to download an older version for your OS (including older nightly builds), replacing `x.y.z` with the versions you want.
+Use these URLs to download an older version for your OS (including older nightly builds), replacing `X.Y.Z` with the version you want.
 
 <Platform.TabGroup>
 <Platform.MacOSTab>
 
 ```shell
-# AMD (Intel) Macs
+# Intel Macs
 https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-amd64
 
-# ARM (Apple Silicon) Macs
+# Apple Silicon Macs
 https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-arm64
+
+# Package installers (available from v2.27.0)
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-amd64.pkg
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-darwin-arm64.pkg
 ```
 
 </Platform.MacOSTab>
 <Platform.GNULinuxTab>
 
 ```
-# AMD
+# AMD64
 https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64.deb
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-amd64.rpm
 
-# ARM
+# ARM64
 https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64.deb
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-linux-arm64.rpm
 ```
 
 </Platform.GNULinuxTab>
@@ -225,6 +313,7 @@ https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepres
 
 ```
 # Windows AMD64
+https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64-setup.exe
 https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepresence-windows-amd64.zip
 
 # Windows ARM64
@@ -233,4 +322,4 @@ https://github.com/telepresenceio/telepresence/releases/download/vX.Y.Z/telepres
 
 </Platform.WindowsTab>
 </Platform.TabGroup>
-</Platform.Provider>
+</Platform.Provider>
\ No newline at end of file
