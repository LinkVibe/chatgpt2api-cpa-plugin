//go:build !cgo

package main

import (
	"encoding/json"
	"fmt"
)

// Stubs for unit tests / builds without a C compiler.
// Production CPA plugin binaries are built with CGO.

func storeHost(host any) {
	_ = host
	setHostPresent(false)
	allowDirectHTTPFallback.Store(true)
}

func hostCall(method string, payload any) ([]byte, error) {
	_ = payload
	return nil, fmt.Errorf("host unavailable (cgo disabled): %s", method)
}

func hostCallInto(method string, payload any, out any) error {
	_, _ = payload, out
	return fmt.Errorf("host unavailable (cgo disabled): %s", method)
}

func hostLog(level, message string) {
	_, _ = level, message
}

var _ = json.Marshal
