#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -eoux pipefail

# Parse arguments
TAG=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --tag)
      TAG="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: ./update-version.sh [--tag <tag>]"
      exit 1
      ;;
  esac
done


if [ -z "${TAG}" ]; then
  echo "Error: --tag argument is required."
  exit 1
fi


# Remove 'v' prefix if present
VERSION="${TAG#v}"
echo "Extracted version: ${VERSION}"

# Verify that the expected version pattern exists before attempting replacement
if ! grep -qE -- '--version [0-9]+\.[0-9]+\.[0-9]+[^ ]*' README.md; then
  echo "Expected version pattern not found in README.md; aborting version update."
  exit 1
fi

# Extract current version from README.md
CURRENT_VERSION=$(grep -oE -- '--version [0-9]+\.[0-9]+\.[0-9]+' README.md | head -1 | sed 's/--version //')
echo "Current version in README.md: ${CURRENT_VERSION}"
echo "New version from tag: ${VERSION}"

# Compare versions using sort -V (version sort)
HIGHER_VERSION=$(printf '%s\n%s' "${CURRENT_VERSION}" "${VERSION}" | sort -V | tail -1)

if [ "${CURRENT_VERSION}" = "${HIGHER_VERSION}" ] && [ "${CURRENT_VERSION}" != "${VERSION}" ]; then
  echo "README.md already has a higher version (${CURRENT_VERSION}). Skipping update."
  exit 0
fi

# Update version in README.md
sed -i -E "s/--version [0-9]+\.[0-9]+\.[0-9]+[^ ]*/--version ${VERSION}/" README.md

# # Configure git
git config user.name "github-actions[bot]"
git config user.email "github-actions[bot]@users.noreply.github.com"

# Create branch and commit
RANDOM_SUFFIX=$(shuf -i 1000-9999 -n 1)
BRANCH="chore/update-version-${VERSION}-${RANDOM_SUFFIX}"

git checkout -b "${BRANCH}"
git add README.md

if git diff --staged --quiet; then
  echo "No changes to commit"
  exit 0
fi

git commit -m "docs: update version to ${VERSION} in README.md"
# git push origin "${BRANCH}"

# # Create pull request
gh pr create \
  --base main \
  --title "docs: update version to ${VERSION} in README.md" \
  --body "This PR updates the version in the README.md install command to ${VERSION}."
