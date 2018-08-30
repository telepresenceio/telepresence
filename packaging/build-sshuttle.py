#!/usr/bin/env python3
"""
Create a standalone sshuttle.

We use a particular commit off of upstream master since at the moment there is
no release with the feature we want (as of July 18, 2017). Once a new release
is made we can pin that.

For now we have a fork with a branch; hope is to upstream our changes
eventually.
"""

import sys
from pathlib import Path
from subprocess import check_call, check_output
from tempfile import TemporaryDirectory


def build_sshuttle(output: Path):
    """
    Build an sshuttle-telepresence executable using Pex
    """
    with TemporaryDirectory() as temp_name:
        build = Path(temp_name)

        # Grab correct sshuttle source code as an sdist tarball
        code = build / "sshuttle"
        check_call([
            "git", "clone", "-q", "https://github.com/datawire/sshuttle.git",
            str(code)
        ])
        check_call(["git", "checkout", "-q", "telepresence"], cwd=str(code))
        check_call(["python3", "setup.py", "-q", "sdist"], cwd=str(code))
        version = str(
            check_output(["python3", "setup.py", "--version"],
                         cwd=str(code)).strip(), "ascii"
        )
        tarball = code / "dist" / "sshuttle-telepresence-{}.tar.gz".format(
            version
        )
        assert tarball.exists(), str(tarball)

        # Set up Pex in a one-off virtualenv
        check_call(["python3", "-m", "venv", str(build / "venv")])
        check_call([str(build / "venv/bin/pip"), "-q", "install", "pex"])

        # Use Pex to build the executable
        check_call([
            str(build / "venv/bin/pex"),
            "--python-shebang=/usr/bin/env python3",
            "--script=sshuttle-telepresence",
            "--output-file={}".format(output),
            tarball,
        ])

    print("Built {}".format(output))


def main():
    """
    Set things up then call the code that builds the executable.
    """
    if len(sys.argv) > 1:
        output = Path(sys.argv[1])
    else:
        project = Path(__file__).absolute().resolve().parent.parent
        output = project / "dist" / "sshuttle-telepresence"

    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        output.unlink()

    build_sshuttle(output)


if __name__ == "__main__":
    main()
