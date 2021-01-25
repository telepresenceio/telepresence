---
description: "Telepresence uses Preview URLs to help you collaborate on developing Kubernetes services with teammates."
---

# Collaboration with Preview URLs

For collaborating on development work, Telepresence generates preview URLs that you can share with your teammate or collaborators outside of our organization. This opens up new possibilities for real time development, debugging, and pair programming among increasingly distributed teams.

Preview URLs are protected behind authentication via Ambassador Cloud, ensuring that only users in your organization can view them.  A preview URL can also be set to allow public access, for sharing with outside collaborators.

## Prerequisites

You must have an active intercept running to your cluster with the intercepted service running on your laptop.

Sharing a preview URL with a teammate requires you both be members of the same GitHub organization.

> More methods of authentication will be available in future Telepresence releases, allowing for collaboration via other service organizations.

## Sharing a Preview URL (With Teammates)

You can collaborate with teammates by sending your preview URL to them via Slack or however you communicate. They will be asked to authenticate via GitHub if they are not already logged into Ambassador Cloud. When they visit the preview URL, they will see the intercepted service running on your laptop. Your laptop must be online and running the service for them to see the live intercept.

## Sharing a Preview URL (With Outside Collaborators)

To collaborate with someone outside of your GitHub organization, you must go to the Ambassador Cloud dashboard (run `telepresence dashboard` to reopen it), select the preview URL, and click **Make Publicly Accessible**.  Now anyone with the link will have access to the preview URL. When they visit the preview URL, they will see the intercepted service running on your laptop. Your laptop must be online and running the service for them to see the live intercept.

To disable sharing the preview URL publicly, click **Require Authentication** in the dashboard. Removing the intercept either from the dashboard or by running `telepresence leave <service>` also removes all access to the preview URL.