#!/bin/bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

images="$(
    git ls-files -- '*Dockerfile' '*Dockerfile.*' '*.Dockerfile' |
        while IFS= read -r dockerfile; do
            awk '
                toupper($1) == "FROM" {
                    field = 2
                    while ($field ~ /^--/) {
                        field++
                    }
                    if ($field ~ /^mcr\.microsoft\.com\//) {
                        print $field
                    }
                }
            ' "$dockerfile"
        done | sort -u
)"

if [[ -z "$images" ]]; then
    echo "No MCR base images were found." >&2
    exit 1
fi

while IFS= read -r image; do
    if [[ ! "$image" =~ @sha256:[0-9a-f]{64}$ ]]; then
        echo "MCR base image must be pinned with a SHA-256 digest: $image" >&2
        exit 1
    fi
done <<< "$images"

printf '%s\n' "$images"
