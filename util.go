package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func extractAuthJSON(req map[string]any) []byte {
	for _, key := range []string{"StorageJSON", "storage_json", "content", "body", "raw", "data"} {
		if b := asBytes(req[key]); len(b) > 0 {
			return b
		}
	}
	// whole request might be the auth file
	raw, _ := json.Marshal(req)
	return raw
}

func parseAccessToken(raw []byte) (token string, meta map[string]string) {
	meta = map[string]string{}
	if len(raw) == 0 {
		return "", meta
	}
	// maybe base64-wrapped
	if decoded, err := jsonRawOrB64(raw); err == nil {
		raw = decoded
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// plain token string
		s := strings.TrimSpace(string(raw))
		if strings.HasPrefix(s, "eyJ") {
			return s, meta
		}
		return "", meta
	}
	meta["email"] = firstNonEmpty(str(m["email"]), str(m["account_email"]))
	for _, key := range []string{"access_token", "accessToken", "token"} {
		if t := str(m[key]); t != "" {
			return t, meta
		}
	}
	if tokens, ok := m["tokens"].(map[string]any); ok {
		if t := str(tokens["access_token"]); t != "" {
			return t, meta
		}
	}
	if auth, ok := m["auth"].(map[string]any); ok {
		if t := str(auth["access_token"]); t != "" {
			return t, meta
		}
	}
	return "", meta
}

func jsonRawOrB64(raw []byte) ([]byte, error) {
	if json.Valid(raw) {
		return raw, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if json.Valid([]byte(s)) {
			return []byte(s), nil
		}
		if b, err := decodeLooseB64(s); err == nil && json.Valid(b) {
			return b, nil
		}
	}
	if b, err := decodeLooseB64(string(raw)); err == nil && json.Valid(b) {
		return b, nil
	}
	return raw, fmt.Errorf("not json")
}

func asBytes(v any) []byte {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return t
	case string:
		if b, err := decodeLooseB64(t); err == nil && (json.Valid(b) || strings.HasPrefix(string(b), "eyJ")) {
			return b
		}
		return []byte(t)
	case json.RawMessage:
		return t
	default:
		b, _ := json.Marshal(t)
		return b
	}
}

func decodeLooseB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case fmt.Stringer:
		return t.String()
	default:
		if t == nil {
			return ""
		}
		return fmt.Sprint(t)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func shortID(token string) string {
	if len(token) > 16 {
		return "c2a-" + token[len(token)-12:]
	}
	return "c2a-" + token
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
