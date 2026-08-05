package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// buildAuthStorage builds the canonical StorageJSON map for this plugin.
func buildAuthStorage(token, email, accountID, label string, sess *accountSession, disabled bool, extra map[string]any) map[string]any {
	out := map[string]any{
		"type":         providerID,
		"access_token": token,
		"email":        email,
		"account_id":   accountID,
		"label":        label,
		"disabled":     disabled,
	}
	if sess != nil {
		out["device_id"] = sess.DeviceID
		out["session_state"] = sess
	}
	for k, v := range extra {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

func storageDisabled(storage map[string]any) bool {
	if storage == nil {
		return false
	}
	b, ok := storage["disabled"].(bool)
	return ok && b
}

func storageAuthFileName(storage map[string]any, fallback string) string {
	if storage != nil {
		for _, k := range []string{"file_name", "FileName", "filename", "name"} {
			if s := strings.TrimSpace(str(storage[k])); s != "" {
				return ensureJSONExt(s)
			}
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return ensureJSONExt(fallback)
	}
	return ""
}

func ensureJSONExt(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return name + ".json"
	}
	return name
}

// resolveAuthFileName prefers explicit name, then storage, then host.auth.list match, then hash name.
func resolveAuthFileName(accessToken, explicit string, storage map[string]any) string {
	if name := ensureJSONExt(explicit); name != "" {
		return name
	}
	if name := storageAuthFileName(storage, ""); name != "" {
		return name
	}
	if name := findAuthFileNameByToken(accessToken); name != "" {
		return name
	}
	return fmt.Sprintf("chatgpt2api-cpa-plugin-%s.json", tokenFileSuffix(accessToken))
}

func findAuthFileNameByToken(accessToken string) string {
	fp := tokenFingerprint(accessToken)
	raw, err := hostCall("host.auth.list", map[string]any{})
	if err != nil || len(raw) == 0 {
		return ""
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		var arr []any
		if json.Unmarshal(raw, &arr) == nil {
			root = map[string]any{"files": arr}
		} else {
			return ""
		}
	}
	files := firstSlice(root, "files", "Files", "items", "auths", "Auths", "Entries", "entries")
	for _, item := range files {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		name := firstNonEmpty(str(m["name"]), str(m["Name"]), str(m["file_name"]), str(m["FileName"]))
		id := firstNonEmpty(str(m["auth_index"]), str(m["AuthIndex"]), str(m["id"]), str(m["ID"]))
		// peek storage
		storage := asBytes(m["StorageJSON"])
		if len(storage) == 0 {
			storage = asBytes(m["storage_json"])
		}
		if len(storage) == 0 && id != "" {
			if got, err := hostAuthGet(id); err == nil {
				storage = got
			}
		}
		tok, _ := parseAccessToken(storage)
		if tok == "" {
			continue
		}
		if tokenFingerprint(tok) == fp || tok == accessToken {
			if name != "" {
				return ensureJSONExt(name)
			}
		}
	}
	return ""
}

func disableAuthByToken(accessToken, authFileName string, extra map[string]any) error {
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("empty token")
	}
	name := resolveAuthFileName(accessToken, authFileName, extra)
	// Merge existing storage when possible so we don't wipe email/label.
	existing := map[string]any{}
	if id := str(extra["auth_index"]); id != "" {
		if raw, err := hostAuthGet(id); err == nil {
			_ = json.Unmarshal(raw, &existing)
		}
	}
	if len(existing) == 0 {
		// try list match body
		if rawName := findAuthFileNameByToken(accessToken); rawName != "" {
			name = rawName
		}
	}
	payload := map[string]any{}
	for k, v := range existing {
		payload[k] = v
	}
	payload["type"] = providerID
	payload["access_token"] = accessToken
	payload["disabled"] = true
	payload["disabled_at"] = time.Now().UTC().Format(time.RFC3339)
	payload["disabled_reason"] = "token_invalid"
	for k, v := range extra {
		if k == "auth_index" {
			continue
		}
		payload[k] = v
	}
	return hostAuthSave(name, mustJSON(payload))
}
