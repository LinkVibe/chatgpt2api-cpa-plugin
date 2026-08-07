//go:build cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

func storeHost(host *C.cliproxy_host_api) {
	C.store_host_api(host)
	setHostPresent(host != nil)
	if host != nil {
		startTokenPatrol()
	}
}

func hostCall(method string, payload any) ([]byte, error) {
	raw, err := hostCallRaw(method, payload)
	if err != nil {
		return nil, err
	}
	return decodeEnvelope(raw)
}

// hostCallInto decodes the host envelope's result directly into out, in the same
// pass that parses the envelope.
//
// hostCall goes through json.RawMessage, which allocates and copies the entire
// result — on an image download that is an extra ~1.3x the image size (the body
// is base64 inside the envelope JSON) held live alongside the decoded bytes.
// Decoding straight into a typed struct skips that copy.
func hostCallInto(method string, payload any, out any) error {
	raw, err := hostCallRaw(method, payload)
	if err != nil {
		return err
	}
	// Decode ok/error and the typed result in a single pass.
	env := struct {
		OK     bool           `json:"ok"`
		Error  *envelopeError `json:"error,omitempty"`
		Result any            `json:"result,omitempty"`
	}{Result: out}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if !env.OK {
		msg := "host call failed"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return errString(msg)
	}
	return nil
}

// hostCallRaw returns the undecoded envelope bytes from the host.
func hostCallRaw(method string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var req *C.uint8_t
	if len(raw) > 0 {
		req = (*C.uint8_t)(C.CBytes(raw))
		defer C.free(unsafe.Pointer(req))
	}
	if C.call_host_api(cMethod, req, C.size_t(len(raw)), &response) != 0 {
		return nil, fmt.Errorf("host call failed: %s", method)
	}
	if response.ptr == nil || response.len == 0 {
		return nil, fmt.Errorf("empty host response: %s", method)
	}
	out := C.GoBytes(response.ptr, C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)
	return out, nil
}

func hostLog(level, message string) {
	_, _ = hostCall("host.log", map[string]any{
		"level":   level,
		"message": message,
	})
}
