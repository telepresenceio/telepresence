#!/usr/bin/python3

from subprocess import Popen, STDOUT
from sys import argv

from connect import get_services


processes = []
local_port = 2000
for name, ip, port in get_services():
    # XXX this is missing code to map service to its pods, since port-forward is for a *pod*.
    p = Popen(["kubectl", "port-forward", name, "{}:{}".format(local_port, port)])
    local_port += 1
    processes.append(p)
for p in processes:
    p.wait()
