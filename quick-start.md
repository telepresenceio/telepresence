---
description: "Get started with Telepresence with installation instructions."
---

import HubspotForm from 'react-hubspot-form';

# Telepresence Quick Start

Run the command below to install for your OS then follow the [tutorial](../tutorial) to learn how to use Telepresence.

### <img class="os-logo" src="../../images/apple.png"/> macOS

```
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/latest/telepresence \
-o /usr/local/bin/telepresence && \
sudo chmod a+x /usr/local/bin/telepresence && \
telepresence version
```
> If you receive an error saying the developer cannot be verified, open **System Preferences → Security & Privacy → General**.  Click **Open Anyway** at the bottom to bypass the security block. Then retry the `telepresence version` command.


### <img class="os-logo" src="../../images/linux.png"/> Linux

```
sudo curl -fL https://app.getambassador.io/download/tel2/linux/amd64/latest/telepresence \
-o /usr/local/bin/telepresence && \
sudo chmod a+x /usr/local/bin/telepresence && \
telepresence version
```

## <img class="os-logo" src="../../images/logo.png"/> What's Next?

Follow the [tutorial](../tutorial/) to learn how to use Telepresence.

### <img class="os-logo" src="../../images/windows.png"/> Windows

Telepresence for Windows is coming soon, sign up here to notified when it is available. <br/>
 <HubspotForm
          portalId='485087'
          formId='2f542f1b-3da8-4319-8057-96fed78e4c26'
          onSubmit={() => console.log('Submit!')}
          onReady={(form) => console.log('Form ready!')}
          loading={<div>Loading...</div>}
        />