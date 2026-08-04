package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Per-access-token browser-ish state: stable device/session + cookie jar.
type accountSession struct {
	DeviceID  string            `json:"device_id"`
	SessionID string            `json:"session_id"`
	Cookies   map[string]string `json:"cookies"`
	UpdatedAt time.Time         `json:"updated_at"`
}

var (
	sessionMu    sync.RWMutex
	sessionStore = map[string]*accountSession{}
)

func tokenFingerprint(accessToken string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(accessToken)))
	return hex.EncodeToString(sum[:16])
}

func stableIDs(accessToken string) (deviceID, sessionID string) {
	sum := sha256.Sum256([]byte("c2a-device|" + strings.TrimSpace(accessToken)))
	deviceID = fmt.Sprintf("%x-%x-%x-%x-%x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	sum2 := sha256.Sum256([]byte("c2a-session|" + strings.TrimSpace(accessToken)))
	sessionID = fmt.Sprintf("%x-%x-%x-%x-%x",
		sum2[0:4], sum2[4:6], sum2[6:8], sum2[8:10], sum2[10:16])
	return deviceID, sessionID
}

func getOrCreateSession(accessToken string) *accountSession {
	key := tokenFingerprint(accessToken)
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if s, ok := sessionStore[key]; ok && s != nil {
		if s.Cookies == nil {
			s.Cookies = map[string]string{}
		}
		return s
	}
	dev, sess := stableIDs(accessToken)
	s := &accountSession{
		DeviceID:  dev,
		SessionID: sess,
		Cookies: map[string]string{
			"oai-did": dev,
		},
		UpdatedAt: time.Now().UTC(),
	}
	sessionStore[key] = s
	return s
}

func (s *accountSession) cookieHeader() string {
	if s == nil {
		return ""
	}
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	if len(s.Cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Cookies))
	for k, v := range s.Cookies {
		if k == "" || v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (s *accountSession) ingestSetCookie(headers map[string][]string) {
	if s == nil || headers == nil {
		return
	}
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if s.Cookies == nil {
		s.Cookies = map[string]string{}
	}
	for k, vals := range headers {
		if !strings.EqualFold(k, "Set-Cookie") && !strings.EqualFold(k, "Set-Cookie2") {
			continue
		}
		for _, line := range vals {
			name, value := parseSetCookiePair(line)
			if name == "" {
				continue
			}
			if value == "" || strings.EqualFold(value, "deleted") {
				delete(s.Cookies, name)
				continue
			}
			s.Cookies[name] = value
		}
	}
	// keep oai-did aligned with device id
	if s.DeviceID != "" {
		s.Cookies["oai-did"] = s.DeviceID
	}
	s.UpdatedAt = time.Now().UTC()
}

func parseSetCookiePair(line string) (name, value string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	semi := strings.IndexByte(line, ';')
	pair := line
	if semi >= 0 {
		pair = line[:semi]
	}
	eq := strings.IndexByte(pair, '=')
	if eq <= 0 {
		return "", ""
	}
	name = strings.TrimSpace(pair[:eq])
	value = strings.TrimSpace(pair[eq+1:])
	lower := strings.ToLower(name)
	switch lower {
	case "path", "domain", "expires", "max-age", "secure", "httponly", "samesite":
		return "", ""
	}
	return name, value
}

// applyToHeaders sets Cookie from jar only. Device/Session are set by baseHeaders to avoid double-write.
func (s *accountSession) applyToHeaders(h map[string]string) {
	if s == nil || h == nil {
		return
	}
	ck := s.cookieHeader()
	if ck == "" {
		return
	}
	// Replace Cookie entirely from jar (do not concatenate duplicates).
	h["Cookie"] = ck
}

func (s *accountSession) snapshotCopy() accountSession {
	if s == nil {
		return accountSession{}
	}
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	cp := accountSession{
		DeviceID:  s.DeviceID,
		SessionID: s.SessionID,
		UpdatedAt: s.UpdatedAt,
		Cookies:   map[string]string{},
	}
	for k, v := range s.Cookies {
		cp.Cookies[k] = v
	}
	return cp
}

func loadSessionFromStorage(accessToken string, storage map[string]any) *accountSession {
	s := getOrCreateSession(accessToken)
	if storage == nil {
		return s
	}
	if raw, ok := storage["session_state"]; ok {
		b, _ := json.Marshal(raw)
		var saved accountSession
		if json.Unmarshal(b, &saved) == nil {
			sessionMu.Lock()
			if saved.DeviceID != "" {
				s.DeviceID = saved.DeviceID
			}
			if saved.SessionID != "" {
				s.SessionID = saved.SessionID
			}
			// Live jar wins: the persisted snapshot is frozen at auth.parse /
			// auth.refresh time, while the in-memory jar holds whatever the last
			// response set. Saved values only fill gaps (e.g. after a restart),
			// otherwise every request would clobber a fresh __cf_bm with a stale one.
			if len(saved.Cookies) > 0 {
				if s.Cookies == nil {
					s.Cookies = map[string]string{}
				}
				for k, v := range saved.Cookies {
					if _, live := s.Cookies[k]; !live {
						s.Cookies[k] = v
					}
				}
			}
			if s.DeviceID != "" {
				s.Cookies["oai-did"] = s.DeviceID
			}
			sessionMu.Unlock()
		}
	}
	if did := str(storage["device_id"]); did != "" {
		sessionMu.Lock()
		s.DeviceID = did
		if s.Cookies == nil {
			s.Cookies = map[string]string{}
		}
		s.Cookies["oai-did"] = did
		sessionMu.Unlock()
	}
	return s
}

// isTokenInvalidError detects revoked/expired access tokens (tight matching to avoid false disables).
func isTokenInvalidError(msg string) bool {
	t := strings.ToLower(msg)
	keys := []string{
		"token_invalidated",
		"token_revoked",
		"invalid_token",
		"invalid access token",
		"invalidated oauth",
		"authentication token has been invalidated",
		"jwt expired",
		"token is expired",
		"access token has expired",
		"oauth token is invalid",
	}
	for _, k := range keys {
		if strings.Contains(t, k) {
			return true
		}
	}
	// HTTP 401 alone is not enough; require token-ish context
	if strings.Contains(t, "http 401") || strings.HasPrefix(t, "401") {
		if strings.Contains(t, "token") || strings.Contains(t, "auth") || strings.Contains(t, "unauthorized") {
			return true
		}
	}
	return false
}

func cloneHeaderMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}
