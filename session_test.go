package main

import (
	"testing"
)

func TestCookieJarIngestAndApply(t *testing.T) {
	s := getOrCreateSession("cookie-test-token-aaaaaaaaaaaaaaaa")
	s.ingestSetCookie(map[string][]string{
		"Set-Cookie": {
			"cf_clearance=xyz; Path=/; HttpOnly",
			"oai-sc=scvalue; Path=/",
		},
	})
	h := map[string]string{
		"OAI-Device-Id": "should-not-be-overwritten-by-cookie-only-apply",
	}
	// baseHeaders sets device; applyToHeaders only sets Cookie
	s.applyToHeaders(h)
	if h["Cookie"] == "" {
		t.Fatal("expected Cookie header")
	}
	if h["OAI-Device-Id"] != "should-not-be-overwritten-by-cookie-only-apply" {
		t.Fatalf("applyToHeaders should not overwrite device id, got %q", h["OAI-Device-Id"])
	}
	if !containsAll(h["Cookie"], "cf_clearance=xyz", "oai-sc=scvalue") {
		t.Fatalf("cookie jar incomplete: %q", h["Cookie"])
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !containsString(s, p) {
			return false
		}
	}
	return true
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (stringIndex(s, sub) >= 0)))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
