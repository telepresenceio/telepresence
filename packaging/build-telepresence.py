#!/usr/bin/env python3
"""
Create a standalone telepresence
"""

import sys
from io import BytesIO
from pathlib import Path
from subprocess import check_output
from tempfile import TemporaryDirectory
from zipapp import create_archive
from zipfile import ZIP_DEFLATED, ZipFile


def make_compressed_zipapp(source: Path, output: Path, main_function: str):
    """
    Make an executable compressed zipfile from Python code
    """
    # Build uncompresed zip file
    uncompressed = BytesIO()
    create_archive(source, uncompressed, main=main_function)
    uncompressed.seek(0, 0)

    # Build compressed zip file
    compressed = BytesIO()
    in_zip = ZipFile(uncompressed)
    with ZipFile(compressed, mode="w") as out_zip:
        for info in in_zip.infolist():
            out_zip.writestr(info, in_zip.read(info), ZIP_DEFLATED)
    compressed.seek(0, 0)

    # Build final zip file
    create_archive(compressed, str(output), interpreter="/usr/bin/env python3")
    output.chmod(0o755)  # Seems to default to 744 on CircleCI


def build_telepresence(project: Path, output: Path):
    """
    Build a telepresence executable zipfile
    """
    with TemporaryDirectory() as temp:
        # Run setup.py build to extract source code. Versioneer will replace
        # _version.py with hard-coded version info.
        check_output(["python3", "-Wignore", "setup.py", "build", "-b", temp],
                     cwd=str(project))
        # Build zip app
        make_compressed_zipapp(
            Path(temp) / "lib", output, "telepresence.main:run_telepresence"
        )

    print("Built {}".format(output))


def main():
    """
    Set things up then call the code that builds the executable.
    """
    project = Path(__file__).absolute().resolve().parent.parent

    if len(sys.argv) > 1:
        output = Path(sys.argv[1])
    else:
        version_bytes = check_output(
            ["python3", "-Wignore", "setup.py", "--version"],
            cwd=str(project),
        )
        version = str(version_bytes, "utf-8").strip()

        output = project / "dist" / "telepresence-{}".format(version)

    output.parent.mkdir(parents=True, exist_ok=True)
    if output.exists():
        output.unlink()

    build_telepresence(project, output)


if __name__ == "__main__":
    main()
