---
name: changelog-entry
description: Add a new entry to CHANGELOG.yml under the current unreleased version, or create the version block if needed, then regenerate documentation. Use when the user says things like "add a changelog entry", "log this fix in the changelog", or "/changelog-entry".
---

# changelog-entry

Adds an entry to `CHANGELOG.yml` following the schema documented in the file header, then regenerates the derived documentation.

## Inputs to gather

1. **type** - one of `bugfix`, `feature`, `security`, `change`. If the user describes the change but does not pick a type, infer it:
   - "fixes/resolves/closes a bug" -> `bugfix`
   - "adds support for / introduces / new" -> `feature`
   - "CVE / vulnerability / hardens" -> `security`
   - anything else affecting behavior -> `change`
2. **title** - short, sentence-cased, no trailing period.
3. **body** - 2-3 sentences. This field is HTML, not markdown. Use `<code>...</code>` for code and `<a href="...">...</a>` for links. Prefer YAML's `>-` folded scalar so line wrapping does not leak literal newlines.
4. **docs** optional - path to a docs page under `docs/` if the entry deserves a "Learn more" link.
5. **image** optional - path under the `release-notes` directory if there is a visual.

## Steps

1. Read the top of `CHANGELOG.yml`. The first `items:` entry is the current/upcoming version.
2. If it has `date: (TBD)`, append the new entry to its `notes:` array.
3. If the top item is already dated, insert a new `- version: <next>` block above it with `date: (TBD)` and the single new note. Ask the user for the next version number; do not invent it.
4. Match existing indentation exactly. YAML is whitespace-sensitive.
5. After saving, run `make docs-files` to regenerate `docs/release-notes.md`, `docs/release-notes.mdx`, and `docs/variables.yml`.
6. Show the user the diff: `git diff CHANGELOG.yml docs/release-notes.md docs/release-notes.mdx docs/variables.yml`.

## Schema reference

```yaml
items:
  - version: 2.28.0
    date: (TBD)
    notes:
      - type: bugfix
        title: Short title
        body: >-
          Two or three sentences describing the change and why it
          is noteworthy. This is HTML.
        docs: optional/path
        image: optional/path
```

## Things to avoid

- Do not edit `docs/release-notes.md`, `docs/release-notes.mdx`, or `docs/variables.yml` directly. They are generated.
- Do not include markdown syntax in `body`; it is rendered as HTML.
- Do not set `date:` to a concrete date for upcoming versions; `make prepare-release` does that automatically for GA versions.
