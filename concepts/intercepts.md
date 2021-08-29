---
title: "Types of intercepts"
description: "Short demonstration of personal vs global intercepts"
---

import React from 'react';

import Alert from '@material-ui/lab/Alert';
import AppBar from '@material-ui/core/AppBar';
import InterceptAnimationSVG from '@src/assets/images/intercept-animation.inline.svg'
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
    const [state, setState] = React.useState({curTab: "personal"})
    return (
        <div class="TabGroup">
            <TabContext value={state.curTab}>
                <AppBar class="TabBar" elevation={0} position="static">
                    <TabList onChange={(ev, newTab) => {setState({curTab: newTab})}} aria-label="intercept types">
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

<TabsContainer>
<TabPanel class="TabBody" value="regular">

## No intercept

<Animation class="mode-regular" />

This is the normal operation of your cluster without Telepresence.

</TabPanel>
<TabPanel class="TabBody" value="global">

## Global intercept

<Animation class="mode-global" />

**Global intercepts** replace the Kubernetes "Orders" service with the
Orders service running on your laptop.  The users see no change, but
with all the traffic coming to your laptop, you can observe and debug
with all your dev tools.

 1. Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME --http-match=all
    ```

    <Alert severity="info">

    Make sure your current kubectl context points to the target
    cluster.  If your service is running in a different namespace than
    your current active context, use or change the `--namespace` flag.

    </Alert>

 2. Send requests to your service:

    All requests will be sent to the version of your service that is
    running in the local development environment.

</TabPanel>
<TabPanel class="TabBody" value="personal">

## Personal intercept

<Animation class="mode-personal" />

**Personal intercepts** allow you to be selective and intercept only
some of the traffic to a service.

**<span style="color: #f24e1e">Orange</span>** requests are being made
by a developer on their laptop and the **<span style="color:
#00c05b">green</span>** are made by a teammate on a different laptop.

They can intercept the same service in the Kubernetes cluster to
create a shared development environment.

 1. Intercept your service from your CLI:

    ```shell
    telepresence intercept SERVICENAME --http-match=Personal-Intercept=126a72c7-be8b-4329-af64-768e207a184b
    ```

    <Alert severity="info">

    Make sure your current kubect context points to the target
    cluster.  If your service is running in a different namespace than
    your current active context, use or change the `--namespace` flag.

    </Alert>

 2. Send requests to your service by passing the HTTP header:

    ```http
    Personal-Intercept: 126a72c7-be8b-4329-af64-768e207a184b
    ```

    <Alert severity="info">

    Need a browser extension to modify or remove an HTTP-request-headers?

    <a class="btn btn-black" href="https://chrome.google.com/webstore/detail/modify-header-value-http/cbdibdfhahmknbkkojljfncpnhmacdek/">Chrome</a>
    {' '}
    <a class="btn btn-black" href="https://addons.mozilla.org/en-US/firefox/addon/modify-header-value/">Firefox</a>

    </Alert>

 3. Send requests to your service without the HTTP header:

    Requests without the header will be sent to the version of your
    service that is running in the cluster.  This enables you to share
    the cluster with a team!

</TabPanel>
</TabsContainer>
