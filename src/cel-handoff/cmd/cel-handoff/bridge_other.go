//go:build !(js && wasm)

package main

// A native build always serves the command line.
func bridgeMode() bool { return false }

func serveBridge() {}
