#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -eou pipefail

if [ $# -eq 0 ]; then
    echo "Error: No release-train provided."
    echo "Usage: $0 <release-train>"
    exit 1
fi

if [ -z "${RESOURCE_GROUP:-}" ] && [ -z "${CLUSTER_NAME:-}" ]; then
    echo "No flags set. Using current kubecontext to grab and parse providerID..."
    PROVIDER_ID=$(kubectl get node -o jsonpath='{.items[0].spec.providerID}')
    if [ -z "${PROVIDER_ID:-}" ]; then
        echo "Failed to get providerID from current kubecontext."
        exit 1
    fi
    echo "Provider ID: ${PROVIDER_ID}"
    IFS='/' read -ra PROVIDER_ID_PARTS <<<"${PROVIDER_ID}"
    NODE_RESOURCE_GROUP=${PROVIDER_ID_PARTS[6]}
    if [ -z "${NODE_RESOURCE_GROUP:-}" ]; then
        echo "Failed to parse node resource group from providerID."
        exit 1
    fi
    CLUSTER_NAME=$(az resource show --id "/subscriptions/${PROVIDER_ID_PARTS[4]}/resourceGroups/${NODE_RESOURCE_GROUP}" | jq -r '.tags."aks-managed-cluster-name"')
    if [ -z "${CLUSTER_NAME:-}" ]; then
        echo "Failed to get managed cluster name from node resource group."
        exit 1
    fi
    RESOURCE_GROUP=$(az resource show --id "/subscriptions/${PROVIDER_ID_PARTS[4]}/resourceGroups/${NODE_RESOURCE_GROUP}" | jq -r '.tags."aks-managed-cluster-rg"')
    if [ -z "${RESOURCE_GROUP:-}" ]; then
        echo "Failed to get managed cluster resource group from node resource group."
        exit 1
    fi
    echo "Resource Group: ${RESOURCE_GROUP}"
    echo "Cluster Name: ${CLUSTER_NAME}"
fi

az k8s-extension create --cluster-type managedClusters --cluster-name "${CLUSTER_NAME}" \
    --resource-group "${RESOURCE_GROUP}" --name cns --extension-type microsoft.azstor \
    --scope cluster --release-train "${RELEASE_TRAIN}" --release-namespace kube-system
