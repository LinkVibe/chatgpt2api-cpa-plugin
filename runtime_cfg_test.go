package main

import (
	"encoding/base64"
	"testing"
)

func TestBase64YAMLConfig(t *testing.T) {
	yaml := "model_prefix: web-\ndefault_model: gpt-image-2\nimage_poll_timeout_secs: 180\n"
	b64 := base64.StdEncoding.EncodeToString([]byte(yaml))
	applyPluginConfigYAML([]byte(b64))
	if cfg := currentRuntimeConfig(); cfg.ModelPrefix != "web-" {
		t.Fatalf("model_prefix not applied, got %q", cfg.ModelPrefix)
	}
}
