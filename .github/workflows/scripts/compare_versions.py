#!/usr/bin/env python3
"""
Compare semantic versions to determine if an update should be performed.

This script compares two semantic versions and returns:
- Exit code 0 if version1 > version2 (should update)
- Exit code 1 if version1 <= version2 (should not update)
"""

import sys
import re


class Version:
    """Simple semantic version class for comparison."""
    
    def __init__(self, version_string):
        """Parse a semantic version string."""
        # Extract major.minor.patch from version string
        match = re.match(r'^(\d+)\.(\d+)\.(\d+)', version_string)
        if not match:
            raise ValueError(f"Invalid version format: {version_string}")
        
        self.major = int(match.group(1))
        self.minor = int(match.group(2))
        self.patch = int(match.group(3))
        self.original = version_string
    
    def __gt__(self, other):
        """Check if this version is greater than another."""
        if self.major != other.major:
            return self.major > other.major
        if self.minor != other.minor:
            return self.minor > other.minor
        return self.patch > other.patch
    
    def __eq__(self, other):
        """Check if this version equals another."""
        return (self.major == other.major and 
                self.minor == other.minor and 
                self.patch == other.patch)
    
    def __le__(self, other):
        """Check if this version is less than or equal to another."""
        return not self > other
    
    def __str__(self):
        """String representation."""
        return f"{self.major}.{self.minor}.{self.patch}"


def main():
    """Main function to compare versions."""
    if len(sys.argv) != 3:
        print("Usage: compare_versions.py <new_version> <current_version>", file=sys.stderr)
        print("Returns exit code 0 if new_version > current_version, 1 otherwise", file=sys.stderr)
        sys.exit(2)
    
    new_version_str = sys.argv[1]
    current_version_str = sys.argv[2]
    
    try:
        new_version = Version(new_version_str)
        current_version = Version(current_version_str)
        
        print(f"Comparing versions:")
        print(f"  New version:     {new_version_str} (parsed as {new_version})")
        print(f"  Current version: {current_version_str} (parsed as {current_version})")
        
        if new_version > current_version:
            print(f"Result: New version {new_version} is greater than current version {current_version}")
            print("  → Proceeding with update")
            sys.exit(0)
        else:
            print(f"Result: New version {new_version} is not greater than current version {current_version}")
            print("  → Skipping update")
            sys.exit(1)
    
    except ValueError as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(2)


if __name__ == "__main__":
    main()
