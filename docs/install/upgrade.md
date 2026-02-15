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

diff --git a/docs/install/upgrade.md b/docs/install/upgrade.md
index bdff321946..7e8ef4a12a 100644
--- a/docs/install/upgrade.md
+++ b/docs/install/upgrade.md
@@ -7,7 +7,7 @@ hide_table_of_contents: true
 import Platform from '@site/src/components/Platform';
 
 # Upgrade Process
-The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.
+The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available. Running the same commands used for installation will replace your current version with the latest.
 
 Before upgrading your CLI, you must stop any live Telepresence processes by issuing `telepresence quit -s` (or `telepresence quit -ur`
 if your current version is less than 2.8.0).
@@ -16,28 +16,35 @@ if your current version is less than 2.8.0).
 <Platform.TabGroup>
 <Platform.MacOSTab>
 
-## Upgrade with brew:
+## Upgrade with the package installer (Recommended)
+
+Download and install the latest `.pkg` for your architecture from the [install page](client.md). The installer
+will replace the previous version and restart the root daemon service.
+
+## OR upgrade with Homebrew
+
 ```shell
 brew upgrade telepresenceio/telepresence/telepresence-oss
 ```
 
-## OR upgrade by downloading the binary for your platform
+## OR upgrade by downloading the binary manually
 
 ### Intel Macs
 
 ```shell
 # 1. Download the binary.
-sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64
+sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-amd64 -o /usr/local/bin/telepresence
 
 # 2. Make the binary executable:
 sudo chmod a+x /usr/local/bin/telepresence
 ```
 
-### ARM (Apple Silicon) Macs
+### Apple Silicon Macs
 
 ```shell
-# 1. Ensure that no old binary exists. This is very important because Silicon macs track the executable's signature
-# and just updating it in place will not work.
+# 1. Ensure that no old binary exists. This is very important because Apple Silicon macs track the executable's
+# signature and just updating it in place will not work.
+sudo rm -f /usr/local/bin/telepresence
 
 # 2. Download the binary.
 sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-darwin-arm64 -o /usr/local/bin/telepresence
@@ -48,13 +55,20 @@ sudo chmod a+x /usr/local/bin/telepresence
 </Platform.MacOSTab>
 <Platform.GNULinuxTab>
 
-```shell
+## Upgrade with a package manager (Recommended)
 
+Download and install the latest `.deb` or `.rpm` package using the same commands as the initial
+[installation](client.md). The package manager will handle replacing the previous version and restarting the
+root daemon service.
+
+## OR upgrade by downloading the binary manually
+
+```shell
 # 1. Download the latest binary (~95 MB):
-### Intel
+### AMD64
 sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-amd64 -o /usr/local/bin/telepresence
 
-### ARM
+### ARM64
 sudo curl -fL https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-linux-arm64 -o /usr/local/bin/telepresence
 
 # 2. Make the binary executable:
@@ -64,14 +78,15 @@ sudo chmod a+x /usr/local/bin/telepresence
 </Platform.GNULinuxTab>
 <Platform.WindowsTab>
 
-To upgrade Telepresence,Click [here](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-amd64.zip)
-to download the Intel Telepresence binary or [here](https://github.com/telepresenceio/telepresence/releases/latest/download/telepresence-windows-arm64.zip)
-to download the ARM Telepresence binary.
+## Upgrade with the setup installer (Recommended)
+
+Download and run the latest setup installer from the [install page](client.md). The installer will replace the
+previous version and restart the root daemon service.
 
-Once you have the binary downloaded and unzipped you will need to do a few things:
+## OR upgrade by downloading manually
 
-1. Rename the binary from `telepresence-windows-[amd64|arm64].exe` to `telepresence.exe`
-2. Move the binary to `C:\Program Files (x86)\$USER\Telepresence\`
+Download the latest zip from the [install page](client.md) and follow the manual installation steps, which will
+replace the existing installation.
 
 </Platform.WindowsTab>
 </Platform.TabGroup>
