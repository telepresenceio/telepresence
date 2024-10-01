---
title: "Intercepts"
description: "Short demonstration of global intercepts"
hide_table_of_contents: true
---

import Admonition from '@theme/Admonition';
import Paper from '@mui/material/Paper';
import Tab from '@mui/material/Tab';
import TabContext from '@mui/lab/TabContext';
import TabList from '@mui/lab/TabList';
import TabPanel from '@mui/lab/TabPanel';
import TabsContainer from '@site/src/components/TabsContainer';
import Animation from '@site/src/components/InterceptAnimation';

<TabsContainer>
<TabPanel className="TabBody" value="regular">

# No intercept

<Paper className="interceptTab">
<Animation className="mode-regular" />

This is the normal operation of your cluster without Telepresence.

</Paper>
</TabPanel>
<TabPanel className="TabBody" value="global">

<Paper className="interceptTab">

# Intercept

<Animation className="mode-global" />

**Intercepts** replace the Kubernetes "Orders" service with the
Orders service running on your laptop.  The users see no change, but
with all the traffic coming to your laptop, you can observe and debug
with all your dev tools.

### Creating and using intercepts

 1. Creating the intercept: Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME
    ```

    <Admonition className="alert" type="info">

    Make sure your current kubectl context points to the target
    cluster.  If your service is running in a different namespace than
    your current active context, use or change the `--namespace` flag.

    </Admonition>

 2. Using the intercept: Send requests to your service:

    All requests will be sent to the version of your service that is
    running in the local development environment.

</Paper>
</TabPanel>
</TabsContainer>
