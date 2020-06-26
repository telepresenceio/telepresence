#!/usr/bin/env python3

import re
import shutil
import subprocess
import sys

from pathlib import Path
from shlex import quote


def spew(*args, **kwargs):
    "Print to stderr"
    print(*args, file=sys.stderr, **kwargs)


def main():
    "Build the website"

    # Source and output directories
    project = Path(__file__).absolute().resolve().parent.parent
    docs = project / "docs"
    out = docs / "_book"
    shutil.rmtree(out, ignore_errors=True)

    # Grab the current version in some way.
    # Netlify's Python setup makes this harder than it should be...
    version_commands = (
        ["python3", "-Wignore", "setup.py", "--version"],
        ["python3.6", "-Wignore", "setup.py", "--version"],
        [
            "python3", "-c",
            "import telepresence; print(telepresence.__version__)"
        ],
        ["git", "describe", "--tags"],
        ["make", "version"],
    )
    for cmd in version_commands:
        try:
            spew("Trying: {}".format(" ".join(quote(arg) for arg in cmd)))
            version_cp = subprocess.run(
                cmd,
                cwd=str(project),
                check=True,
                stdout=subprocess.PIPE,
            )
        except (subprocess.CalledProcessError, OSError):
            spew(" ... failed")
            continue
        version = str(version_cp.stdout, "utf-8").strip()
        spew("\nFound version {}".format(version))
        break
    else:
        raise RuntimeError("Failed to determine version number")

    # Try to roll back unreleased version number to prior release number
    if "-" in version:
        version = version[:version.index("-")]
        spew("Using version {}".format(version))
    spew()

    # Build book.json, substituting the current version into the template
    book_json = (docs / "book.json.in").read_text()
    book_json = book_json.replace("{{ VERSION }}", version)
    (docs / "book.json").write_text(book_json)

    # Run GitBook
    shutil.rmtree(docs / "node_modules", ignore_errors=True)
    run = lambda command: subprocess.run(command, check=True, cwd=docs)
    run(["npm", "install"])
    run(["npm", "run", "build"])
    assert out.exists(), out

    # Remove the data-path attributed of every list item linking to index.html,
    # which are the ones marked with data-level="1.1". This causes the GitBook
    # scripts to redirect to the index page rather fetching and replacing just
    # the content area, as they do for proper GitBook-generated pages.
    # (Replacements done in-place in the output directory)
    filenames = list(out.rglob("*.html"))
    filenames.append(out / "search_index.json")
    pattern = r'<li class="chapter " data-level="1.1" data-path="[^"]*">'
    replacement = '<li class="chapter " data-level="1.1">'
    replace = re.compile(pattern).sub
    for filename in filenames:
        original = filename.read_text()
        transformed = replace(replacement, original)
        filename.write_text(transformed)

    # Replace the generated index.html with our hand-crafted landing page.
    # Insert the current version number.
    landing_page = (docs / "index.html").read_text()
    landing_page = landing_page.replace("{{ VERSION }}", version)
    (out / "index.html").write_text(landing_page)
    community_page = (docs / "community.html").read_text()
    (out / "community/index.html").write_text(community_page)


if __name__ == "__main__":
    main()
