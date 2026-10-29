#!/bin/bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

if (($# != 1)); then
    echo "Usage: $0 <trust-store-directory>" >&2
    exit 1
fi

CA_STORE="$1/x509/ca/supplychain"
TSA_STORE="$1/x509/tsa/esrp"
CERT_DOWNLOAD="$(mktemp)"
trap 'rm -f -- "$CERT_DOWNLOAD"' EXIT

import_root() {
    curl --fail --location --proto '=https' --retry 3 --show-error --silent \
        "$1" --output "$CERT_DOWNLOAD"
    printf '%s  %s\n' "$2" "$CERT_DOWNLOAD" | sha256sum --check --status
    openssl x509 -in "$CERT_DOWNLOAD" -inform DER -out "$3" -outform PEM
}

mkdir -p "$CA_STORE" "$TSA_STORE"

import_root \
    "https://www.microsoft.com/pkiops/certs/Microsoft%20Supply%20Chain%20RSA%20Root%20CA%202022.crt" \
    "23ffe2b8bdb9a1711515d4cffda04bc7f793d513c76c243f1020507d8669b7db" \
    "$CA_STORE/microsoft-supply-chain-rsa-root-ca-2022.crt"

import_root \
    "https://www.microsoft.com/pki/certs/MicRooCerAut_2010-06-23.crt" \
    "df545bf919a2439c36983b54cdfc903dfa4f37d3996d8d84b4c31eec6f3c163e" \
    "$TSA_STORE/microsoft-root-certificate-authority-2010.crt"
