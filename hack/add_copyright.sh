#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.


set -euo pipefail

SOURCE_ROOT="${1:-$(pwd)}"

if [ ! -d "$SOURCE_ROOT" ]; then
  echo "Error: Directory '$SOURCE_ROOT' does not exist"
  exit 1
fi

# Define file types with their headers
declare -A FILE_TYPES
FILE_TYPES=(
  ["go"]="// Copyright (c) Microsoft Corporation.\n// Licensed under the MIT License."
  ["bicep"]="// Copyright (c) Microsoft Corporation.\n// Licensed under the MIT License."
  ["sh"]="# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License."
  ["py"]="# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License."
  ["mk"]="# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License."
  ["Makefile"]="# Copyright (c) Microsoft Corporation.\n# Licensed under the MIT License."
)

# Process files by type
for type in "${!FILE_TYPES[@]}"; do
  header="${FILE_TYPES[$type]}"

  # Handle special case for Makefile
  if [ "$type" == "Makefile" ]; then
    find_pattern=(-name Makefile)
  else
    find_pattern=(-name "*.$type")
  fi

  echo "Processing $type files..."

  # Find and process each file
  find "$SOURCE_ROOT" -type f "${find_pattern[@]}" | while read -r file; do
    # Skip if header already exists
    if grep -q "Copyright (c) Microsoft Corporation" "$file"; then
      continue
    fi

    echo "Adding header to $file"

    # Handle shebang for scripts
    if [[ "$type" =~ ^(sh|py)$ ]] && grep -q "^#!" "$file"; then
      awk 'NR==1{print; print "'"$(echo -e "$header")"'"; print ""} NR!=1' "$file" > "$file.tmp"
    else
      echo -e "$header\n\n$(cat "$file")" > "$file.tmp"
    fi

    chmod --reference="$file" "$file.tmp"
    mv "$file.tmp" "$file"
  done
done

echo "Done"
