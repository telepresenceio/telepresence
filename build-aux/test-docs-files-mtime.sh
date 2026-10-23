#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

target=docs/release-notes.md
tmp="$(mktemp -d)"
real_git="$(command -v git)"

cp -p CHANGELOG.yml "$tmp/CHANGELOG.yml"
cp -p "$target" "$tmp/release-notes.md"
cleanup() {
  cp -p "$tmp/CHANGELOG.yml" CHANGELOG.yml
  cp -p "$tmp/release-notes.md" "$target"
  rm -rf "$tmp"
}
trap cleanup EXIT

mkdir "$tmp/bin"
cat >"$tmp/bin/git" <<EOF
#!/usr/bin/env bash
if [[ "\$1" == add ]]; then
  exit 0
fi
exec "$real_git" "\$@"
EOF
chmod +x "$tmp/bin/git"

make TELEPRESENCE_VERSION=v2.33.0-rc.1 tools/bin/relnotesgen >/dev/null
touch -d '@946684800' CHANGELOG.yml
touch -d '@4102444800' "$target"

PATH="$tmp/bin:$PATH" make TELEPRESENCE_VERSION=v2.33.0-rc.1 "$target"
target_mtime="$(stat -c %Y "$target")"
current_time="$(date +%s)"
if (( target_mtime > current_time + 60 )); then
  printf 'expected make to regenerate %s when its output is newer than CHANGELOG.yml\n' "$target" >&2
  exit 1
fi

for generated_target in \
  docs/README.md \
  docs/release-notes.md \
  docs/release-notes.mdx \
  docs/variables.yml \
  docs/helm/values.schema.json \
  docs/schemas/workstation-state.v1alpha1.json; do
  if ! awk -v target="$generated_target" '$1 == target ":" && /FORCE/ { found = 1 } END { exit !found }' build-aux/main.mk; then
    printf 'expected %s to depend on FORCE\n' "$generated_target" >&2
    exit 1
  fi
done

printf 'docs generation mtime regression test passed\n'
