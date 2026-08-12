package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	abiVersion    uint32 = 1
	schemaVersion uint32 = 2 // CPA pluginabi.SchemaVersion
	pluginName           = "chatgpt2api-cpa-plugin"
	pluginAuthor         = "LinkVibe"
	pluginRepo           = "https://github.com/LinkVibe/chatgpt2api-cpa-plugin"
)

// Overridden at link time: -ldflags "-X main.pluginVersion=1.2.3"
var pluginVersion = "0.3.7"

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var life map[string]any
		_ = json.Unmarshal(request, &life)
		if raw := asBytes(life["config_yaml"]); len(raw) > 0 {
			applyPluginConfigYAML(raw)
		}
		if raw := asBytes(life["ConfigYAML"]); len(raw) > 0 {
			applyPluginConfigYAML(raw)
		}
		return okEnvelope(registration())
	case "auth.identifier":
		return okEnvelope(map[string]any{"identifier": providerID})
	case "auth.parse":
		return handleAuthParse(request)
	case "auth.login.start":
		return okEnvelope(map[string]any{
			"Provider": providerID,
			"URL":      "",
			"State":    "",
			"Message":  "use access_token JSON auth file instead of interactive login",
		})
	case "auth.login.poll":
		return okEnvelope(map[string]any{"Status": "error", "Message": "interactive login not supported"})
	case "auth.refresh":
		return handleAuthRefresh(request)
	case "model.static":
		// No token here — return minimal fallback; real list comes from model.for_auth.
		return okEnvelope(map[string]any{"Provider": providerID, "Models": fallbackModels()})
	case "model.for_auth":
		return handleModelsForAuth(request)
	case "model.register":
		return okEnvelope(map[string]any{"Provider": providerID, "Models": fallbackModels()})
	case "executor.identifier":
		return okEnvelope(map[string]any{"identifier": providerID})
	case "executor.execute":
		return handleExecutorExecute(request, false)
	case "executor.execute_stream":
		return handleExecutorExecute(request, true)
	case "executor.count_tokens":
		return okEnvelope(map[string]any{"Payload": mustJSON(map[string]any{"total_tokens": 0})})
	case "executor.http_request":
		return okEnvelope(map[string]any{
			"StatusCode": http.StatusOK,
			"Headers":    map[string][]string{"content-type": {"application/json"}},
			"Body":       mustJSON(map[string]any{"plugin": providerID}),
		})
	case "management.register":
		return handleManagementRegister()
	case "management.handle":
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func registration() map[string]any {
	// Must satisfy CPA validPlugin(): Name/Version/Author/GitHubRepository non-empty
	// and at least one capability flag true (host builds RPC adapters from flags).
	return map[string]any{
		"schema_version": schemaVersion,
		"metadata": map[string]any{
			"Name":             pluginName,
			"Version":          pluginVersion,
			"Author":           pluginAuthor,
			"GitHubRepository": pluginRepo,
			"ConfigFields": []map[string]any{
				{"Name": "model_prefix", "Type": "string", "Description": "Plugin-level model prefix added to every registered model id and stripped before upstream calls (e.g. web-; empty = native slugs)."},
				{"Name": "default_model", "Type": "string", "Description": "Fallback model (e.g. gpt-image-2)."},
				{"Name": "image_poll_timeout_secs", "Type": "number", "Description": "Max seconds to poll conversation for image ids."},
				{"Name": "image_poll_interval_secs", "Type": "number", "Description": "Poll interval seconds."},
				{"Name": "image_initial_wait_secs", "Type": "number", "Description": "Wait before first poll after SSE."},
				{"Name": "image_settle_enabled", "Type": "boolean", "Description": "Extra settle poll after first hit."},
				{"Name": "image_settle_wait_secs", "Type": "number", "Description": "Settle wait seconds."},
				{"Name": "disable_invalid_token", "Type": "boolean", "Description": "Auto-disable auth file when access_token is invalid."},
				{"Name": "token_patrol_interval_secs", "Type": "number", "Description": "Seconds between background token validity probes (0 disables patrol)."},
			},
		},
		"capabilities": map[string]any{
			"auth_provider":        true,
			"model_provider":       true,
			"executor":             true,
			"management_api":       true,
			"executor_model_scope": "oauth",
			"executor_input_formats": []string{
				"chat-completions",
				"responses",
				"openai",
				"openai-response",
				"openai-image",
			},
			"executor_output_formats": []string{
				"chat-completions",
				"responses",
				"openai",
				"openai-response",
				"openai-image",
			},
		},
	}
}

