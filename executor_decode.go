package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

type executorRequest struct {
	AuthID          string      `json:"AuthID"`
	AuthProvider    string      `json:"AuthProvider"`
	Model           string      `json:"Model"`
	Format          string      `json:"Format"`
	Stream          bool        `json:"Stream"`
	OriginalRequest []byte      `json:"OriginalRequest"`
	Payload         []byte      `json:"Payload"`
	StorageJSON     []byte      `json:"StorageJSON"`
	SourceFormat    string      `json:"SourceFormat"`
	Headers         http.Header `json:"Headers"`
	FileName        string      `json:"FileName"`
	AuthIndex       string      `json:"AuthIndex"`

	// StreamID is opened by the host stream bridge for executor.execute_stream.
	// When present the plugin should emit chunks via host.stream.emit instead of
	// returning them all in one response array.
	StreamID string `json:"stream_id"`
	// HostCallbackID must be forwarded on nested host.model.* calls.
	HostCallbackID string `json:"host_callback_id"`
}

func decodeExecutorRequest(request []byte, forceStream bool) (executorRequest, error) {
	var req executorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		var m map[string]any
		if err2 := json.Unmarshal(request, &m); err2 != nil {
			return req, fmt.Errorf("invalid executor request: %w", err)
		}
		req = executorRequestFromMap(m)
	} else {
		// Fill empties from lowercase aliases if present in raw map
		var m map[string]any
		if json.Unmarshal(request, &m) == nil {
			fillExecutorFromMap(&req, m)
		}
	}
	if forceStream {
		req.Stream = true
	}
	return req, nil
}

func executorRequestFromMap(m map[string]any) executorRequest {
	var req executorRequest
	fillExecutorFromMap(&req, m)
	return req
}

func fillExecutorFromMap(req *executorRequest, m map[string]any) {
	if req.Model == "" {
		req.Model = firstNonEmpty(str(m["Model"]), str(m["model"]))
	}
	if req.Format == "" {
		req.Format = firstNonEmpty(str(m["Format"]), str(m["format"]))
	}
	if req.SourceFormat == "" {
		req.SourceFormat = firstNonEmpty(str(m["SourceFormat"]), str(m["source_format"]))
	}
	if len(req.StorageJSON) == 0 {
		req.StorageJSON = firstBytes(m["StorageJSON"], m["storage_json"])
	}
	if len(req.Payload) == 0 {
		req.Payload = firstBytes(m["Payload"], m["payload"])
	}
	if len(req.OriginalRequest) == 0 {
		req.OriginalRequest = firstBytes(m["OriginalRequest"], m["original_request"])
	}
	if req.AuthID == "" {
		req.AuthID = firstNonEmpty(str(m["AuthID"]), str(m["auth_id"]))
	}
	if req.AuthIndex == "" {
		req.AuthIndex = firstNonEmpty(str(m["AuthIndex"]), str(m["auth_index"]), req.AuthID)
	}
	if req.FileName == "" {
		req.FileName = firstNonEmpty(str(m["FileName"]), str(m["file_name"]), str(m["filename"]))
	}
	if req.StreamID == "" {
		req.StreamID = firstNonEmpty(str(m["stream_id"]), str(m["StreamID"]))
	}
	if req.HostCallbackID == "" {
		req.HostCallbackID = firstNonEmpty(str(m["host_callback_id"]), str(m["HostCallbackID"]))
	}
	if !req.Stream {
		if b, ok := m["Stream"].(bool); ok {
			req.Stream = b
		} else if b, ok := m["stream"].(bool); ok {
			req.Stream = b
		}
	}
	if req.Headers == nil {
		if h, ok := m["Headers"].(map[string]any); ok {
			req.Headers = headerFromAny(h)
		} else if h, ok := m["headers"].(map[string]any); ok {
			req.Headers = headerFromAny(h)
		}
	}
}

func headerFromAny(m map[string]any) http.Header {
	h := make(http.Header, len(m))
	for k, v := range m {
		switch vals := v.(type) {
		case []any:
			for _, it := range vals {
				h.Add(k, str(it))
			}
		case []string:
			for _, it := range vals {
				h.Add(k, it)
			}
		default:
			if s := str(vals); s != "" {
				h.Set(k, s)
			}
		}
	}
	return h
}

func firstBytes(vals ...any) []byte {
	for _, v := range vals {
		if b := asBytes(v); len(b) > 0 {
			return b
		}
	}
	return nil
}

func stringMapFromAny(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = str(v)
	}
	return out
}

func decodeStorageMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func requestBodyJSON(req executorRequest) (map[string]any, []byte) {
	body := req.Payload
	if len(body) == 0 {
		body = req.OriginalRequest
	}
	var payload map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
	}
	if payload == nil {
		payload = multipartPayload(req, body)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, body
}

func multipartPayload(req executorRequest, body []byte) map[string]any {
	ct := req.Headers.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return nil
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	payload := map[string]any{}
	var images []string

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil || part == nil {
			continue
		}
		name := part.FormName()
		fileName := part.FileName()
		if name == "" {
			continue
		}
		data, _ := io.ReadAll(part)
		if len(data) == 0 {
			continue
		}

		if fileName != "" || isMultipartFilePart(part, data) {
			mimeType := part.Header.Get("Content-Type")
			if mimeType == "" {
				mimeType = http.DetectContentType(data)
			}
			encoded := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
			if name == "image" || name == "image[]" || name == "images" {
				images = append(images, encoded)
			} else if name == "mask" {
				payload["mask"] = encoded
			} else {
				images = append(images, encoded)
			}
			continue
		}
		payload[name] = strings.TrimSpace(string(data))
	}

	if len(images) == 1 {
		payload["image"] = images[0]
	} else if len(images) > 1 {
		arr := make([]any, len(images))
		for i, v := range images {
			arr[i] = v
		}
		payload["images"] = arr
	}
	return payload
}

func isMultipartFilePart(part *multipart.Part, data []byte) bool {
	if part.FileName() != "" {
		return true
	}
	ct := part.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "image/") || mediaType == "application/octet-stream"
}

func resolveModelName(req executorRequest, payload map[string]any) string {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(str(payload["model"]))
	}
	if model == "" {
		model = currentRuntimeConfig().DefaultModel
	}
	return model
}
