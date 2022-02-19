---
title: "Types of intercepts"
description: "Short demonstration of personal vs global intercepts"
---

import React from 'react';

import Alert from '@material-ui/lab/Alert';
import AppBar from '@material-ui/core/AppBar';
import InterceptAnimationSVG from '@src/assets/images/intercept-animation.inline.svg'
import Paper from '@material-ui/core/Paper';
import Tab from '@material-ui/core/Tab';
import TabContext from '@material-ui/lab/TabContext';
import TabList from '@material-ui/lab/TabList';
import TabPanel from '@material-ui/lab/TabPanel';

export function Animation(props) {
    let el = React.useRef(null);
    React.useEffect(() => {
        const queueAnimation = () => {
            setTimeout(() => {
                el.current?.getAnimations({subtree: true})?.forEach((anim) => {
                    anim.finish();
                    anim.play();
                })
                queueAnimation();
            }, 3000);
        };
        queueAnimation();
    }, el);
    return (
        <div ref={el} style="text-align: center">
            <InterceptAnimationSVG style="max-width: 700px" {...props} />
        </div>
    );
};

export function TabsContainer({ children, ...props }) {
    const [state, setState] = React.useState({curTab: "personal"});
    React.useEffect(() => {
        const query = new URLSearchParams(window.location.search);
        var interceptType = query.get('intercept') || "personal";
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
                        <Tab class="TabHead" value="global" label="Global intercept"/>
                        <Tab class="TabHead" value="personal" label="Personal intercept"/>
                    </TabList>
                </AppBar>
                {children}
            </TabContext>
        </div>
    );
};

# Types of intercepts

<TabsContainer>
<TabPanel class="TabBody" value="regular">

# No intercept

<Paper style="padding: 1em">
<Animation class="mode-regular" />

This is the normal operation of your cluster without Telepresence.

</Paper>
</TabPanel>
<TabPanel class="TabBody" value="global">

# Global intercept

<Paper style="padding: 1em">
<Animation class="mode-global" />

**Global intercepts** replace the Kubernetes "Orders" service with the
Orders service running on your laptop.  The users see no change, but
with all the traffic coming to your laptop, you can observe and debug
with all your dev tools.

</Paper>

### Creating and using global intercepts

 1. Creating the intercept: Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME --http-match=all
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
<TabPanel class="TabBody" value="personal">

# Personal intercept

**Personal intercepts** allow you to be selective and intercept only
some of the traffic to a service while not interfering with the rest
of the traffic. This allows you to share a cluster with others on your
team without interfering with their work.

<Paper style="padding: 1em">
<Animation class="mode-personal" />

In the illustration above, **<span style="color: #f24e1e">Orange</span>**
requests are being made by Developer 2 on their laptop and the
**<span style="color: #00c05b">green</span>** are made by a teammate,
Developer 1, on a different laptop.

Each developer can intercept the Orders service for their requests only,
while sharing the rest of the development environment.

</Paper>

### Creating and using personal intercepts

 1. Creating the intercept: Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME --http-match=Personal-Intercept=126a72c7-be8b-4329-af64-768e207a184b
    ```

    We're using
    `Personal-Intercept=126a72c7-be8b-4329-af64-768e207a184b` as the
    header for the sake of the example, but you can use any
    `key=value` pair you want, or `--http-match=auto` to have it
    choose something automatically.

    <Alert severity="info">

    Make sure your current kubect context points to the target
    cluster.  If your service is running in a different namespace than
    your current active context, use or change the `--namespace` flag.

    </Alert>

 2. Using the intercept: Send requests to your service by passing the
    HTTP header:

    ```http
    Personal-Intercept: 126a72c7-be8b-4329-af64-768e207a184b
    ```

    <Alert severity="info">

    Need a browser extension to modify or remove an HTTP-request-headers?

    <a class="btn-sm-bluedark" href="https://chrome.google.com/webstore/detail/modheader/idgpnmonknjnojddfkpgkljpfnnfcklj">Chrome</a>
    {' '}
    <a class="btn-sm-bluedark" href="https://addons.mozilla.org/firefox/addon/modheader-firefox/">Firefox</a>

    </Alert>

 3. Using the intercept: Send requests to your service without the
    HTTP header:

    Requests without the header will be sent to the version of your
    service that is running in the cluster.  This enables you to share
    the cluster with a team!

### Intercepting a specific endpoint

It's not uncommon to have one service serving several endpoints. Telepresence is capable of limiting an
intercept to only affect the endpoints you want to work with by using one of the `--http-path-xxx`
flags below in addition to using `--http-match` flags. Only one such flag can be used in an intercept
and, contrary to the `--http-match` flag, it cannot be repeated.

The following flags are available:

| Flag                          | Meaning                                                          |
|-------------------------------|------------------------------------------------------------------|
| `--http-path-equal <path>`    | Only intercept the endpoint for this exact path                  |
| `--http-path-prefix <prefix>` | Only intercept endpoints with a matching path prefix             |
| `--http-path-regex <regex>`   | Only intercept endpoints that match the given regular expression |

</TabPanel>
</TabsContainer>