// fallbackModels used when official fetch fails or no auth (static/register).
func fallbackModels() []map[string]any {
	ids := []string{
		"gpt-image-2",
		"auto",
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		pub := publicModel(id)
		out = append(out, map[string]any{
			"ID":                         pub,
			"Object":                     "model",
			"OwnedBy":                    providerID,
			"DisplayName":                pub + " (chatgpt web fallback)",
			"Name":                       id,
			"Type":                       "openai-image",
			"SupportedGenerationMethods": []string{"chat", "image"},
			"UserDefined":                true,
		})
	}
	return out
}

func handleModelsForAuth(request []byte) ([]byte, error) {
	var req map[string]any
	_ = json.Unmarshal(request, &req)
	storage := asBytes(req["StorageJSON"])
	if len(storage) == 0 {
		storage = asBytes(req["storage_json"])
	}
	token, _ := parseAccessToken(storage)
	if token == "" {
		// still try whole request
		token, _ = parseAccessToken(request)
	}
	if token == "" {
		return okEnvelope(map[string]any{"Provider": providerID, "Models": fallbackModels()})
	}
	var storageMap map[string]any
	_ = json.Unmarshal(storage, &storageMap)
	client := newChatGPTClientFromStorage(token, storageMap, str(req["FileName"]))
	models, err := client.listOfficialModels()
	if err != nil {
		hostLog("warn", "listOfficialModels: "+err.Error())
		if isTokenInvalidError(err.Error()) {
			client.maybeDisableToken(err.Error())
		}
		return okEnvelope(map[string]any{"Provider": providerID, "Models": fallbackModels()})
	}
	return okEnvelope(map[string]any{"Provider": providerID, "Models": models})
}

type authParseRequest struct {
	StorageJSON json.RawMessage `json:"StorageJSON"`
	Content     json.RawMessage `json:"content"`
	Body        json.RawMessage `json:"body"`
	FileName    string          `json:"FileName"`
	Path        string          `json:"Path"`
}

func handleAuthParse(request []byte) ([]byte, error) {
	var req map[string]any
	_ = json.Unmarshal(request, &req)
	raw := extractAuthJSON(req)
	token, meta := parseAccessToken(raw)
	if token == "" {
		return okEnvelope(map[string]any{"Handled": false})
	}
	email, accountID, label := resolveAccountIdentity(token, meta["email"])
	storageMap := decodeStorageMap(raw)
	sess := loadSessionFromStorage(token, storageMap)
	fileName := firstNonEmpty(str(req["FileName"]), str(req["file_name"]), shortID(token)+".json")
	disabled := storageDisabled(storageMap)
	storage := buildAuthStorage(token, email, accountID, label, sess, disabled, map[string]any{
		"file_name": fileName,
	})
	return okEnvelope(map[string]any{
		"Handled": true,
		"Auth": map[string]any{
			"Provider":    providerID,
			"ID":          shortID(token),
			"FileName":    fileName,
			"Label":       label,
			"Disabled":    disabled,
			"StorageJSON": mustJSON(storage),
			"Metadata": map[string]any{
				"type":       providerID,
				"email":      email,
				"account_id": accountID,
				"device_id":  sess.DeviceID,
			},
			"Attributes": map[string]any{
				"provider": providerID,
			},
		},
	})
}

func handleAuthRefresh(request []byte) ([]byte, error) {
	var req map[string]any
	_ = json.Unmarshal(request, &req)
	raw := extractAuthJSON(req)
	token, meta := parseAccessToken(raw)
	if token == "" {
		return okEnvelope(map[string]any{})
	}
	email, accountID, label := resolveAccountIdentity(token, meta["email"])
	storageMap := decodeStorageMap(raw)
	sess := loadSessionFromStorage(token, storageMap)
	storage := buildAuthStorage(token, email, accountID, label, sess, storageDisabled(storageMap), nil)
	return okEnvelope(map[string]any{
		"Auth": map[string]any{
			"Provider":         providerID,
			"ID":               shortID(token),
			"Label":            label,
			"StorageJSON":      mustJSON(storage),
			"NextRefreshAfter": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		},
	})
}

