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

# Global intercept

<Paper>
<Animation class="mode-global" />

**Global intercepts** replace the Kubernetes "Orders" service with the
Orders service running on your laptop.  The users see no change, but
with all the traffic coming to your laptop, you can observe and debug
with all your dev tools.

</Paper>

### Creating and using global intercepts

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

</TabPanel>
<TabPanel class="TabBody" value="personal">

# Personal intercept

**Personal intercepts** allow you to be selective and intercept only
some of the traffic to a service while not interfering with the rest
of the traffic. This allows you to share a cluster with others on your
team without interfering with their work.

<Paper>
<Animation class="mode-personal" />

In the illustration above, **<span style={{color: "#f24e1e"}}>Orange</span>**
requests are being made by Developer 2 on their laptop and the
**<span style={{color: "#00c05b"}}>green</span>** are made by a teammate,
Developer 1, on a different laptop.

Each developer can intercept the Orders service for their requests only,
while sharing the rest of the development environment.

</Paper>

### Creating and using personal intercepts

1. Creating the intercept: Intercept your service from your CLI:

   ```shell
   telepresence intercept SERVICENAME --http-header=Personal-Intercept=126a72c7-be8b-4329-af64-768e207a184b
   ```

   We're using
   `Personal-Intercept=126a72c7-be8b-4329-af64-768e207a184b` as the
   header for the sake of the example, but you can use any
   `key=value` pair you want.

   <Admonition className="alert" type="info">

   Make sure your current kubect context points to the target
   cluster.  If your service is running in a different namespace than
   your current active context, use or change the `--namespace` flag.

   </Admonition>

2. Using the intercept: Send requests to your service by passing the
   HTTP header:

   ```http
   Personal-Intercept: 126a72c7-be8b-4329-af64-768e207a184b
   ```

   <Admonition className="alert" type="info">

   Need a browser extension to modify or remove an HTTP-request-headers?

   <a class="btn-sm-bluedark" href="https://chromewebstore.google.com/detail/modheader-modify-http-hea/idgpnmonknjnojddfkpgkljpfnnfcklj">Chrome</a>
   {' '}
   <a class="btn-sm-bluedark" href="https://addons.mozilla.org/en-US/firefox/addon/modify-header-value/">Firefox</a>

   </Admonition>

3. Using the intercept: Send requests to your service without the
   HTTP header:

   Requests without the header will be sent to the version of your
   service that is running in the cluster.  This enables you to share
   the cluster with a team!

### Intercepting a specific endpoint

It's not uncommon to have one service serving several endpoints. Telepresence is capable of limiting an
intercept to only affect the endpoints you want to work with by using one of the `--http-path-xxx`
flags below in addition to using `--http-header` flags. Only one such flag can be used in an intercept
and, contrary to the `--http-header` flag, it cannot be repeated.

The following flags are available:

| Flag                          | Meaning                                                          |
|-------------------------------|------------------------------------------------------------------|
| `--http-path-equal <path>`    | Only intercept the endpoint for this exact path                  |
| `--http-path-prefix <prefix>` | Only intercept endpoints with a matching path prefix             |
| `--http-path-regex <regex>`   | Only intercept endpoints that match the given regular expression |

</TabPanel>
</TabsContainer>
