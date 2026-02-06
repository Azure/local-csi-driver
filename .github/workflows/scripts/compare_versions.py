#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""
Script to compare semantic versions.
Returns 0 if new_version > current_version, 1 otherwise.
"""

import sys
import argparse
from typing import Tuple


def parse_version(version: str) -> Tuple[int, int, int, str]:
    """
    Parse a semantic version string into components.
    
    Args:
        version: Version string (e.g., "1.2.3" or "1.2.3-preview.1")
    
    Returns:
        Tuple of (major, minor, patch, prerelease)
    """
    # Remove 'v' prefix if present
    version = version.lstrip('v')
    
    # Split on '-' to separate version from prerelease
    parts = version.split('-', 1)
    base_version = parts[0]
    prerelease = parts[1] if len(parts) > 1 else ""
    
    # Parse major.minor.patch
    version_parts = base_version.split('.')
    if len(version_parts) != 3:
        raise ValueError(f"Invalid version format: {version}")
    
    try:
        major = int(version_parts[0])
        minor = int(version_parts[1])
        patch = int(version_parts[2])
    except ValueError as e:
        raise ValueError(f"Invalid version format: {version}") from e
    
    return (major, minor, patch, prerelease)


def compare_versions(current: str, new: str) -> bool:
    """
    Compare two semantic versions.
    
    Args:
        current: Current version string
        new: New version string
    
    Returns:
        True if new > current, False otherwise
    """
    current_parts = parse_version(current)
    new_parts = parse_version(new)
    
    # Compare major, minor, patch
    for i in range(3):
        if new_parts[i] > current_parts[i]:
            return True
        elif new_parts[i] < current_parts[i]:
            return False
    
    # If base versions are equal, compare prerelease
    # A version without prerelease is higher than one with prerelease
    # e.g., 1.2.3 > 1.2.3-preview.1
    current_prerelease = current_parts[3]
    new_prerelease = new_parts[3]
    
    if not current_prerelease and new_prerelease:
        # current is stable, new is prerelease -> new is lower
        return False
    elif current_prerelease and not new_prerelease:
        # current is prerelease, new is stable -> new is higher
        return True
    elif current_prerelease and new_prerelease:
        # Both are prereleases, compare lexicographically
        return new_prerelease > current_prerelease
    
    # Versions are equal
    return False


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description='Compare semantic versions'
    )
    parser.add_argument(
        '--current',
        required=True,
        help='Current version (e.g., 1.2.3)'
    )
    parser.add_argument(
        '--new',
        required=True,
        help='New version to compare (e.g., 1.2.4)'
    )
    
    args = parser.parse_args()
    
    try:
        is_higher = compare_versions(args.current, args.new)
        
        if is_higher:
            print(f"✓ New version {args.new} is higher than current version {args.current}")
            sys.exit(0)
        else:
            print(f"✗ New version {args.new} is NOT higher than current version {args.current}")
            sys.exit(1)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
