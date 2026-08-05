package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// hostPresent is set when CPA loads the plugin via cliproxy_plugin_init.
var hostPresent atomic.Bool

// allowDirectHTTPFallback is only for local/dev builds without a host.
// When host is present, silent net/http fallback is disabled (audit P0).
var allowDirectHTTPFallback atomic.Bool

func setHostPresent(v bool) { hostPresent.Store(v) }

// streamIdlePoll is the backoff between idle stream_read calls when the host
// has no buffered data yet. It bounds CPU to ~10 reads/sec while waiting, and
// adds no latency when chunks are flowing.
const streamIdlePoll = 100 * time.Millisecond

// hostHTTPRequest carries a host-issued HTTP request.
type hostHTTPRequest struct {
	Method  string              `json:"method,omitempty"`
	URL     string              `json:"url,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

// hostHTTPResponse accepts both the snake_case and PascalCase wire shapes in one
// decode. Go matches `Headers`/`Body` to the lowercase tags case-insensitively;
// only StatusCode needs an explicit alternate tag (the underscore blocks it).
type hostHTTPResponse struct {
	StatusCode    int                 `json:"status_code"`
	StatusCodeAlt int                 `json:"StatusCode"`
	Headers       map[string][]string `json:"headers"`
	Body          []byte              `json:"body"`
}

func (r hostHTTPResponse) status() int {
	if r.StatusCode != 0 {
		return r.StatusCode
	}
	return r.StatusCodeAlt
}

func doHTTP(method, url string, headers map[string]string, body []byte, timeoutSec int) (int, map[string][]string, []byte, error) {
	h := map[string][]string{}
	for k, v := range headers {
		h[k] = []string{v}
	}
	payload := hostHTTPRequest{
		Method:  method,
		URL:     url,
		Headers: h,
		Body:    body,
	}

	// Decode straight into the typed response: hostCall would first copy the
	// whole result through a json.RawMessage, which on image downloads is an
	// extra ~1.3x the image size held live next to the decoded bytes.
	var hostRes hostHTTPResponse
	err := hostCallInto("host.http.do", payload, &hostRes)
	if err == nil {
		status := hostRes.status()
		if status == 0 {
			status = 200
		}
		return status, hostRes.Headers, hostRes.Body, nil
	}
	if hostPresent.Load() && !allowDirectHTTPFallback.Load() {
		return 0, nil, nil, fmt.Errorf("host.http.do: %w", err)
	}
	hostLog("warn", "host.http.do failed, direct fallback: "+err.Error())

	// Direct fallback (local test / explicit allow only)
	client := &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	req, errReq := http.NewRequest(method, url, bytes.NewReader(body))
	if errReq != nil {
		return 0, nil, nil, errReq
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, errDo := client.Do(req)
	if errDo != nil {
		return 0, nil, nil, errDo
	}
	defer res.Body.Close()
	data, errRead := io.ReadAll(res.Body)
	if errRead != nil {
		return res.StatusCode, res.Header, nil, errRead
	}
	return res.StatusCode, res.Header, data, nil
}

func doHTTPSession(sess *accountSession, method, url string, headers map[string]string, body []byte, timeoutSec int) (int, map[string][]string, []byte, error) {
	h := cloneHeaderMap(headers)
	if sess != nil {
		sess.applyToHeaders(h)
	}
	status, respHeaders, data, err := doHTTP(method, url, h, body, timeoutSec)
	if sess != nil && respHeaders != nil {
		sess.ingestSetCookie(respHeaders)
	}
	return status, respHeaders, data, err
}

type hostStreamOpenResponse struct {
	StatusCode    int                 `json:"status_code"`
	StreamID      string              `json:"stream_id"`
	StatusCodeAlt int                 `json:"StatusCode"`
	StreamIDAlt   string              `json:"StreamID"`
	Headers       map[string][]string `json:"headers,omitempty"`
	HeadersAlt    map[string][]string `json:"Headers,omitempty"`
}

type hostStreamReadResponse struct {
	Payload []byte   `json:"payload,omitempty"`
	Error   string   `json:"error,omitempty"`
	Done    bool     `json:"done,omitempty"`
	Chunks  [][]byte `json:"chunks,omitempty"`
}

func doHTTPStream(method, url string, headers map[string]string, body []byte, timeoutSec int, onChunk func([]byte) error) (int, error) {
	return doHTTPStreamSession(nil, method, url, headers, body, timeoutSec, onChunk)
}

func doHTTPStreamSession(sess *accountSession, method, url string, headers map[string]string, body []byte, timeoutSec int, onChunk func([]byte) error) (int, error) {
	hdr := cloneHeaderMap(headers)
	if sess != nil {
		sess.applyToHeaders(hdr)
	}
	h := map[string][]string{}
	for k, v := range hdr {
		h[k] = []string{v}
	}
	openPayload := hostHTTPRequest{
		Method:  method,
		URL:     url,
		Headers: h,
		Body:    body,
	}
	raw, err := hostCall("host.http.do_stream", openPayload)
	if err != nil {
		if hostPresent.Load() && !allowDirectHTTPFallback.Load() {
			return 0, fmt.Errorf("host.http.do_stream: %w", err)
		}
		status, respHeaders, data, err2 := doHTTP(method, url, hdr, body, timeoutSec)
		if sess != nil && respHeaders != nil {
			sess.ingestSetCookie(respHeaders)
		}
		if err2 != nil {
			return 0, err2
		}
		if len(data) == 0 {
			return status, nil
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if err := onChunk(append(bytes.TrimRight(line, "\r"), '\n')); err != nil {
				return status, err
			}
		}
		return status, nil
	}

	var opened hostStreamOpenResponse
	if err := json.Unmarshal(raw, &opened); err != nil {
		return 0, fmt.Errorf("decode stream open: %w", err)
	}
	if sess != nil {
		if opened.Headers != nil {
			sess.ingestSetCookie(opened.Headers)
		} else if opened.HeadersAlt != nil {
			sess.ingestSetCookie(opened.HeadersAlt)
		}
	}
	streamID := opened.StreamID
	if streamID == "" {
		streamID = opened.StreamIDAlt
	}
	status := opened.StatusCode
	if status == 0 {
		status = opened.StatusCodeAlt
	}
	if streamID == "" {
		return status, fmt.Errorf("empty stream_id from host.http.do_stream")
	}
	defer func() {
		_, _ = hostCall("host.http.stream_close", map[string]any{"stream_id": streamID})
	}()

	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		if time.Now().After(deadline) {
			return status, fmt.Errorf("stream read deadline exceeded (%ds)", timeoutSec)
		}
		chunkRaw, err := hostCall("host.http.stream_read", map[string]any{"stream_id": streamID})
		if err != nil {
			return status, err
		}
		var chunk hostStreamReadResponse
		if err := json.Unmarshal(chunkRaw, &chunk); err != nil {
			var pascal struct {
				Payload []byte `json:"Payload"`
				Error   string `json:"Error"`
				Done    bool   `json:"Done"`
			}
			if err2 := json.Unmarshal(chunkRaw, &pascal); err2 != nil {
				return status, fmt.Errorf("decode stream chunk: %w raw=%s", err, truncate(string(chunkRaw), 200))
			}
			chunk.Payload = pascal.Payload
			chunk.Error = pascal.Error
			chunk.Done = pascal.Done
		}
		if chunk.Error != "" {
			return status, fmt.Errorf("stream error: %s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			if err := onChunk(chunk.Payload); err != nil {
				return status, err
			}
		}
		for _, c := range chunk.Chunks {
			if len(c) == 0 {
				continue
			}
			if err := onChunk(c); err != nil {
				return status, err
			}
		}
		if chunk.Done {
			return status, nil
		}
		// The host may return an empty chunk immediately while data is still
		// pending. Sleep on idle reads so the loop never busy-spins a CPU core
		// for the whole stream window; reads carrying data stay unthrottled.
		if len(chunk.Payload) == 0 && len(chunk.Chunks) == 0 {
			time.Sleep(streamIdlePoll)
		}
	}
}

// hostStreamEmit pushes one chunk into the host stream bridge opened for
// executor.execute_stream. The bridge queue is bounded (16), so this blocks
// under backpressure — which is exactly what keeps plugin memory flat instead
// of buffering the whole response into a chunk array.
func hostStreamEmit(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("stream id is required")
	}
	_, err := hostCall("host.stream.emit", map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	})
	return err
}

// hostStreamClose ends the bridge stream. A non-empty errMsg is surfaced to the
// client as a stream error chunk.
func hostStreamClose(streamID, errMsg string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = hostCall("host.stream.close", map[string]any{
		"stream_id": streamID,
		"error":     strings.TrimSpace(errMsg),
	})
}

func headerGet(headers map[string][]string, key string) string {
	if headers == nil {
		return ""
	}
	if v, ok := headers[key]; ok && len(v) > 0 {
		return v[0]
	}
	lk := strings.ToLower(key)
	for k, v := range headers {
		if strings.ToLower(k) == lk && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}
