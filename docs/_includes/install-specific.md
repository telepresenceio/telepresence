
#### OS X

On OS X you can install Telepresence by running the following:

```shell
brew cask install osxfuse
brew install datawire/blackbird/telepresence
```

#### Ubuntu 16.04 or later

Run the following to install Telepresence:

```shell
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.deb.sh | sudo bash
sudo apt install --no-install-recommends telepresence
```

#### Fedora 25

Run the following:

```shell
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.rpm.sh | sudo bash
sudo dnf install telepresence
```

#### Windows

If you are running Windows 10 Creators Edition (released April 2017), you have access to the Windows Subsystem for Linux.
This allows you to run Linux programs transparently inside Windows, with access to the normal Windows filesystem.
Some older versions of Windows also had WSL, but those were based off Ubuntu 14.04 and will not work with Telepresence.

To run Telepresence inside WSL:

1. Install [Windows Subsystem for Linux](https://msdn.microsoft.com/en-us/commandline/wsl/install_guide).
2. Start the BASH.exe program.
3. Install Telepresence by following the Ubuntu instructions above.

Caveats:

* At the moment volumes are not supported on Windows, but [we plan on fixing this](https://github.com/datawire/telepresence/issues/115).
* Network proxying won't affect Windows binaries.
  You can however edit your files in Windows and compile Java or .NET packages, and then run them with the Linux interpreters or VMs.

#### Other platforms

Don't see your favorite platform?
[Let us know](https://github.com/datawire/telepresence/issues/new) and we'll try to add it. 
