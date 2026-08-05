package main

import (
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
