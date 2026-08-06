package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// Runtime knobs (plugin config + defaults aligned with chatgpt2api).
type runtimeConfig struct {
	ModelPrefix           string
	ImagePollTimeoutSecs  float64
	ImagePollIntervalSecs float64
	ImageInitialWaitSecs  float64
	ImageSettleEnabled    bool
	ImageSettleWaitSecs   float64
	DisableInvalidToken   bool
	DefaultModel          string
}

var (
	cfgMu sync.RWMutex
	cfg   = runtimeConfig{
		ImagePollTimeoutSecs:  180,
		ImagePollIntervalSecs: 5,
		ImageInitialWaitSecs:  8,
		ImageSettleEnabled:    true,
		ImageSettleWaitSecs:   2,
		DisableInvalidToken:   true,
		DefaultModel:          "gpt-image-2",
	}
)

func currentRuntimeConfig() runtimeConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg
}

func applyPluginConfigYAML(raw []byte) {
	if len(raw) == 0 {
		return
	}
	if decoded, err := decodeLooseB64(string(raw)); err == nil {
		raw = decoded
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		// minimal YAML line parser: key: value
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			i := strings.IndexByte(line, ':')
			if i <= 0 {
				continue
			}
			k := strings.TrimSpace(line[:i])
			v := strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
			m[k] = v
		}
	}
	applyConfigMap(m)
}

// applyConfigMap applies a decoded config map (plugin config or panel /api/config
// POST body). Unknown keys are ignored so host config can carry extra fields.
func applyConfigMap(m map[string]any) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if s := strings.TrimSpace(str(m["model_prefix"])); s != "" || m["model_prefix"] != nil {
		cfg.ModelPrefix = strings.TrimSpace(str(m["model_prefix"]))
	}
	if v, ok := toFloat(m["image_poll_timeout_secs"]); ok && v > 0 {
		cfg.ImagePollTimeoutSecs = v
	}
	if v, ok := toFloat(m["image_poll_interval_secs"]); ok && v > 0 {
		cfg.ImagePollIntervalSecs = v
	}
	if v, ok := toFloat(m["image_initial_wait_secs"]); ok && v >= 0 {
		cfg.ImageInitialWaitSecs = v
	}
	if v, ok := toBool(m["image_settle_enabled"]); ok {
		cfg.ImageSettleEnabled = v
	}
	if v, ok := toFloat(m["image_settle_wait_secs"]); ok && v >= 0 {
		cfg.ImageSettleWaitSecs = v
	}
	if v, ok := toBool(m["disable_invalid_token"]); ok {
		cfg.DisableInvalidToken = v
	}
	if s := str(m["default_model"]); s != "" {
		cfg.DefaultModel = s
	}
}

// publicModel maps an upstream ChatGPT slug to the registered (public) model id,
// applying the optional plugin-level prefix. The prefix keeps this plugin's
// models from colliding with CPA's official provider names; empty by default.
func publicModel(slug string) string {
	return modelPrefix() + slug
}

// stripModelPrefix maps a public (possibly prefixed) model id back to the
// upstream ChatGPT slug. Models without the prefix pass through unchanged.
func stripModelPrefix(model string) string {
	return strings.TrimPrefix(model, modelPrefix())
}

func modelPrefix() string {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return cfg.ModelPrefix
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func toBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if s == "true" || s == "1" || s == "yes" {
			return true, true
		}
		if s == "false" || s == "0" || s == "no" {
			return false, true
		}
	}
	return false, false
}
