---
title: Running Telepresence inside a container
hide_table_of_contents: true
---
# Running Telepresence inside a container

The `telepresence connect` command now has the option `--docker`. This option tells telepresence to start the Telepresence daemon in a
docker container.

Running the daemon in a container brings many advantages. The daemon will no longer make modifications to the host's network or DNS, and
it will not mount files in the host's filesystem. Consequently, it will not need admin privileges to run, nor will it need special software
like macFUSE or WinFSP to mount the remote file systems.

The intercept handler (the process that will receive the intercepted traffic) must also be a docker container, because that is the only
way to access the cluster network that the daemon makes available, and to mount the docker volumes needed.
