---
title: "Types of intercepts"
description: "Short demonstration of personal vs global intercepts"
---

import React from 'react';

import Alert from '@material-ui/lab/Alert';
import AppBar from '@material-ui/core/AppBar';
import Paper from '@material-ui/core/Paper';
import Tab from '@material-ui/core/Tab';
import TabContext from '@material-ui/lab/TabContext';
import TabList from '@material-ui/lab/TabList';
import TabPanel from '@material-ui/lab/TabPanel';
import Animation from '@src/components/InterceptAnimation';

export function TabsContainer({ children, ...props }) {
    const [state, setState] = React.useState({curTab: "personal"});
    React.useEffect(() => {
        const query = new URLSearchParams(window.location.search);
        var interceptType = query.get('intercept') || "regular";
        if (state.curTab != interceptType) {
            setState({curTab: interceptType});
        }
    }, [state, setState])
    var setURL = function(newTab) {
        history.replaceState(null,null,
            `?intercept=${newTab}${window.location.hash}`,
        );
    };
    return (
        <div class="TabGroup">
            <TabContext value={state.curTab}>
                <AppBar class="TabBar" elevation={0} position="static">
                    <TabList onChange={(ev, newTab) => {setState({curTab: newTab}); setURL(newTab)}} aria-label="intercept types">
                        <Tab class="TabHead" value="regular" label="No intercept"/>
                        <Tab class="TabHead" value="global" label="Intercept"/>
                    </TabList>
                </AppBar>
                {children}
            </TabContext>
        </div>
    );
};

<TabsContainer>
<TabPanel class="TabBody" value="regular">

# No intercept

<Paper style="padding: 1em">
<Animation class="mode-regular" />

This is the normal operation of your cluster without Telepresence.

</Paper>
</TabPanel>
<TabPanel class="TabBody" value="global">

# Intercept

<Paper style="padding: 1em">
<Animation class="mode-global" />

**Intercepts** replace the Kubernetes "Orders" service with the
Orders service running on your laptop.  The users see no change, but
with all the traffic coming to your laptop, you can observe and debug
with all your dev tools.

</Paper>

### Creating and using intercepts

 1. Creating the intercept: Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME
    ```

    <Alert severity="info">

    Make sure your current kubectl context points to the target
    cluster.  If your service is running in a different namespace than
    your current active context, use or change the `--namespace` flag.

    </Alert>

 2. Using the intercept: Send requests to your service:

    All requests will be sent to the version of your service that is
    running in the local development environment.

</TabPanel>
</TabsContainer>
