#!/bin/sh
set -eu

if [ $# -ne 1 ]; then
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 0.1.1" >&2
  exit 2
fi

version="$1"
version="${version#v}"

cd /src

git config --add safe.directory /src

# Find last release tag (vX.Y.Z)
last_tag="$(git tag --list 'v*' --sort=-version:refname | head -n 1 || true)"

export DEBEMAIL="${DEBEMAIL:-bot@foundries.io}"

if [ -z "$last_tag" ]; then
  echo "No previous v* tag found. Generating changelog from initial commit." >&2
  gbp dch -N "$version" --ignore-branch
else
  echo "Generating changelog since tag: $last_tag" >&2
  gbp dch -N "$version" --ignore-branch --since="$last_tag"
fi
