#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
#
# Generate NOTICE.txt from third-party Go dependency licenses.
#
# Requires `go-licenses` to be on PATH. Writes NOTICE.txt in the current
# working directory by default, or to the path supplied as the first argument.

set -euo pipefail

OUTPUT="${1:-NOTICE.txt}"

if ! command -v go-licenses >/dev/null 2>&1; then
    echo "Error: go-licenses not found on PATH" >&2
    exit 1
fi

STD_PACKAGES="$(go list std | tr '\n' ',' | sed 's/,$//')"

go-licenses report ./cmd/... \
    --ignore local-csi-driver \
    --ignore "${STD_PACKAGES}" \
    --template NOTICE.tmpl \
    > "${OUTPUT}"

cat NOTICE.manual >> "${OUTPUT}"
