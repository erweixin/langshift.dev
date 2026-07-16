#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 ABSOLUTE_REVIEWED_DEFINITIONS ABSOLUTE_NEW_OUTPUT_DIRECTORY" >&2
  exit 64
fi

repository="$(git rev-parse --show-toplevel)"
cd "$repository"
configuration="$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"
output="$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")"

case "$configuration" in
  "$repository"/*) ;;
  *) echo "release definitions must be inside the repository" >&2; exit 64 ;;
esac
if [[ ! -f "$configuration" || -e "$output" ]]; then
  echo "definitions must exist and output must be new" >&2
  exit 64
fi
if ! git diff --quiet --exit-code || ! git diff --cached --quiet --exit-code || [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
  echo "agent release assets require a completely clean source tree" >&2
  exit 65
fi

relative_configuration="${configuration#"$repository"/}"
git ls-files --error-unmatch -- "$relative_configuration" >/dev/null
source_commit="$(git rev-parse --verify HEAD)"
source_date_epoch="$(git show -s --format=%ct HEAD)"

go run ./cmd/lites-release-assets \
  --config "$configuration" \
  --output "$output" \
  --source-commit "$source_commit" \
  --source-date-epoch "$source_date_epoch"
