#!/usr/bin/env python3
import json
import os
import subprocess
import sys


GENERATED_SUFFIXES = {
    "DEPENDENCIES.md",
    "DEPENDENCY_LICENSES.md",
    "docs/release-notes.md",
    "docs/release-notes.mdx",
    "docs/variables.yml",
}


def normalize(path):
    path = path.strip().strip('"')
    while path.startswith("./"):
        path = path[2:]
    return path.replace(os.sep, "/")


def paths_from_patch(command):
    paths = []
    for line in command.splitlines():
        for marker in (
            "*** Add File: ",
            "*** Delete File: ",
            "*** Update File: ",
            "*** Move to: ",
        ):
            if line.startswith(marker):
                paths.append(normalize(line[len(marker) :]))
    return paths


def touched_paths(payload):
    tool_input = payload.get("tool_input") or {}
    paths = []

    file_path = tool_input.get("file_path")
    if isinstance(file_path, str):
        paths.append(normalize(file_path))

    command = tool_input.get("command")
    if isinstance(command, str):
        paths.extend(paths_from_patch(command))

    patch = tool_input.get("patch")
    if isinstance(patch, str):
        paths.extend(paths_from_patch(patch))

    return sorted(set(paths))


def is_generated(path):
    return path.startswith("docs/reference/cli/") or path in GENERATED_SUFFIXES or any(
        path.endswith("/" + suffix) for suffix in GENERATED_SUFFIXES
    )


def main():
    payload = json.load(sys.stdin)
    paths = touched_paths(payload)
    event = payload.get("hook_event_name")

    generated = [path for path in paths if is_generated(path)]
    if event == "PreToolUse" and generated:
        print(
            "Refusing to edit generated file(s): "
            + ", ".join(generated)
            + ". Modify the source (CHANGELOG.yml, Go source under "
            + "pkg/client/cli/cmd/, or run 'make generate') instead.",
            file=sys.stderr,
        )
        sys.exit(2)

    if event == "PostToolUse" and any(path == "CHANGELOG.yml" or path.endswith("/CHANGELOG.yml") for path in paths):
        print("CHANGELOG.yml changed; regenerating docs...", file=sys.stderr)
        completed = subprocess.run(["make", "docs-files"], cwd=payload.get("cwd") or os.getcwd())
        sys.exit(completed.returncode)


if __name__ == "__main__":
    main()
