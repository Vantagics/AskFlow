//go:build !linux

package fontcheck

// ensureCJKFontsLinux is a no-op on non-Linux platforms.
// Windows and macOS ship with CJK fonts by default.
func ensureCJKFontsLinux() {}
