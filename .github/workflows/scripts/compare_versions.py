#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""
Script to compare semantic versions using the semver library.
Returns 0 if new_version > current_version, 1 otherwise.
"""

import sys
import argparse
import semver


def compare_versions(current: str, new: str) -> bool:
    """
    Compare two semantic versions.

    Args:
        current: Current version string
        new: New version string

    Returns:
        True if new > current, False otherwise
    """
    # Remove 'v' prefix if present
    current = current.lstrip('v')
    new = new.lstrip('v')

    # Parse versions using semver library
    current_ver = semver.Version.parse(current)
    new_ver = semver.Version.parse(new)

    # Use built-in comparison
    return new_ver > current_ver


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
            print(
                f"✓ New version {args.new} is higher than "
                f"current version {args.current}"
            )
            sys.exit(0)
        else:
            print(
                f"✗ New version {args.new} is NOT higher than "
                f"current version {args.current}"
            )
            sys.exit(1)
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
