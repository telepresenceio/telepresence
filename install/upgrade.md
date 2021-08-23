---
description: "How to upgrade your installation of Telepresence and install previous versions."
---

import UpgradeTabs from './upgrade-tabs'

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.

<UpgradeTabs/>

After upgrading your CLI you must stop any live Telepresence processes by issuing `telepresence quit`, then upgrade the Traffic Manager by running `telepresence connect`
