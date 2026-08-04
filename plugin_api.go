//go:build !cgo

package main

// Non-CGO entry: unit tests and syntax checks without a C compiler.
// The C ABI exports live in abi_cgo.go, which supplies main() for cgo builds.

func main() {}