func handleExecutorExecute(request []byte, stream bool) ([]byte, error) {
	req, err := decodeExecutorRequest(request, stream)
	if err != nil {
		return nil, err
	}

	token, _ := parseAccessToken(req.StorageJSON)
	if token == "" {
		token, _ = parseAccessToken(req.Payload)
	}
	if token == "" {
		return nil, fmt.Errorf("access_token missing in auth storage")
	}
	storageMap := decodeStorageMap(req.StorageJSON)
	if storageDisabled(storageMap) {
		return nil, fmt.Errorf("auth disabled (invalid or revoked access_token)")
	}
	authFile := resolveAuthFileName(token, req.FileName, storageMap)
	if req.AuthIndex != "" && storageMap != nil {
		storageMap["auth_index"] = req.AuthIndex
	}

	payload, _ := requestBodyJSON(req)
	model := resolveModelName(req, payload)
	if err := validateModel(model); err != nil {
		return nil, err
	}

	client := newChatGPTClientFromStorage(token, storageMap, authFile)
	format := strings.ToLower(strings.TrimSpace(req.Format))
	isImage := isImageModel(model) || wantsImageGeneration(payload) || isImageFormat(format)

	// When the host opened a stream bridge it waits on that bridge unless we
	// return a non-empty chunk array (pluginhost/rpc_client_stream.go). Returning
	// a plain payload here would leave the bridge open and hang the client, so
	// every output path must go through host.stream.emit once StreamID is set.
	if req.StreamID != "" {
		go runExecutorStream(req, client, payload, model, format, isImage)
		return okEnvelope(map[string]any{
			"headers": map[string][]string{"content-type": {"text/event-stream"}},
		})
	}

	if isImage {
		prompt, images, n, size, quality := extractImageRequest(payload)
		if prompt == "" {
			return nil, fmt.Errorf("image prompt is required")
		}
		result, errGen := client.generateImages(prompt, model, n, size, quality, images)
		if errGen != nil {
			if isTokenInvalidError(errGen.Error()) {
				client.maybeDisableToken(errGen.Error())
			}
			return nil, errGen
		}
		client.decrementImageQuota(storageMap, authFile)
		if format == "" {
			format = "openai-image"
		}
		if isImageFormat(format) {
			return okEnvelope(map[string]any{
				"Payload": mustJSON(result),
				"Headers": map[string][]string{"Content-Type": {"application/json"}},
			})
		}
		if format == "chat-completions" || format == "openai" || format == "responses" || format == "openai-response" {
			out := wrapImageAsChat(result, model, format)
			return okEnvelope(map[string]any{
				"Payload": out,
				"Headers": map[string][]string{"Content-Type": {"application/json"}},
			})
		}
		return okEnvelope(map[string]any{
			"Payload": mustJSON(result),
			"Headers": map[string][]string{"Content-Type": {"application/json"}},
		})
	}

	messages := extractMessages(payload)
	if len(messages) == 0 {
		if p := str(payload["input"]); p != "" {
			messages = []map[string]any{{"role": "user", "content": p}}
		}
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages/input required")
	}

	if req.Stream {
		var chunks []map[string]any
		errStream := client.streamTextConversation(messages, model, func(delta string) error {
			chunk := map[string]any{
				"id":      "chatcmpl-plugin",
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   model,
				"choices": []map[string]any{{
					"index": 0,
					"delta": map[string]any{"content": delta},
				}},
			}
			chunks = append(chunks, map[string]any{"Payload": chatStreamChunkBytes(format, chunk)})
			return nil
		})
		if errStream != nil {
			if isTokenInvalidError(errStream.Error()) {
				client.maybeDisableToken(errStream.Error())
			}
			return nil, errStream
		}
		chunks = append(chunks, map[string]any{"Payload": chatStreamChunkBytes(format, finishReasonChunk(model))})
		if done := chatStreamDoneBytes(format); len(done) > 0 {
			chunks = append(chunks, map[string]any{"Payload": done})
		}
		return okEnvelope(map[string]any{
			"headers": map[string][]string{"content-type": {"text/event-stream"}},
			"chunks":  chunks,
		})
	}

	var full strings.Builder
	errText := client.streamTextConversation(messages, model, func(delta string) error {
		full.WriteString(delta)
		return nil
	})
	if errText != nil {
		if isTokenInvalidError(errText.Error()) {
			client.maybeDisableToken(errText.Error())
		}
		return nil, errText
	}
	out := map[string]any{
		"id":      "chatcmpl-plugin",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": full.String(),
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
	return okEnvelope(map[string]any{
		"Payload": mustJSON(out),
		"Headers": map[string][]string{"content-type": {"application/json"}},
	})
}

// runExecutorStream drives an executor.execute_stream request through the host
// stream bridge. It runs detached: the host expects execute_stream to return the
// headers immediately and then consume the bridge until it is closed.
//
// Text deltas are emitted one at a time instead of being accumulated into a
// chunk array, so peak memory no longer scales with response length.
func runExecutorStream(req executorRequest, client *chatgptClient, payload map[string]any, model, format string, isImage bool) {
	streamID := req.StreamID
	failed := false
	fail := func(msg string) {
		failed = true
		hostStreamClose(streamID, msg)
	}
	defer func() {
		if r := recover(); r != nil {
			hostStreamClose(streamID, fmt.Sprintf("plugin panic: %v", r))
			return
		}
		if !failed {
			hostStreamClose(streamID, "")
		}
	}()

	if isImage {
		prompt, images, n, size, quality := extractImageRequest(payload)
		if prompt == "" {
			fail("image prompt is required")
			return
		}
		result, err := client.generateImages(prompt, model, n, size, quality, images)
		if err != nil {
			if isTokenInvalidError(err.Error()) {
				client.maybeDisableToken(err.Error())
			}
			fail(err.Error())
			return
		}
		streamStorage := decodeStorageMap(req.StorageJSON)
		client.decrementImageQuota(streamStorage, resolveAuthFileName(client.accessToken, req.FileName, streamStorage))
		// /v1/images/generations has no incremental representation, so the whole
		// JSON body goes out as a single chunk. This exists to participate in the
		// bridge at all, not to stream content.
		// Format selection mirrors the non-streaming path exactly.
		if format == "" {
			format = "openai-image"
		}
		out := mustJSON(result)
		if !isImageFormat(format) {
			switch format {
			case "chat-completions", "openai", "responses", "openai-response":
				out = wrapImageAsChat(result, model, format)
			}
		}
		if err := hostStreamEmit(streamID, out); err != nil {
			fail(err.Error())
		}
		return
	}

	messages := extractMessages(payload)
	if len(messages) == 0 {
		if p := str(payload["input"]); p != "" {
			messages = []map[string]any{{"role": "user", "content": p}}
		}
	}
	if len(messages) == 0 {
		fail("messages/input required")
		return
	}

	errStream := client.streamTextConversation(messages, model, func(delta string) error {
		chunk := map[string]any{
			"id":      "chatcmpl-plugin",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"content": delta},
			}},
		}
		return hostStreamEmit(streamID, chatStreamChunkBytes(format, chunk))
	})
	if errStream != nil {
		if isTokenInvalidError(errStream.Error()) {
			client.maybeDisableToken(errStream.Error())
		}
		fail(errStream.Error())
		return
	}
	if err := hostStreamEmit(streamID, chatStreamChunkBytes(format, finishReasonChunk(model))); err != nil {
		fail(err.Error())
		return
	}
	if done := chatStreamDoneBytes(format); len(done) > 0 {
		if err := hostStreamEmit(streamID, done); err != nil {
			fail(err.Error())
		}
	}
}

