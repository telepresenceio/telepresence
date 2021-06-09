---
description: "How to upgrade your installation of Telepresence and install previous versions."
---

import UpgradeTabs from './upgrade-tabs'

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.

<UpgradeTabs/>

After upgrading your CLI, the Traffic Manager **must be uninstalled** from your cluster. This can be done using `telepresence uninstall --everything` or by `kubectl delete svc,deploy -n ambassador traffic-manager`. The next time you run a `telepresence` command it will deploy an upgraded Traffic Manager.
