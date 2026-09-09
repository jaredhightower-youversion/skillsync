#!/usr/bin/env bash
# Run by version.yml once main has no pending changesets: tag the version in
# package.json and start the release build. Idempotent, a re-run on an already
# tagged version does nothing.
set -euo pipefail
tag="v$(node -p 'require("./package.json").version')"
if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  echo "$tag already exists; nothing to do"
  exit 0
fi
git tag "$tag"
git push origin "$tag"
gh workflow run release.yml --ref "$tag"
echo "tagged $tag and dispatched release.yml"
