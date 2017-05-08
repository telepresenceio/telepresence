import os
import time
from subprocess import run


def disconnect():
    # Kill off sshd server process the SSH client is talking to, forcing
    # disconnection:
    env = os.environ.copy()
    # Don't want torsocks messing with kubectl:
    for name in ["LD_PRELOAD", "DYLD_INSERT_LIBRARIES"]:
        if name in env:
            del env[name]
    # We can't tell if this succeeded, sadly, since it kills ssh session used
    # by kubectl exec!
    run([
        "kubectl", "exec",
        "--container=" + os.environ["TELEPRESENCE_CONTAINER"],
        os.environ["TELEPRESENCE_POD"], "--", "/bin/sh", "-c",
        r"kill $(ps xa | grep 'sshd: ' | " +
        r"sed 's/ *\([0-9][0-9]*\).*/\1/')"
    ],
        env=env)


if __name__ == '__main__':
    disconnect()
    time.sleep(10)
    raise SystemExit(66)  # test expects 3
