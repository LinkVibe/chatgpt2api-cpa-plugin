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

// quotaInfo is the plugin-owned, upstream-synced image quota snapshot for one
// auth, stored inside the auth StorageJSON under quotaInfoKey. It mirrors the
// chatgpt2api semantics: quota is the remaining image-generation allowance the
// account has at chatgpt.com; status is 正常 / 限流 (quota exhausted); restore_at
// is the upstream reset_after timestamp. Text requests never touch it.
type quotaInfo struct {
	Plan       string `json:"plan,omitempty"`
	Quota      int    `json:"quota"`
	Known      bool   `json:"known"`
	Status     string `json:"status,omitempty"`
	RestoreAt  string `json:"restore_at,omitempty"`
	LastSyncAt string `json:"last_sync_at,omitempty"`
}

const quotaInfoKey = "quota_info"

func (qi quotaInfo) normalizedStatus() string {
	if !qi.Known {
		return "未知"
	}
	if qi.Quota <= 0 {
		return "限流"
	}
	return "正常"
}

// quotaInfoFromMap reads the quota_info object out of a decoded storage map.
func quotaInfoFromMap(m map[string]any) quotaInfo {
	raw, _ := m[quotaInfoKey].(map[string]any)
	var qi quotaInfo
	if raw == nil {
		return qi
	}
	qi.Plan = str(raw["plan"])
	qi.Quota = intFromAny(raw["quota"])
	qi.Known = boolFromAny(raw["known"])
	qi.Status = str(raw["status"])
	qi.RestoreAt = str(raw["restore_at"])
	qi.LastSyncAt = str(raw["last_sync_at"])
	return qi
}

func loadQuotaInfo(storage []byte) quotaInfo {
	var m map[string]any
	if len(storage) == 0 || json.Unmarshal(storage, &m) != nil {
		return quotaInfo{}
	}
	return quotaInfoFromMap(m)
}

// quotaInfoFromJWT fills plan from the token claims (no network); quota stays
// unknown until an upstream sync happens.
func quotaInfoFromJWT(token string) quotaInfo {
	qi := quotaInfo{LastSyncAt: time.Now().UTC().Format(time.RFC3339)}
	if claims := decodeJWTPayload(token); claims != nil {
		if plan, _ := jwtPlan(claims); plan != "" {
			qi.Plan = plan
		}
	}
	return qi
}

// saveQuotaInfo reads the current auth storage (preserving any concurrent
// session/disabled updates), merges the quota snapshot in and writes it back.
// Best-effort: callers must not block the request path on failure.
func saveQuotaInfo(token, authFile string, storageMap map[string]any, qi quotaInfo) {
	if strings.TrimSpace(token) == "" {
		return
	}
	name := resolveAuthFileName(token, authFile, storageMap)
	current := []byte(nil)
	if id := str(storageMap["auth_index"]); id != "" {
		if raw, err := hostAuthGet(id); err == nil {
			current = raw
		}
	}
	if len(current) == 0 && len(storageMap) > 0 {
		current = mustJSON(storageMap)
	}
	var m map[string]any
	if json.Unmarshal(current, &m) != nil || m == nil {
		m = map[string]any{}
	}
	m["type"] = providerID
	m["access_token"] = token
	m[quotaInfoKey] = qi
	if err := hostAuthSave(name, mustJSON(m)); err != nil {
		hostLog("warn", "save quota_info failed: "+err.Error())
	}
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
	name := strings.TrimSpace(authFileName)
	if name != "" {
		if err := setAuthDisabled(name, true, "token_invalid"); err == nil {
			return nil
		}
	}
	rec, ok := findAccountRecordByToken(accessToken)
	if ok {
		return setAuthDisabled(rec.view.Name, true, "token_invalid")
	}
	if name == "" {
		return fmt.Errorf("auth not found for token")
	}
	// Fallback to host save if the record/path cannot be located.
	existing := map[string]any{}
	if id := str(extra["auth_index"]); id != "" {
		if raw, err := hostAuthGet(id); err == nil {
			_ = json.Unmarshal(raw, &existing)
		}
	}
	if len(existing) == 0 {
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
