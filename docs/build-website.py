#!/usr/bin/env python3

import json
import re
import shutil
import subprocess

from pathlib import Path


def main():
    "Build the website"

    # Source and output directories
    docs = Path(__file__).parent.resolve()
    out = docs / "_book"
    shutil.rmtree(out, ignore_errors=True)

    # Grab the current version number from book.json
    book = json.load((docs / "book.json").open())
    version = book["variables"]["version"]

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

    # Drop extra files copied by GitBook
    (out / "build-website.py").unlink()


if __name__ == "__main__":
    main()