// finishReasonChunk builds the terminal chat-completions chunk carrying
// finish_reason "stop" with an empty delta. The host does not synthesize this
// for plugin executors, so without it clients see finish_reason "other".
func finishReasonChunk(model string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-plugin",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}
}

func isImageModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(stripModelPrefix(model)))
	return m == "gpt-image-2" || strings.Contains(m, "image")
}

// isImageMethods reports whether a model exposes the image generation method.
func isImageMethods(methods []string) bool {
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true
		}
	}
	return false
}

// validateModel guards the inbound model so this plugin never picks up CPA
// codex models. The plugin-level prefix (if configured) is stripped first; the
// remaining slug is used for both the registry and upstream.
func validateModel(model string) error {
	m := strings.ToLower(strings.TrimSpace(stripModelPrefix(model)))
	if m == "" {
		return fmt.Errorf("model is required (e.g. gpt-image-2, gpt-5)")
	}
	if strings.Contains(m, "codex") {
		return fmt.Errorf("model %q is not handled here; use CPA codex provider", model)
	}
	return nil
}

func isImageFormat(format string) bool {
	f := strings.ToLower(strings.TrimSpace(format))
	return f == "openai-image" || f == "image" || f == "images" || f == "images-generations" || f == "images-edits"
}

// isResponsesFormat reports whether the client requested the OpenAI Responses
// streaming protocol. The host's responses SSE framer expects already-framed
// "data: ...\n\n" chunks for this format; every other format (chat-completions /
// openai) expects raw JSON chunks because the host applies the "data: %s\n\n"
// framing itself (openai_handlers.go). Emitting "data:" here too would double-
// frame and break client SSE parsers.
func isResponsesFormat(format string) bool {
	f := strings.ToLower(strings.TrimSpace(format))
	return f == "openai-response" || f == "responses"
}

// chatStreamChunkBytes renders one chat-completions delta chunk in the wire
// format the host expects for the requested output format.
func chatStreamChunkBytes(format string, chunk map[string]any) []byte {
	body := mustJSON(chunk)
	if isResponsesFormat(format) {
		return []byte("data: " + string(body) + "\n\n")
	}
	return body
}

