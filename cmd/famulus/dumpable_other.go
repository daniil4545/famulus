//go:build !linux

package main

// hideProc has nothing to hide outside Linux: the runner runs in a Linux container.
func hideProc() error { return nil }
