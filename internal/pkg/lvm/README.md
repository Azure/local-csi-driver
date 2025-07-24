# lvm2

A Go wrapper for working with [LVM2](https://sourceware.org/lvm2).

## Sources

The lvm package is based on
[github.com/dpeckett/lvm2](https://pkg.go.dev/github.com/dpeckett/lvm2#section-sourcefiles)
at version 0.3.1.

We didn't take a dependency as we wanted to change the error handling and use
context to control cancellation. The repository isn't public, and the author
didn't respond to emails, so there was no path for us to contribute these
changes.

## Usage

For detailed examples see the [lvm_test.go](./lvm_test.go) file.
