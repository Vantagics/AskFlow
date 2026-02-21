//go:build !linux

package fontcheck

// EnsureCJKFonts is a no-op on non-Linux platforms.
// Windows and macOS ship with CJK fonts by default.
func EnsureCJKFonts() {}