// chatStreamDoneBytes renders the stream terminator for the requested format.
// For chat-completions the host appends "data: [DONE]\n\n" itself when the
// channel closes, so the plugin must NOT emit it (a second [DONE] makes some
// clients JSON.parse "data: [DONE]" and error out).
func chatStreamDoneBytes(format string) []byte {
	if isResponsesFormat(format) {
		return []byte("data: [DONE]\n\n")
	}
	return nil
}

func wantsImageGeneration(payload map[string]any) bool {
	if tools, ok := payload["tools"].([]any); ok {
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			if tm == nil {
				continue
			}
			if str(tm["type"]) == "image_generation" {
				return true
			}
		}
	}
	return false
}

func extractImageRequest(payload map[string]any) (prompt string, images []string, n int, size, quality string) {
	n = 1
	size = strings.TrimSpace(str(payload["size"]))
	quality = strings.TrimSpace(str(payload["quality"]))
	if quality == "" {
		quality = "auto"
	}
	if v, ok := toFloat(payload["n"]); ok && int(v) > 0 {
		n = int(v)
	}
	prompt = strings.TrimSpace(str(payload["prompt"]))
	if prompt == "" {
		if msgs, ok := payload["messages"].([]any); ok {
			for i := len(msgs) - 1; i >= 0; i-- {
				m, _ := msgs[i].(map[string]any)
				if m == nil {
					continue
				}
				if str(m["role"]) != "user" {
					continue
				}
				switch c := m["content"].(type) {
				case string:
					prompt = c
				case []any:
					for _, part := range c {
						pm, _ := part.(map[string]any)
						if pm == nil {
							continue
						}
						switch str(pm["type"]) {
						case "text":
							prompt += str(pm["text"])
						case "image_url":
							if iu, ok := pm["image_url"].(map[string]any); ok {
								if u := str(iu["url"]); u != "" {
									images = append(images, u)
								}
							} else if u := str(pm["image_url"]); u != "" {
								images = append(images, u)
							}
						case "image":
							if d := str(pm["data"]); d != "" {
								images = append(images, d)
							}
						}
					}
				}
				if prompt = strings.TrimSpace(prompt); prompt != "" {
					break
				}
			}
		}
	}
	if prompt == "" {
		prompt = extractInputPrompt(payload["input"])
	}
	// images field for edits
	if imgs, ok := payload["images"].([]any); ok {
		for _, it := range imgs {
			switch v := it.(type) {
			case string:
				images = append(images, v)
			case map[string]any:
				if u := str(v["image_url"]); u != "" {
					images = append(images, u)
				} else if u := str(v["b64_json"]); u != "" {
					images = append(images, u)
				} else if u := str(v["url"]); u != "" {
					images = append(images, u)
				}
			}
		}
	}
	if img := str(payload["image"]); img != "" {
		images = append(images, img)
	}
	return strings.TrimSpace(prompt), images, n, size, quality
}

func extractInputPrompt(input any) string {
	switch v := input.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		var b strings.Builder
		for _, it := range v {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			if str(m["role"]) != "user" && str(m["role"]) != "" {
				continue
			}
			parts, _ := m["content"].([]any)
			for _, part := range parts {
				pm, _ := part.(map[string]any)
				if pm == nil {
					continue
				}
				switch str(pm["type"]) {
				case "input_text", "text":
					b.WriteString(str(pm["text"]))
				case "input_image", "image_url":
					// images handled separately via image[] references below
				}
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func extractMessages(payload map[string]any) []map[string]any {
	raw, ok := payload["messages"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

func wrapImageAsChat(result map[string]any, model, format string) []byte {
	data, _ := result["data"].([]map[string]any)
	if data == nil {
		if arr, ok := result["data"].([]any); ok {
			for _, it := range arr {
				if m, ok := it.(map[string]any); ok {
					data = append(data, m)
				}
			}
		}
	}
	var b strings.Builder
	for i, item := range data {
		if i > 0 {
			b.WriteString("\n")
		}
		if u := str(item["url"]); u != "" {
			b.WriteString("![image](" + u + ")")
		} else if b64 := str(item["b64_json"]); b64 != "" {
			b.WriteString("data:image/png;base64,")
			b.WriteString(b64)
		}
	}
	if format == "responses" {
		return mustJSON(map[string]any{
			"id":     "resp-plugin",
			"object": "response",
			"model":  model,
			"output": []map[string]any{{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{{
					"type": "output_text",
					"text": b.String(),
				}},
			}},
		})
	}
	return mustJSON(map[string]any{
		"id":      "chatcmpl-plugin",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": b.String(),
			},
			"finish_reason": "stop",
		}},
	})
}
