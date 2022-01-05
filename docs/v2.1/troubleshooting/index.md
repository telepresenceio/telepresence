---
description: "Troubleshooting issues related to Telepresence."
---
# Troubleshooting

## Creating an Intercept Did Not Generate a Preview URL

Preview URLs are only generated when you are logged into Ambassador Cloud, so that you can use it to manage all your preview URLs. When not logged in, the intercept will not generate a preview URL and will proxy all traffic.  Remove the intercept with `telepresence leave [deployment name]`, run `telepresence login` to login to Ambassador Cloud, then recreate the intercept. See the [intercepts how-to doc](../howtos/intercepts) for more details.

## Error on Accessing Preview URL: `First record does not look like a TLS handshake`

The service you are intercepting is likely not using TLS, however when configuring the intercept you indicated that it does use TLS. Remove the intercept with `telepresence leave [deployment name]` and recreate it, setting `TLS` to `n`. Telepresence tries to intelligently determine these settings for you when creating an intercept and offer them as defaults, but odd service configurations might cause it to suggest the wrong settings.

## Error on Accessing Preview URL: Detected a 301 Redirect Loop

If your ingress is set to redirect HTTP requests to HTTPS and your web app uses HTTPS, but you configure the intercept to not use TLS, you will get this error when opening the preview URL.  Remove the intercept with `telepresence leave [deployment name]` and recreate it, selecting the correct port and setting `TLS` to `y` when prompted.

## Your GitHub Organization Isn't Listed

Ambassador Cloud needs access granted to your GitHub organization as a third-party OAuth app. If an org isn't listed during login then the correct access has not been granted.

The quickest way to resolve this is to go to the **Github menu** → **Settings** → **Applications** → **Authorized OAuth Apps** → **Ambassador Labs**. An org owner will have a **Grant** button, anyone not an owner will have **Request** which sends an email to the owner. If an access request has been denied in the past the user will not see the **Request** button, they will have to reach out to the owner.

Once access is granted, log out of Ambassador Cloud and log back in, you should see the GitHub org listed.

The org owner can go to the **GitHub menu** → **Your organizations** → **[org name]** → **Settings** → **Third-party access** to see if Ambassador Labs has access already or authorize a request for access (only owners will see **Settings** on the org page). Clicking the pencil icon will show the permissions that were granted.

GitHub's documentation provides more detail about [managing access granted to third-party applications](https://docs.github.com/en/github/authenticating-to-github/connecting-with-third-party-applications) and [approving access to apps](https://docs.github.com/en/github/setting-up-and-managing-organizations-and-teams/approving-oauth-apps-for-your-organization).

### Granting or Requesting Access on Initial Login

When using GitHub as your identity provider, the first time you login to Ambassador Cloud GitHub will ask to authorize Ambassador Labs to access your orgs and certain user data.

<img src="../images/github-login.png" width="50%"/>

Any listed org with a green check has already granted access to Ambassador Labs (you still need to authorize to allow Ambassador Labs to read your user data and org membership).

Any org with a red X requires access to be granted to Ambassador Labs.  Owners of the org will see a **Grant** button. Anyone who is not an owner will see a **Request** button. This will send an email to the org owner requesting approval to access the org. If an access request has been denied in the past the user will not see the **Request** button, they will have to reach out to the owner.

Once approval is granted, you will have to log out of Ambassador Cloud then back in to select the org.

