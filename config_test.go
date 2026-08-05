package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func resetConfig() {
	applyConfigMap(map[string]any{"model_prefix": ""})
}

func TestModelPrefixDefaultNative(t *testing.T) {
	resetConfig()
	defer resetConfig()
	if got := publicModel("gpt-image-2"); got != "gpt-image-2" {
		t.Fatalf("default prefix must keep native slug, got %q", got)
	}
	if got := stripModelPrefix("gpt-image-2"); got != "gpt-image-2" {
		t.Fatalf("got %q", got)
	}
}

func TestModelPrefixAppliedAndStripped(t *testing.T) {
	resetConfig()
	defer resetConfig()
	applyConfigMap(map[string]any{"model_prefix": "web-"})
	if got := publicModel("gpt-5"); got != "web-gpt-5" {
		t.Fatalf("publicModel got %q", got)
	}
	if got := publicModel("auto"); got != "web-auto" {
		t.Fatalf("publicModel(auto) got %q", got)
	}
	if got := stripModelPrefix("web-gpt-5"); got != "gpt-5" {
		t.Fatalf("stripModelPrefix got %q", got)
	}
	// unprefixed models pass through unchanged
	if got := stripModelPrefix("gpt-5"); got != "gpt-5" {
		t.Fatalf("stripModelPrefix bare got %q", got)
	}
}

func TestValidateModelWithPrefix(t *testing.T) {
	resetConfig()
	defer resetConfig()
	applyConfigMap(map[string]any{"model_prefix": "web-"})
	if err := validateModel("web-gpt-5"); err != nil {
		t.Fatal(err)
	}
	if err := validateModel("web-codex-gpt-5"); err == nil {
		t.Fatal("expected codex rejection")
	}
	if err := validateModel(""); err == nil {
		t.Fatal("expected empty rejection")
	}
}

func TestIsImageModelWithPrefix(t *testing.T) {
	resetConfig()
	defer resetConfig()
	applyConfigMap(map[string]any{"model_prefix": "web-"})
	if !isImageModel("web-gpt-image-2") {
		t.Fatal("expected prefixed image model")
	}
	if isImageModel("web-gpt-5") {
		t.Fatal("prefixed text model must not be image")
	}
}

func TestConfigYAMLSnippetIncludesPrefix(t *testing.T) {
	resetConfig()
	defer resetConfig()
	applyConfigMap(map[string]any{"model_prefix": "c2a-", "default_model": "gpt-image-2"})
	yml := configYAMLSnippet(currentRuntimeConfig())
	if !strings.Contains(yml, "model_prefix: \"c2a-\"") {
		t.Fatalf("yaml missing model_prefix:\n%s", yml)
	}
	if !strings.Contains(yml, providerID) {
		t.Fatalf("yaml missing plugin id:\n%s", yml)
	}
}

func TestValidateConfigPayload(t *testing.T) {
	if err := validateConfigPayload(map[string]any{"model_prefix": "web-"}); err != nil {
		t.Fatal(err)
	}
	if err := validateConfigPayload(map[string]any{"model_prefix": "has space"}); err == nil {
		t.Fatal("expected space rejection")
	}
	if err := validateConfigPayload(map[string]any{"default_model": "codex-1"}); err == nil {
		t.Fatal("expected codex default rejection")
	}
}

// TestChatStreamChunkFormat guards the stream wire format. The host's
// chat-completions handler applies "data: %s\n\n" itself, so the plugin must
// emit raw JSON for chat-completions (double framing breaks client SSE parsers).
// The Responses SSE framer instead expects already-framed "data: ...\n\n".
func TestChatStreamChunkFormat(t *testing.T) {
	chunk := map[string]any{"object": "chat.completion.chunk"}
	chat := chatStreamChunkBytes("chat-completions", chunk)
	if bytes.HasPrefix(chat, []byte("data:")) {
		t.Fatalf("chat-completions chunk must be raw JSON, got %q", chat)
	}
	if !json.Valid(chat) {
		t.Fatalf("chat-completions chunk must be valid JSON, got %q", chat)
	}
	resp := chatStreamChunkBytes("openai-response", chunk)
	if !bytes.HasPrefix(resp, []byte("data: ")) || !bytes.HasSuffix(resp, []byte("\n\n")) {
		t.Fatalf("responses chunk must be SSE-framed, got %q", resp)
	}
	if chatStreamDoneBytes("chat-completions") != nil {
		t.Fatal("chat-completions done must be nil — host appends [DONE]")
	}
	if string(chatStreamDoneBytes("openai-response")) != "data: [DONE]\n\n" {
		t.Fatal("responses done must be data: [DONE]\\n\\n")
	}
}

func TestSanitizeCitations(t *testing.T) {
	cases := map[string]string{
		"hello citeturn0search1turn0search4 world": "hello  world",
		"turn0search1 result":                      " result",
		"no citations here":                        "no citations here",
		"text citeturn0search1 mid citeturn0 end":  "text  mid  end",
	}
	for in, want := range cases {
		got := sanitizeCitations(in)
		if got != want {
			t.Fatalf("sanitizeCitations(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractTextDeltaSkipsVersionMarker(t *testing.T) {
	// {"v":"v1"} is the encoding version marker, not text content.
	got := extractTextDelta([]byte(`{"v":"v1"}`))
	if got != "" {
		t.Fatalf("version marker must not be extracted as text, got %q", got)
	}
	// Real append deltas are extracted.
	got = extractTextDelta([]byte(`{"p":"/message/content/parts/0","o":"append","v":"hello"}`))
	if got != "hello" {
		t.Fatalf("append delta got %q, want hello", got)
	}
}

func TestFinishReasonChunk(t *testing.T) {
	c := finishReasonChunk("gpt-5")
	choices, _ := c["choices"].([]map[string]any)
	if len(choices) != 1 {
		t.Fatal("expected one choice")
	}
	if choices[0]["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v, want stop", choices[0]["finish_reason"])
	}
	delta, _ := choices[0]["delta"].(map[string]any)
	if len(delta) != 0 {
		t.Fatalf("finish delta must be empty, got %v", delta)
	}
}

// TestManagementRegisterDeclaresConfigRoutes guards the /api/config route
// registration. The host only forwards /v0/management/plugins/<id>/api/config to
// the plugin when the route is declared here; otherwise it answers 404 HTML and
// the settings panel can neither load nor save.
func TestManagementRegisterDeclaresConfigRoutes(t *testing.T) {
	raw, err := handleManagementRegister()
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	result, _ := reg["result"].(map[string]any)
	if result == nil {
		t.Fatal("registration envelope missing result")
	}
	declared := map[string]bool{}
	for _, r := range firstSlice(result, "routes", "Routes") {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		declared[strings.ToUpper(str(m["Method"]))+" "+str(m["Path"])] = true
	}
	for _, want := range []string{
		"GET /plugins/chatgpt2api-cpa-plugin/api/config",
		"POST /plugins/chatgpt2api-cpa-plugin/api/config",
	} {
		if !declared[want] {
			t.Fatalf("management routes missing %s", want)
		}
	}
}
