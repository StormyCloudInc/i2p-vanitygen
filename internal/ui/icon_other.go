//go:build !windows

package ui

// setWindowIcon is a no-op on non-Windows platforms.
func setWindowIcon(_ string) {}
