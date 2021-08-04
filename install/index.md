import QSTabs from '../quick-start/qs-tabs'
import OldVersionTabs from './old-version-tabs'
import NightlyVersionTabs from './nightly-version-tabs'

# Install

Install Telepresence by running the commands below for your OS.

<QSTabs/>

## <img class="os-logo" src="../images/logo.png"/> What's Next?

Follow one of our [quick start guides](../quick-start/) to start using Telepresence, either with our sample app or in your own environment.

## Installing nightly versions of Telepresence

We build and publish the contents of the default branch, [release/v2](https://github.com/telepresenceio/telepresence), of Telepresence
nightly, Monday through Friday, for macOS (Intel and M1 chips), Linux, and Windows.

The tags are formatted like so: `vX.Y.Z-nightly-$gitShortHash`.

`vX.Y.Z` is the most recent release of Telepresence with the patch version (Z) bumped one higher.
For example, if our last release was 2.3.4, nightly builds would start with v2.3.5, until a new
version of Telepresence is released.

`$gitShortHash` will be the short hash of the git commit of the build.

Use these URLs to download the most recent nightly build.

<NightlyVersionTabs/>

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS (including older nightly builds), replacing `x.y.z` with the versions you want.

<OldVersionTabs/>
