# Windows support

If you are running Windows 10 Creators Edition (released April 2017), you have access to the Windows Subsystem for Linux.
This allows you to run Linux programs transparently inside Windows, with access to the normal Windows filesystem.
Some older versions of Windows also had WSL, but those were based off Ubuntu 14.04 and will not work with Telepresence.

To run Telepresence inside WSL:

1. Install [Windows Subsystem for Linux](https://msdn.microsoft.com/en-us/commandline/wsl/install_guide).
2. Start the BASH.exe program.
3. Install Telepresence by following the Ubuntu instructions above.

Caveats:

* At the moment volumes are not supported on Windows, but [we plan on fixing this](https://github.com/telepresenceio/telepresence/issues/115).
* Only the `inject-tcp` method is supported.
* Network proxying won't affect Windows binaries.
  You can however edit your files in a Windows program (and compile Java or .NET packages), and then run them with the Linux interpreters or VMs.
