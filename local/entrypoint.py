#!/usr/bin/python3

from subprocess import Popen, STDOUT
from sys import argv

processes = []
for port in range(2000, 2020):
    # XXX need to map service name to port# somehow
    # XXX what if there is more than 20 services
    p = Popen(["kubectl", "port-forward", "telepresence", str(port)])
    processes.append(p)

for p in processes:
    p.wait()
