#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.


# This script is used to patch the helm chart to ensure that the Chart.yaml file
# is created with the correct version.
#
# Usage: ./hack/fix-helm-chart.sh --chart <chart-directory> --version <version>

set -eou pipefail
CHART=""
TAG=""

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --chart) CHART="$2"; shift ;;
        --version) VERSION="$2"; shift ;;
        --app-version) APP_VERSION="$2"; shift ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

if [[ -z "$CHART" || -z "$VERSION" || -z "$APP_VERSION" ]]; then
    echo "Usage: $0 --chart <chart-directory> --version <version> --app-version <app-version>"
    exit 1
fi

cat <<EOF > "$CHART/Chart.yaml"
# Default values for local-csi-driver.
apiVersion: v2
description: Local CSI Driver Helm chart for Kubernetes
name: local-csi-driver
type: application
version: $VERSION
appVersion: $APP_VERSION
EOF
