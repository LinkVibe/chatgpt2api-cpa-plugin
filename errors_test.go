package main

import "testing"

func TestIsTokenInvalidError(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"token_invalidated by server", true},
		{"authentication token has been invalidated", true},
		{"HTTP 401: invalid access token", true},
		{"HTTP 401: something else", false},
		{"rate limit 429 unauthorized pool", false},
		{"download HTTP 403: File stream access denied", false},
		{"jwt expired", true},
	}
	for _, tc := range cases {
		if got := isTokenInvalidError(tc.msg); got != tc.want {
			t.Fatalf("isTokenInvalidError(%q)=%v want %v", tc.msg, got, tc.want)
		}
	}
}

func TestValidateModel(t *testing.T) {
	if err := validateModel("gpt-image-2"); err != nil {
		t.Fatal(err)
	}
	if err := validateModel("gpt-5"); err != nil {
		t.Fatal(err)
	}
	if err := validateModel(""); err == nil {
		t.Fatal("expected error for empty model")
	}
	if err := validateModel("codex-gpt-image-2"); err == nil {
		t.Fatal("expected error for codex model")
	}
}

func TestParseSetCookiePair(t *testing.T) {
	n, v := parseSetCookiePair("oai-did=abc-123; Path=/; Secure")
	if n != "oai-did" || v != "abc-123" {
		t.Fatalf("got %q=%q", n, v)
	}
	n, v = parseSetCookiePair("Path=/")
	if n != "" {
		t.Fatalf("attr should be ignored, got %q", n)
	}
}

func TestStableIDsDeterministic(t *testing.T) {
	a1, b1 := stableIDs("token-hello")
	a2, b2 := stableIDs("token-hello")
	if a1 != a2 || b1 != b2 {
		t.Fatal("stableIDs not deterministic")
	}
	a3, _ := stableIDs("token-other")
	if a1 == a3 {
		t.Fatal("different tokens should differ")
	}
}

func TestEnsureImageBytesRejectsJSON(t *testing.T) {
	err := ensureImageBytes([]byte(`{"detail":"File stream access denied."}`), "application/json")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIsImageModel(t *testing.T) {
	if !isImageModel("gpt-image-2") {
		t.Fatal("expected image model")
	}
	if !isImageModel("dall-e-image") {
		t.Fatal("expected image model")
	}
	if isImageModel("gpt-5") {
		t.Fatal("text model should not be image")
	}
}
