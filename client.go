package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	baseURL            = "https://chatgpt.com"
	defaultUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	defaultSecCHUA     = `"Chromium";v="143", "Google Chrome";v="143", "Not/A)Brand";v="99"`
	defaultClientVer   = "prod-a194cd50d4416d3c0b47c740f206b12ce60f5887"
	defaultClientBuild = "6708908"
	providerID         = "chatgpt2api-cpa-plugin"
)

var (
	// apiBaseURL mirrors baseURL but is overridable in tests.
	apiBaseURL          = baseURL
	fileServiceIDRe     = regexp.MustCompile(`file-service://([A-Za-z0-9_-]+)`)
	realImageFileIDRe   = regexp.MustCompile(`\bfile_00000000[a-f0-9]{24}\b`)
	sedimentIDRe        = regexp.MustCompile(`sediment://([A-Za-z0-9_-]+)`)
	conversationIDRe    = regexp.MustCompile(`"conversation_id"\s*:\s*"([0-9a-fA-F-]{8,})"`)
	genericFileIDRe     = regexp.MustCompile(`\b(file-[A-Za-z0-9_-]{10,})\b`)
	downloadURLInJSONRe = regexp.MustCompile(`https?://[^\s"']+`)
)

type chatRequirements struct {
	Token          string
	ProofToken     string
	TurnstileToken string
	SOToken        string
}

type chatgptClient struct {
	accessToken      string
	userAgent        string
	deviceID         string
	sessionID        string
	powScriptSources []string
	powDataBuild     string
	sess             *accountSession
	authFileName     string // for auto-disable on token invalid
}

type uploadedImage struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int    `json:"file_size"`
	MimeType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

func newChatGPTClient(accessToken string) *chatgptClient {
	sess := getOrCreateSession(accessToken)
	return &chatgptClient{
		accessToken: accessToken,
		userAgent:   defaultUA,
		deviceID:    sess.DeviceID,
		sessionID:   sess.SessionID,
		sess:        sess,
	}
}

func newChatGPTClientFromStorage(accessToken string, storage map[string]any, authFileName string) *chatgptClient {
	sess := loadSessionFromStorage(accessToken, storage)
	c := &chatgptClient{
		accessToken:  accessToken,
		userAgent:    defaultUA,
		deviceID:     sess.DeviceID,
		sessionID:    sess.SessionID,
		sess:         sess,
		authFileName: authFileName,
	}
	return c
}

func (c *chatgptClient) http(method, url string, headers map[string]string, body []byte, timeoutSec int) (int, map[string][]string, []byte, error) {
	status, hdrs, data, err := doHTTPSession(c.sess, method, url, headers, body, timeoutSec)
	if err != nil {
		c.maybeDisableToken(err.Error())
		return status, hdrs, data, err
	}
	if status == 401 || status == 403 {
		msg := fmt.Sprintf("HTTP %d: %s", status, truncate(string(data), 200))
		c.maybeDisableToken(msg)
		return status, hdrs, data, fmt.Errorf("%s", msg)
	}
	return status, hdrs, data, nil
}

func (c *chatgptClient) maybeDisableToken(msg string) {
	if c == nil || !currentRuntimeConfig().DisableInvalidToken {
		return
	}
	if !isTokenInvalidError(msg) {
		return
	}
	extra := map[string]any{}
	if c.sess != nil {
		cp := c.sess.snapshotCopy()
		extra["device_id"] = cp.DeviceID
		extra["session_state"] = cp
	}
	if err := disableAuthByToken(c.accessToken, c.authFileName, extra); err != nil {
		hostLog("warn", "auto-disable token failed: "+err.Error())
	} else {
		hostLog("warn", "access_token invalid — auth disabled")
	}
}

func (c *chatgptClient) baseHeaders(path string, extra map[string]string) map[string]string {
	// Aligned with services/openai_backend_api.py session defaults + Authorization + OAI-*.
	h := map[string]string{
		"User-Agent":                 c.userAgent,
		"Origin":                     baseURL,
		"Referer":                    baseURL + "/",
		"Accept-Language":            "zh-CN,zh;q=0.9,en;q=0.8,en-US;q=0.7",
		"Cache-Control":              "no-cache",
		"Pragma":                     "no-cache",
		"Priority":                   "u=1, i",
		"Sec-Ch-Ua":                  defaultSecCHUA,
		"Sec-Ch-Ua-Arch":             `"x86"`,
		"Sec-Ch-Ua-Bitness":          `"64"`,
		"Sec-Ch-Ua-Mobile":           "?0",
		"Sec-Ch-Ua-Model":            `""`,
		"Sec-Ch-Ua-Platform":         `"Windows"`,
		"Sec-Ch-Ua-Platform-Version": `"19.0.0"`,
		"Sec-Fetch-Dest":             "empty",
		"Sec-Fetch-Mode":             "cors",
		"Sec-Fetch-Site":             "same-origin",
		"OAI-Device-Id":              c.deviceID,
		"OAI-Session-Id":             c.sessionID,
		"OAI-Language":               "zh-CN",
		"OAI-Client-Version":         defaultClientVer,
		"OAI-Client-Build-Number":    defaultClientBuild,
	}
	if c.accessToken != "" {
		h["Authorization"] = "Bearer " + c.accessToken
	}
	if c.sess != nil {
		c.sess.applyToHeaders(h)
	}
	for k, v := range extra {
		h[k] = v
	}
	_ = path
	return h
}

const modelsCacheTTL = 5 * time.Minute

type modelsCacheEntry struct {
	models []map[string]any
	at     time.Time
}

var (
	modelsCacheMu sync.RWMutex
	modelsCache   = map[string]*modelsCacheEntry{}
)

func modelsCacheKey(accessToken string) string {
	h := sha256.Sum256([]byte(accessToken))
	return hex.EncodeToString(h[:])
}

func getModelsCache(key string) ([]map[string]any, bool) {
	modelsCacheMu.RLock()
	defer modelsCacheMu.RUnlock()
	entry, ok := modelsCache[key]
	if !ok || time.Since(entry.at) > modelsCacheTTL {
		return nil, false
	}
	out := make([]map[string]any, len(entry.models))
	copy(out, entry.models)
	return out, true
}

func setModelsCache(key string, models []map[string]any) {
	out := make([]map[string]any, len(models))
	copy(out, models)
	modelsCacheMu.Lock()
	defer modelsCacheMu.Unlock()
	modelsCache[key] = &modelsCacheEntry{models: out, at: time.Now()}
}

// listOfficialModels fetches ChatGPT website model slugs (backend-api/models), same as openai_backend_api.list_models.
func (c *chatgptClient) listOfficialModels() ([]map[string]any, error) {
	if err := c.bootstrap(); err != nil {
		return nil, err
	}
	cacheKey := modelsCacheKey(c.accessToken)
	if cached, ok := getModelsCache(cacheKey); ok {
		return cached, nil
	}
	path := "/backend-api/models?history_and_training_disabled=false"
	if c.accessToken == "" {
		path = "/backend-anon/models?iim=false&is_gizmo=false"
	}
	route := "/backend-api/models"
	if c.accessToken == "" {
		route = "/backend-anon/models"
	}
	status, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(route, map[string]string{
		"Accept": "application/json",
	}), nil, 30)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("list models failed: status=%d body=%s", status, truncate(string(body), 300))
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	rawModels, _ := root["models"].([]any)
	out := make([]map[string]any, 0, len(rawModels))
	seen := map[string]struct{}{}
	for _, item := range rawModels {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		slug := strings.TrimSpace(str(m["slug"]))
		if slug == "" {
			slug = strings.TrimSpace(str(m["id"]))
		}
		if slug == "" {
			continue
		}
		// skip codex-only slugs if any; web plugin is website path only
		if strings.Contains(strings.ToLower(slug), "codex") {
			continue
		}
		pub := publicModel(slug)
		if _, ok := seen[pub]; ok {
			continue
		}
		seen[pub] = struct{}{}
		methods := []string{"chat"}
		title := strings.ToLower(slug + " " + str(m["title"]) + " " + str(m["description"]))
		if strings.Contains(title, "image") || strings.Contains(slug, "image") || slug == "auto" {
			// auto is used for picture_v2; expose image method on auto / image-ish slugs
			if slug == "auto" || strings.Contains(slug, "image") {
				methods = []string{"chat", "image"}
			}
		}
		created := int64(0)
		switch t := m["created"].(type) {
		case float64:
			created = int64(t)
		case int64:
			created = t
		}
		row := map[string]any{
			"ID":                         pub,
			"Object":                     "model",
			"Created":                    created,
			"OwnedBy":                    providerID,
			"DisplayName":                pub + " (chatgpt web)",
			"Name":                       slug,
			"SupportedGenerationMethods": methods,
			"UserDefined":                true,
		}
		if isImageMethods(methods) {
			// Register as an OpenAI-compatible image model so the host lets it
			// through /v1/images/* without any host-side change.
			row["Type"] = "openai-image"
		}
		out = append(out, row)
	}
	// Always ensure image entry for website picture_v2 (official list may not expose gpt-image-2 slug).
	gptImageID := publicModel("gpt-image-2")
	if _, ok := seen[gptImageID]; !ok {
		out = append(out, map[string]any{
			"ID":                         gptImageID,
			"Object":                     "model",
			"OwnedBy":                    providerID,
			"DisplayName":                gptImageID + " (chatgpt web picture_v2)",
			"Name":                       "auto",
			"Type":                       "openai-image",
			"SupportedGenerationMethods": []string{"chat", "image"},
			"UserDefined":                true,
		})
	}
	autoID := publicModel("auto")
	if _, ok := seen[autoID]; !ok {
		hasAuto := false
		for _, row := range out {
			if str(row["ID"]) == autoID {
				hasAuto = true
				break
			}
		}
		if !hasAuto {
			out = append(out, map[string]any{
				"ID":                         autoID,
				"Object":                     "model",
				"OwnedBy":                    providerID,
				"DisplayName":                autoID + " (chatgpt web)",
				"Name":                       "auto",
				"Type":                       "openai-image",
				"SupportedGenerationMethods": []string{"chat", "image"},
				"UserDefined":                true,
			})
		}
	}
	setModelsCache(cacheKey, out)
	return out, nil
}

// fetchQuotaInfo pulls the live image quota + plan from the ChatGPT web API —
// the same source chatgpt2api uses. Quota comes from POST /backend-api/
// conversation/init (limits_progress.image_gen): it must be POSTed with a JSON
// body or the server answers 400 "Invalid conversation init". The plan (plan_
// type) is NOT present there; it comes from GET /backend-api/accounts/check.
func (c *chatgptClient) fetchQuotaInfo() (quotaInfo, error) {
	qi := quotaInfo{LastSyncAt: time.Now().UTC().Format(time.RFC3339)}
	if c == nil || c.accessToken == "" {
		return qi, nil
	}

	initPath := "/backend-api/conversation/init"
	initBody, err := json.Marshal(map[string]any{
		"gizmo_id":                nil,
		"requested_default_model": nil,
		"conversation_id":         nil,
		"timezone_offset_min":     -480,
	})
	if err != nil {
		return qi, err
	}
	status, _, body, err := c.http("POST", apiBaseURL+initPath, c.baseHeaders(initPath, map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}), initBody, 30)
	if err != nil {
		return qi, err
	}
	if status >= 400 {
		return qi, fmt.Errorf("conversation/init failed: status=%d body=%s", status, truncate(string(body), 300))
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return qi, err
	}
	qi.Known = false
	if limits, _ := root["limits_progress"].([]any); limits != nil {
		qi.Known = true
		for _, item := range limits {
			m, _ := item.(map[string]any)
			if m == nil || str(m["feature_name"]) != "image_gen" {
				continue
			}
			qi.Quota = intFromAny(m["remaining"])
			qi.RestoreAt = str(m["reset_after"])
			break
		}
	}
	qi.Status = "正常"
	if qi.Quota <= 0 {
		qi.Status = "限流"
	}

	qi.Plan = c.fetchPlan()
	return qi, nil
}

// fetchPlan resolves the account's subscription tier from GET /backend-api/
// accounts/check/v4-2023-04-27 (accounts.default.account.plan_type). Best-effort:
// a failure here must not mark the whole quota snapshot as errored.
func (c *chatgptClient) fetchPlan() string {
	if c == nil || c.accessToken == "" {
		return ""
	}
	path := "/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480"
	status, _, body, err := c.http("GET", apiBaseURL+path, c.baseHeaders(path, map[string]string{
		"Accept": "application/json",
	}), nil, 30)
	if err != nil || status >= 400 {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	acc, _ := root["accounts"].(map[string]any)
	if acc == nil {
		return ""
	}
	def, _ := acc["default"].(map[string]any)
	if def == nil {
		return ""
	}
	acct, _ := def["account"].(map[string]any)
	if acct == nil {
		return ""
	}
	return str(acct["plan_type"])
}

// decrementImageQuota applies one successful image generation to the stored
// quota (1 per image, mirroring chatgpt2api). Best-effort: only when the quota
// was synced from upstream, and never blocks the request path on failure.
func (c *chatgptClient) decrementImageQuota(storageMap map[string]any, authFile string) {
	if c == nil || c.accessToken == "" {
		return
	}
	var current []byte
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
	qi := quotaInfoFromMap(m)
	if !qi.Known {
		return
	}
	if qi.Quota > 0 {
		qi.Quota--
	}
	qi.Status = qi.normalizedStatus()
	qi.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	m["type"] = providerID
	m["access_token"] = c.accessToken
	m[quotaInfoKey] = qi
	name := resolveAuthFileName(c.accessToken, authFile, storageMap)
	if err := hostAuthSave(name, mustJSON(m)); err != nil {
		hostLog("warn", "decrement image quota save failed: "+err.Error())
	}
}

func (c *chatgptClient) bootstrap() error {
	if sources, build, ok := cachedPOWResources(); ok {
		c.powScriptSources, c.powDataBuild = sources, build
		return nil
	}
	status, _, body, err := c.http("GET", baseURL+"/", c.baseHeaders("/", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	}), nil, 30)
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("bootstrap failed: status=%d", status)
	}
	c.powScriptSources, c.powDataBuild = parsePOWResources(body)
	if len(c.powScriptSources) == 0 {
		c.powScriptSources = []string{defaultPOWScript}
	}
	storePOWResources(c.powScriptSources, c.powDataBuild)
	return nil
}

func (c *chatgptClient) getChatRequirements() (*chatRequirements, error) {
	basePath := "/backend-api/sentinel/chat-requirements"
	if c.accessToken == "" {
		basePath = "/backend-anon/sentinel/chat-requirements"
	}
	pToken := buildLegacyRequirementsToken(c.userAgent, c.powScriptSources, c.powDataBuild)

	preparePath := basePath + "/prepare"
	prepareBody, _ := json.Marshal(map[string]any{"p": pToken})
	status, _, body, err := c.http("POST", baseURL+preparePath, c.baseHeaders(preparePath, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}), prepareBody, 30)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("chat_requirements_prepare failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	var prepareData map[string]any
	if err := json.Unmarshal(body, &prepareData); err != nil {
		return nil, err
	}
	if arkose, _ := prepareData["arkose"].(map[string]any); arkose != nil {
		if req, _ := arkose["required"].(bool); req {
			return nil, fmt.Errorf("chat requirements requires arkose token, which is not implemented")
		}
	}

	proofToken := ""
	if powInfo, _ := prepareData["proofofwork"].(map[string]any); powInfo != nil {
		if req, _ := powInfo["required"].(bool); req {
			seed, _ := powInfo["seed"].(string)
			diff, _ := powInfo["difficulty"].(string)
			proofToken, err = buildProofToken(seed, diff, c.userAgent, c.powScriptSources, c.powDataBuild)
			if err != nil {
				return nil, err
			}
		}
	}

	turnstileToken := ""
	if ts, _ := prepareData["turnstile"].(map[string]any); ts != nil {
		if req, _ := ts["required"].(bool); req {
			if dx, _ := ts["dx"].(string); dx != "" {
				turnstileToken = solveTurnstileToken(dx, pToken)
			}
		}
	}

	prepareTok, _ := prepareData["prepare_token"].(string)
	finalizePath := basePath + "/finalize"
	finalizeBody, _ := json.Marshal(map[string]any{
		"prepare_token":   prepareTok,
		"proof_token":     proofToken,
		"turnstile_token": turnstileToken,
	})
	status, _, body, err = c.http("POST", baseURL+finalizePath, c.baseHeaders(finalizePath, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}), finalizeBody, 30)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("chat_requirements_finalize failed: status=%d body=%s", status, truncate(string(body), 500))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	token, _ := data["token"].(string)
	if token == "" {
		return nil, fmt.Errorf("missing chat requirements token: %s", truncate(string(body), 300))
	}
	so, _ := data["so_token"].(string)
	return &chatRequirements{
		Token:          token,
		ProofToken:     proofToken,
		TurnstileToken: turnstileToken,
		SOToken:        so,
	}, nil
}

func (c *chatgptClient) conversationHeaders(path string, req *chatRequirements) map[string]string {
	extra := map[string]string{
		"Accept":       "text/event-stream",
		"Content-Type": "application/json",
		"OpenAI-Sentinel-Chat-Requirements-Token": req.Token,
	}
	if req.ProofToken != "" {
		extra["OpenAI-Sentinel-Proof-Token"] = req.ProofToken
	}
	if req.TurnstileToken != "" {
		extra["OpenAI-Sentinel-Turnstile-Token"] = req.TurnstileToken
	}
	if req.SOToken != "" {
		extra["OpenAI-Sentinel-SO-Token"] = req.SOToken
	}
	return c.baseHeaders(path, extra)
}

func (c *chatgptClient) imageHeaders(path string, req *chatRequirements, conduit, accept string) map[string]string {
	extra := map[string]string{
		"Content-Type": "application/json",
		"Accept":       accept,
		"OpenAI-Sentinel-Chat-Requirements-Token": req.Token,
	}
	if req.ProofToken != "" {
		extra["OpenAI-Sentinel-Proof-Token"] = req.ProofToken
	}
	if conduit != "" {
		extra["X-Conduit-Token"] = conduit
	}
	if accept == "text/event-stream" {
		extra["X-Oai-Turn-Trace-Id"] = uuid.NewString()
	}
	return c.baseHeaders(path, extra)
}

func (c *chatgptClient) prepareImageConversation(prompt, model string, req *chatRequirements) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	upstream, effort := imageModelSettings(model)
	payload := map[string]any{
		"action":                "next",
		"fork_from_shared_post": false,
		"parent_message_id":     uuid.NewString(),
		"model":                 upstream,
		"client_prepare_state":  "success",
		"timezone_offset_min":   -480,
		"timezone":              "Asia/Shanghai",
		"conversation_mode":     map[string]any{"kind": "primary_assistant"},
		"system_hints":          []string{"picture_v2"},
		"partial_query": map[string]any{
			"id":      uuid.NewString(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{prompt}},
		},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
	}
	if effort != "" {
		payload["thinking_effort"] = effort
	}
	raw, _ := json.Marshal(payload)
	status, _, body, err := c.http("POST", baseURL+path, c.imageHeaders(path, req, "", "*/*"), raw, 60)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("prepare image conversation failed: status=%d body=%s", status, truncate(string(body), 400))
	}
	var data map[string]any
	_ = json.Unmarshal(body, &data)
	tok, _ := data["conduit_token"].(string)
	return tok, nil
}

func (c *chatgptClient) uploadImage(b64OrDataURL, fileName string) (*uploadedImage, error) {
	data, err := decodeImageBytes(b64OrDataURL)
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// try decode fully
		img, _, err2 := image.Decode(bytes.NewReader(data))
		if err2 != nil {
			return nil, fmt.Errorf("decode image: %w", err)
		}
		b := img.Bounds()
		cfg.Width, cfg.Height = b.Dx(), b.Dy()
	}
	mimeType := detectImageMIME(data)
	path := "/backend-api/files"
	metaBody, _ := json.Marshal(map[string]any{
		"file_name": fileName,
		"file_size": len(data),
		"use_case":  "multimodal",
		"width":     cfg.Width,
		"height":    cfg.Height,
	})
	status, _, body, err := c.http("POST", baseURL+path, c.baseHeaders(path, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}), metaBody, 60)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("files create failed: status=%d body=%s", status, truncate(string(body), 300))
	}
	var uploadMeta map[string]any
	if err := json.Unmarshal(body, &uploadMeta); err != nil {
		return nil, err
	}
	uploadURL, _ := uploadMeta["upload_url"].(string)
	fileID, _ := uploadMeta["file_id"].(string)
	if uploadURL == "" || fileID == "" {
		return nil, fmt.Errorf("missing upload_url/file_id")
	}
	status, _, _, err = c.http("PUT", uploadURL, map[string]string{
		"Content-Type":   mimeType,
		"x-ms-blob-type": "BlockBlob",
		"x-ms-version":   "2020-04-08",
		"Origin":         baseURL,
		"Referer":        baseURL + "/",
		"User-Agent":     c.userAgent,
		"Accept":         "application/json, text/plain, */*",
	}, data, 120)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("blob upload failed: status=%d", status)
	}
	donePath := "/backend-api/files/" + fileID + "/uploaded"
	status, _, _, err = c.http("POST", baseURL+donePath, c.baseHeaders(donePath, map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
	}), []byte("{}"), 60)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("files uploaded finalize failed: status=%d", status)
	}
	return &uploadedImage{
		FileID:   fileID,
		FileName: fileName,
		FileSize: len(data),
		MimeType: mimeType,
		Width:    cfg.Width,
		Height:   cfg.Height,
	}, nil
}

func (c *chatgptClient) startImageGeneration(prompt, model string, req *chatRequirements, conduit string, refs []*uploadedImage) ([]string, error) {
	upstream, effort := imageModelSettings(model)
	parts := make([]any, 0, len(refs)+1)
	for _, item := range refs {
		parts = append(parts, map[string]any{
			"content_type":  "image_asset_pointer",
			"asset_pointer": "file-service://" + item.FileID,
			"width":         item.Width,
			"height":        item.Height,
			"size_bytes":    item.FileSize,
		})
	}
	parts = append(parts, prompt)
	var content map[string]any
	if len(refs) > 0 {
		content = map[string]any{"content_type": "multimodal_text", "parts": parts}
	} else {
		content = map[string]any{"content_type": "text", "parts": []string{prompt}}
	}
	metadata := map[string]any{
		"developer_mode_connector_ids": []any{},
		"selected_github_repos":        []any{},
		"selected_all_github_repos":    false,
		"system_hints":                 []string{"picture_v2"},
		"serialization_metadata":       map[string]any{"custom_symbol_offsets": []any{}},
	}
	if len(refs) > 0 {
		atts := make([]map[string]any, 0, len(refs))
		for _, item := range refs {
			atts = append(atts, map[string]any{
				"id":       item.FileID,
				"mimeType": item.MimeType,
				"name":     item.FileName,
				"size":     item.FileSize,
				"width":    item.Width,
				"height":   item.Height,
			})
		}
		metadata["attachments"] = atts
	}
	payload := map[string]any{
		"action": "next",
		"messages": []map[string]any{{
			"id":          uuid.NewString(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().UnixNano()) / 1e9,
			"content":     content,
			"metadata":    metadata,
		}},
		"parent_message_id":        uuid.NewString(),
		"model":                    upstream,
		"client_prepare_state":     "sent",
		"timezone_offset_min":      -480,
		"timezone":                 "Asia/Shanghai",
		"conversation_mode":        map[string]any{"kind": "primary_assistant"},
		"enable_message_followups": true,
		"system_hints":             []string{"picture_v2"},
		"supports_buffering":       true,
		"supported_encodings":      []string{"v1"},
		"client_contextual_info": map[string]any{
			"is_dark_mode":      false,
			"time_since_loaded": 1200,
			"page_height":       1072,
			"page_width":        1724,
			"pixel_ratio":       1.2,
			"screen_height":     1440,
			"screen_width":      2560,
			"app_name":          "chatgpt.com",
		},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	if effort != "" {
		payload["thinking_effort"] = effort
	}
	path := "/backend-api/f/conversation"
	raw, _ := json.Marshal(payload)
	col := newImageIDCollector()
	textExtractor := newConversationTextExtractor("")
	facts := &imageStreamFacts{}
	var scanner sseLineScanner
	var conversationID string
	var terminalReached bool
	// Image gen SSE can stay open a long time; wall timeout ~5 min.
	streamHeaders := c.imageHeaders(path, req, conduit, "text/event-stream")
	onLine := func(line []byte) {
		if terminalReached {
			return
		}
		data := sseData(line)
		if data == nil {
			return
		}
		s := string(data)
		if conversationID == "" {
			conversationID = extractConversationID(s)
		}
		col.feed(s)
		textExtractor.extract(data)
		updateImageStreamFacts(facts, data)
		if isImageStreamTerminalPayload(data) {
			terminalReached = true
		}
	}
	status, err := doHTTPStreamSession(c.sess, "POST", baseURL+path, streamHeaders, raw, 300, func(chunk []byte) error {
		scanner.feed(chunk, onLine)
		if terminalReached {
			return errImageStreamTerminal
		}
		return nil
	})
	scanner.flush(onLine)
	if err != nil && !errors.Is(err, errImageStreamTerminal) {
		return nil, fmt.Errorf("image sse: %w", err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("image conversation failed: status=%d preview=%s", status, col.lastPreview)
	}

	messageText := strings.TrimSpace(textExtractor.text)
	if messageText == "" {
		messageText = facts.lastText
	}
	hasRefs := len(refs) > 0
	shouldPoll := shouldPollForImage(facts, hasRefs)

	// If the assistant turn was clearly not an image gen task (no tool arguments,
	// no async_task_type image_gen, no asset pointer), there is nothing to poll.
	if len(col.fileIDs) == 0 && len(col.sedimentIDs) == 0 && !shouldPoll {
		if messageText != "" {
			return nil, fmt.Errorf("image generation failed: %s", messageText)
		}
		return nil, fmt.Errorf("image generation failed: no image generated")
	}

	// Upstream often finishes SSE before file ids are committed; poll when possible.
	rt := currentRuntimeConfig()
	if conversationID != "" {
		if rt.ImageInitialWaitSecs > 0 {
			time.Sleep(time.Duration(rt.ImageInitialWaitSecs * float64(time.Second)))
		}
		// tasks error check (moderation / failed image_gen)
		if errMsg := c.checkTasksError(conversationID); errMsg != "" {
			return nil, fmt.Errorf("image task error: %s", errMsg)
		}
		deadline := time.Now().Add(time.Duration(rt.ImagePollTimeoutSecs * float64(time.Second)))
		interval := time.Duration(rt.ImagePollIntervalSecs * float64(time.Second))
		if interval < time.Second {
			interval = 5 * time.Second
		}
		for time.Now().Before(deadline) {
			if errMsg := c.checkTasksError(conversationID); errMsg != "" {
				return nil, fmt.Errorf("image task error: %s", errMsg)
			}
			moreF, moreS := c.pollConversationImageIDs(conversationID)
			col.addFile(moreF...)
			col.addSediment(moreS...)
			if len(col.fileIDs) > 0 || len(col.sedimentIDs) > 0 {
				if rt.ImageSettleEnabled && rt.ImageSettleWaitSecs > 0 {
					time.Sleep(time.Duration(rt.ImageSettleWaitSecs * float64(time.Second)))
					moreF, moreS = c.pollConversationImageIDs(conversationID)
					col.addFile(moreF...)
					col.addSediment(moreS...)
				}
				break
			}
			time.Sleep(interval)
		}
	}

	// Align with openai_backend_api._resolve_image_urls:
	// file_id  -> /backend-api/files/{id}/download
	// sediment -> /backend-api/conversation/{cid}/attachment/{id}/download
	fileIDs, sedimentIDs := col.fileIDs, col.sedimentIDs
	urls, dlErrs := c.resolveImageURLs(conversationID, fileIDs, sedimentIDs)
	if len(urls) == 0 {
		return nil, fmt.Errorf(
			"no image urls (conversation_id=%q sse_events=%d file_ids=%v sediment_ids=%v dl_errors=%v)",
			conversationID, col.events, fileIDs, sedimentIDs, dlErrs,
		)
	}
	return urls, nil
}

func (c *chatgptClient) pollConversationImageIDs(conversationID string) (fileIDs, sedimentIDs []string) {
	path := "/backend-api/conversation/" + conversationID
	status, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(path, map[string]string{
		"Accept": "application/json",
	}), nil, 60)
	if err != nil || status >= 400 {
		return nil, nil
	}
	// Prefer structured mapping extraction (same idea as _extract_image_tool_records).
	f, s := extractImageIDsFromConversationJSON(body)
	if len(f) > 0 || len(s) > 0 {
		return f, s
	}
	return extractImageIDsFromPayloads([]string{string(body)})
}

// checkTasksError mirrors openai_backend_api.check_task_error via /backend-api/tasks.
func (c *chatgptClient) checkTasksError(conversationID string) string {
	if conversationID == "" {
		return ""
	}
	path := "/backend-api/tasks"
	status, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(path, map[string]string{
		"Accept": "application/json",
	}), nil, 30)
	if err != nil || status >= 400 {
		return ""
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	tasks, _ := root["tasks"].([]any)
	for _, t := range tasks {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		cid := str(tm["conversation_id"])
		if cid == "" {
			cid = str(tm["original_conversation_id"])
		}
		if cid != "" && cid != conversationID {
			continue
		}
		imgMsg, _ := tm["image_gen_message"].(map[string]any)
		if imgMsg == nil {
			continue
		}
		meta, _ := imgMsg["metadata"].(map[string]any)
		content, _ := imgMsg["content"].(map[string]any)
		author, _ := imgMsg["author"].(map[string]any)
		isErr, _ := meta["is_error"].(bool)
		if !isErr {
			continue
		}
		if str(content["content_type"]) != "text" {
			continue
		}
		if str(author["role"]) != "assistant" {
			// still treat as error if is_error
		}
		parts, _ := content["parts"].([]any)
		var b strings.Builder
		for _, p := range parts {
			if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		msg := strings.TrimSpace(b.String())
		if msg == "" {
			msg = "image generation failed (task is_error)"
		}
		return msg
	}
	return ""
}

// resolveImageURLs mirrors openai_backend_api._resolve_image_urls.
func (c *chatgptClient) resolveImageURLs(conversationID string, fileIDs, sedimentIDs []string) (urls []string, errs []string) {
	for _, fileID := range fileIDs {
		u, err := c.getFileDownloadURL(fileID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("file %s: %v", fileID, err))
			continue
		}
		if u != "" {
			urls = appendUnique(urls, u)
		}
	}
	if conversationID != "" {
		for _, sedimentID := range sedimentIDs {
			u, err := c.getAttachmentDownloadURL(conversationID, sedimentID)
			if err != nil {
				errs = append(errs, fmt.Sprintf("sediment %s: %v", sedimentID, err))
				continue
			}
			if u != "" {
				urls = appendUnique(urls, u)
			}
		}
	}
	return urls, errs
}

func (c *chatgptClient) getFileDownloadURL(fileID string) (string, error) {
	// Root project primary path.
	path := "/backend-api/files/" + fileID + "/download"
	status, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(path, map[string]string{
		"Accept": "application/json",
	}), nil, 60)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("%s status=%d body=%s", path, status, truncate(string(body), 160))
	}
	if u := downloadURLFromJSON(body); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("%s: no download_url in %s", path, truncate(string(body), 160))
}

func (c *chatgptClient) getAttachmentDownloadURL(conversationID, attachmentID string) (string, error) {
	if conversationID == "" || attachmentID == "" {
		return "", fmt.Errorf("conversation_id/attachment_id required")
	}
	path := "/backend-api/conversation/" + conversationID + "/attachment/" + attachmentID + "/download"
	status, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(path, map[string]string{
		"Accept":                "application/json",
		"X-OpenAI-Target-Route": "/backend-api/conversation/{conversation_id}/attachment/{attachment_id}/download",
	}), nil, 60)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("attachment download meta failed: %d body=%s", status, truncate(string(body), 160))
	}
	if u := downloadURLFromJSON(body); u != "" {
		return u, nil
	}
	return "", fmt.Errorf("no attachment download url")
}

func downloadURLFromJSON(body []byte) string {
	var data map[string]any
	if json.Unmarshal(body, &data) != nil {
		return ""
	}
	for _, k := range []string{"download_url", "url", "signed_url", "downloadUrl"} {
		if u, _ := data[k].(string); strings.TrimSpace(u) != "" {
			return strings.TrimSpace(u)
		}
	}
	return ""
}

// extractImageIDsFromConversationJSON walks conversation mapping like root project.
func extractImageIDsFromConversationJSON(body []byte) (fileIDs, sedimentIDs []string) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return nil, nil
	}
	mapping, _ := root["mapping"].(map[string]any)
	if mapping == nil {
		// fallback whole doc
		return extractImageIDsFromPayloads([]string{string(body)})
	}
	for _, node := range mapping {
		nm, _ := node.(map[string]any)
		if nm == nil {
			continue
		}
		msg, _ := nm["message"].(map[string]any)
		if msg == nil {
			continue
		}
		author, _ := msg["author"].(map[string]any)
		role := strings.ToLower(str(author["role"]))
		if role != "tool" && role != "assistant" {
			continue
		}
		meta, _ := msg["metadata"].(map[string]any)
		content, _ := msg["content"].(map[string]any)
		isImageGen := str(meta["async_task_type"]) == "image_gen"
		blob, _ := json.Marshal(map[string]any{"content": content, "metadata": meta})
		f, s := extractImageIDsFromPayloads([]string{string(blob)})
		if !isImageGen && len(f) == 0 && len(s) == 0 && !hasImageAssetPointer(content) && !hasImageAssetPointer(meta) {
			continue
		}
		fileIDs = appendUnique(fileIDs, f...)
		sedimentIDs = appendUnique(sedimentIDs, s...)
	}
	return fileIDs, sedimentIDs
}

func hasImageAssetPointer(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if str(t["content_type"]) == "image_asset_pointer" {
			return true
		}
		ap := str(t["asset_pointer"])
		if strings.HasPrefix(ap, "file-service://") || strings.HasPrefix(ap, "sediment://") {
			return true
		}
		for _, child := range t {
			if hasImageAssetPointer(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if hasImageAssetPointer(child) {
				return true
			}
		}
	}
	return false
}

func (c *chatgptClient) generateImages(prompt, model string, n int, size, quality string, referenceImages []string) (map[string]any, error) {
	if c.accessToken == "" {
		return nil, fmt.Errorf("access_token is required")
	}
	if n < 1 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	if err := c.bootstrap(); err != nil {
		return nil, err
	}
	req, err := c.getChatRequirements()
	if err != nil {
		return nil, err
	}
	refs := make([]*uploadedImage, 0, len(referenceImages))
	for i, img := range referenceImages {
		up, err := c.uploadImage(img, fmt.Sprintf("image_%d.png", i+1))
		if err != nil {
			return nil, err
		}
		refs = append(refs, up)
	}
	finalPrompt := buildImagePrompt(prompt, size, quality)
	// multi-n: sequential generations
	data := make([]map[string]any, 0, n)
	var lastErr error
	for i := 0; i < n; i++ {
		reqI := req
		if i > 0 {
			reqI, err = c.getChatRequirements()
			if err != nil {
				return nil, err
			}
		}
		conduit, err := c.prepareImageConversation(finalPrompt, model, reqI)
		if err != nil {
			return nil, err
		}
		urls, err := c.startImageGeneration(finalPrompt, model, reqI, conduit, refs)
		if err != nil {
			lastErr = err
			continue
		}
		for _, u := range urls {
			imgBytes, err := c.downloadImageBytes(u)
			if err != nil {
				lastErr = err
				hostLog("warn", "image download failed: "+err.Error())
				continue
			}
			data = append(data, map[string]any{
				"b64_json": base64.StdEncoding.EncodeToString(imgBytes),
			})
		}
	}
	if len(data) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("no image generated: %w", lastErr)
		}
		return nil, fmt.Errorf("no image generated")
	}
	return map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	}, nil
}

func (c *chatgptClient) streamTextConversation(messages []map[string]any, model string, onDelta func(string) error) error {
	if err := c.bootstrap(); err != nil {
		return err
	}
	req, err := c.getChatRequirements()
	if err != nil {
		return err
	}
	path := "/backend-api/conversation"
	if c.accessToken == "" {
		path = "/backend-anon/conversation"
	}
	// Upstream ChatGPT slug equals the public id minus the optional plugin-level
	// prefix; image generation maps to "auto" (matches openai_backend_api website
	// path).
	upstreamModel := stripModelPrefix(model)
	if upstreamModel == "" || upstreamModel == "gpt-image-2" {
		upstreamModel = "auto"
	}
	payload := map[string]any{
		"action":                        "next",
		"messages":                      toConversationMessages(messages),
		"parent_message_id":             uuid.NewString(),
		"model":                         upstreamModel,
		"timezone_offset_min":           -480,
		"timezone":                      "Asia/Shanghai",
		"conversation_mode":             map[string]any{"kind": "primary_assistant"},
		"supports_buffering":            true,
		"supported_encodings":           []string{"v1"},
		"history_and_training_disabled": true,
	}
	raw, _ := json.Marshal(payload)
	var scanner sseLineScanner
	streamHeaders := c.conversationHeaders(path, req)
	extractor := newConversationTextExtractor(assistantHistoryText(messages))
	var deltaErr error
	onLine := func(line []byte) {
		if deltaErr != nil {
			return
		}
		data := sseData(line)
		if data == nil {
			return
		}
		delta := extractor.extract(data)
		if delta == "" {
			return
		}
		if onDelta != nil {
			deltaErr = onDelta(delta)
		}
	}
	status, err := doHTTPStreamSession(c.sess, "POST", baseURL+path, streamHeaders, raw, 300, func(chunk []byte) error {
		scanner.feed(chunk, onLine)
		return deltaErr
	})
	scanner.flush(onLine)
	if deltaErr != nil {
		return deltaErr
	}
	if err != nil {
		return err
	}
	if status >= 400 {
		return fmt.Errorf("conversation failed: status=%d", status)
	}
	return nil
}

func toConversationMessages(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		if role == "" {
			role = "user"
		}
		content := m["content"]
		switch v := content.(type) {
		case string:
			out = append(out, map[string]any{
				"id":      uuid.NewString(),
				"author":  map[string]any{"role": role},
				"content": map[string]any{"content_type": "text", "parts": []string{v}},
			})
		default:
			raw, _ := json.Marshal(v)
			out = append(out, map[string]any{
				"id":      uuid.NewString(),
				"author":  map[string]any{"role": role},
				"content": map[string]any{"content_type": "text", "parts": []string{string(raw)}},
			})
		}
	}
	return out
}

// sanitizeText strips ChatGPT web annotation markers from streamed text so API
// clients never see internal citation/bookmark/source icons. Two forms exist:
//
//  1. Private-use Unicode annotations: \ue200<kind>\ue202<data>\ue201. ChatGPT
//     uses these for citations ("cite"), URL links, and bookmarks. Some clients
//     render the inner "cite" label as a visible icon. We strip the entire
//     \ue200...\ue201 span plus any trailing incomplete marker.
//
//  2. Plain-text citation pointers: "citeturn0search1turn0search4" or
//     "turn0search1" — left over when the annotation delimiters were already
//     consumed upstream but the readable pointer was not.
//
// This mirrors services/protocol/conversation.py:sanitize_output_text.
var (
	annotationSpanRe   = regexp.MustCompile(`\x{e200}[^\x{e201}]*\x{e201}`)
	annotationTailRe   = regexp.MustCompile(`\x{e200}[^\x{e201}]*$`)
	citationTextRe     = regexp.MustCompile(`(?i)cite(?:turn\d+\w*)+|turn\d+search\d+`)
	privUseCharRe      = regexp.MustCompile(`[\x{e200}\x{e201}\x{e202}]`)
	spaceBeforePunctRe = regexp.MustCompile(`\s+([.,;:!?])`)
	bracketedURLRe     = regexp.MustCompile(`\[(?:https?://[^\s\]]+(?:\s+https?://[^\s\]]+)*)\]`)
	metadataTokenRe    = regexp.MustCompile(`(?i)\b(?:grouped_webpages|search_result_groups|finish_details|can_save|is_complete|stop_tokens|fallback_items|sources_footnote|supporting_websites)\b`)
)

func sanitizeText(s string) string {
	s = stripGoMapArtifacts(s)
	s = stripMetadataTokens(s)
	s = bracketedURLRe.ReplaceAllString(s, "")
	s = annotationSpanRe.ReplaceAllString(s, "")
	s = annotationTailRe.ReplaceAllString(s, "")
	s = citationTextRe.ReplaceAllString(s, "")
	s = privUseCharRe.ReplaceAllString(s, "")
	s = spaceBeforePunctRe.ReplaceAllString(s, "$1")
	return s
}

func stripMetadataTokens(s string) string {
	s = metadataTokenRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "[]", "")
}

// stripGoMapArtifacts removes Go fmt.Sprint artifacts like "map[...]" that
// leak into the stream when patch values are maps or slices. It only removes
// map literals whose contents look like internal metadata to avoid stripping
// legitimate user text (e.g. "map[string]int" in a code answer).
func stripGoMapArtifacts(s string) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(rs); i++ {
		if i+3 < len(rs) && rs[i] == 'm' && rs[i+1] == 'a' && rs[i+2] == 'p' && rs[i+3] == '[' {
			start := i + 4
			depth := 1
			j := start
			for j < len(rs) && depth > 0 {
				switch rs[j] {
				case '[':
					depth++
				case ']':
					depth--
				}
				j++
			}
			if depth == 0 {
				inner := string(rs[start : j-1])
				if looksLikeMetadataArtifact(inner) {
					i = j - 1
					continue
				}
			} else {
				// Unclosed map[...] artifact that runs to the end of the string.
				// If it looks like internal metadata, drop the rest of the string.
				if looksLikeMetadataArtifact(string(rs[start:])) {
					break
				}
			}
		}
		b.WriteRune(rs[i])
	}
	out := b.String()
	out = strings.ReplaceAll(out, "[]", "")
	return out
}

// looksLikeMetadataArtifact detects the Go map/slice artifacts produced by
// str(map) or str([]any{map}) on internal upstream metadata structures.
func looksLikeMetadataArtifact(inner string) bool {
	inner = strings.ToLower(inner)
	markers := []string{
		"<nil>",
		"alt:",
		"end_idx:",
		"start_idx:",
		"invalid:",
		"matched_text:",
		"prompt_text:",
		"refs:",
		"safe_urls:",
		"type:",
		"finish_details",
		"search_result_groups",
		"grouped_webpages",
		"sources_footnote",
		"can_save",
		"is_complete",
		"stop_tokens",
		"fallback_items",
		"attribution:",
		"pub_date:",
		"ref_id",
		"ref_type",
		"turn_index",
		"status:",
		"error:",
		"items:",
		"snippet:",
		"title:",
		"url:",
		"domain:",
		"entries:",
		"supporting_websites",
	}
	for _, m := range markers {
		if strings.Contains(inner, m) {
			return true
		}
	}
	return false
}

// conversationTextExtractor turns upstream ChatGPT SSE payloads into sanitized
// text deltas. It mirrors chatgpt2api's assistant_text / apply_text_patch and
// keeps a cumulative text state, which is required to handle message events,
// replace/patch operations and bare-v appends correctly.
type conversationTextExtractor struct {
	rawText     string
	text        string
	historyText string
}

func newConversationTextExtractor(historyText string) *conversationTextExtractor {
	return &conversationTextExtractor{historyText: historyText}
}

func (e *conversationTextExtractor) extract(payload []byte) string {
	var event map[string]any
	if json.Unmarshal(payload, &event) != nil {
		// Non-object payloads (e.g. the bare "v1" encoding-version string) are
		// protocol events, not text. chatgpt2api skips non-dict events entirely.
		return ""
	}

	nextRaw := e.nextRawText(event)
	if nextRaw == "" || nextRaw == e.rawText {
		return ""
	}
	e.rawText = nextRaw

	nextText := sanitizeText(nextRaw)
	if nextText == e.text {
		return ""
	}

	var delta string
	if strings.HasPrefix(nextText, e.text) {
		delta = nextText[len(e.text):]
	} else {
		delta = nextText
	}
	e.text = nextText
	return delta
}

func (e *conversationTextExtractor) nextRawText(event map[string]any) string {
	// Some events carry the full assistant message object. Prefer that when
	// available because it is authoritative and already cumulative.
	for _, candidate := range []any{event, event["v"]} {
		m, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := m["message"].(map[string]any)
		if !ok {
			continue
		}
		author, _ := msg["author"].(map[string]any)
		if str(author["role"]) != "assistant" {
			continue
		}
		if t := e.messageText(msg); t != "" {
			return t
		}
	}

	// Otherwise apply the patch protocol.
	return e.applyTextPatch(event, e.rawText, e.historyText)
}

func (e *conversationTextExtractor) messageText(message map[string]any) string {
	content, _ := message["content"].(map[string]any)
	if content == nil {
		return ""
	}
	// Only consume normal assistant text. Other content types (code, tool args,
	// search result JSON, etc.) should not be echoed as user-facing text.
	contentType := str(content["content_type"])
	if contentType != "" && contentType != "text" && contentType != "multimodal_text" {
		return ""
	}
	if parts, ok := content["parts"].([]any); ok && len(parts) > 0 {
		var b strings.Builder
		for _, p := range parts {
			if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	if text, ok := content["text"].(string); ok {
		return text
	}
	return ""
}

func (e *conversationTextExtractor) applyTextPatch(event map[string]any, currentText, historyText string) string {
	p, _ := event["p"].(string)
	op, _ := event["o"].(string)

	// Only apply append/replace patches that target the assistant text.
	// Metadata patches (p starting with "/message/metadata/...") must be ignored.
	if p == "/message/content/parts/0" || p == "/message/content/text" ||
		((op == "append" || op == "replace") && p == "") {
		return e.applyPatchOp(event, currentText, historyText)
	}

	if v, ok := event["v"].(string); ok && currentText != "" && p == "" && event["o"] == nil {
		return currentText + v
	}

	if ops, ok := event["v"].([]any); ok {
		text := currentText
		for _, item := range ops {
			if m, ok := item.(map[string]any); ok {
				text = e.applyTextPatch(m, text, historyText)
			}
		}
		return text
	}

	return currentText
}

func (e *conversationTextExtractor) applyPatchOp(op map[string]any, currentText, historyText string) string {
	value, ok := op["v"].(string)
	if !ok {
		// Non-string patch values are upstream metadata (maps, slices, numbers).
		// Converting them with str() leaks Go map/list literals into the output.
		return currentText
	}
	switch str(op["o"]) {
	case "append":
		return currentText + value
	case "replace":
		return stripHistory(value, historyText)
	}
	return currentText
}

func stripHistory(text, historyText string) string {
	for historyText != "" && strings.HasPrefix(text, historyText) {
		text = text[len(historyText):]
	}
	return text
}

func assistantHistoryText(messages []map[string]any) string {
	var b strings.Builder
	for _, m := range messages {
		if str(m["role"]) != "assistant" {
			continue
		}
		b.WriteString(messageContentText(m["content"]))
	}
	return b.String()
}

func messageContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, item := range v {
			switch p := item.(type) {
			case string:
				b.WriteString(p)
			case map[string]any:
				if str(p["type"]) == "text" {
					b.WriteString(str(p["text"]))
				}
			}
		}
		return b.String()
	}
	return str(content)
}

// extractTextDelta is a stateless convenience wrapper around
// conversationTextExtractor for callers and tests that do not need history.
func extractTextDelta(payload []byte) string {
	return newConversationTextExtractor("").extract(payload)
}

func imageModelSettings(model string) (upstream, effort string) {
	m := strings.ToLower(strings.TrimSpace(stripModelPrefix(model)))
	switch m {
	case "gpt-image-2":
		return "gpt-5-3", ""
	case "codex-gpt-image-2":
		return "codex-gpt-image-2", ""
	}
	return "auto", ""
}

// buildImagePrompt mirrors services/protocol/conversation.py:build_image_prompt:
// append Chinese size/quality hints so the upstream picture_v2 path receives
// the same prompt the official web UI sends.
func buildImagePrompt(prompt, size, quality string) string {
	prompt = strings.TrimSpace(prompt)
	var hints []string
	if size = strings.TrimSpace(size); size != "" {
		hints = append(hints, "输出图片尺寸为 "+size+"。")
	}
	if quality = strings.TrimSpace(quality); quality == "" {
		quality = "auto"
	}
	if quality != "" {
		hints = append(hints, "输出图片质量为 "+quality+"。")
	}
	if len(hints) == 0 {
		return prompt
	}
	return prompt + "\n\n" + strings.Join(hints, "")
}

var errImageStreamTerminal = errors.New("image stream terminal reached")

var terminalMessageStatuses = map[string]bool{
	"complete":                    true,
	"completed":                   true,
	"done":                        true,
	"finished":                    true,
	"finished_successfully":       true,
	"finished_partial_completion": true,
	"success":                     true,
	"succeeded":                   true,
}

func isImageStreamTerminalPayload(data []byte) bool {
	if len(data) > 64*1024 {
		return false
	}
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return false
	}
	return payloadHasCompletionMarker(event) && anyStatusTerminal(payloadStatusValues(event))
}

func payloadHasCompletionMarker(v any) bool {
	switch m := v.(type) {
	case map[string]any:
		for k, item := range m {
			lk := strings.ToLower(strings.TrimSpace(k))
			if lk == "is_complete" || lk == "complete" {
				if b, ok := item.(bool); ok && b {
					return true
				}
			}
			if lk == "finish_details" {
				if mm, ok := item.(map[string]any); ok && len(mm) > 0 {
					return true
				}
			}
			if payloadHasCompletionMarker(item) {
				return true
			}
		}
	case []any:
		for _, item := range m {
			if payloadHasCompletionMarker(item) {
				return true
			}
		}
	}
	return false
}

func payloadStatusValues(v any) []string {
	var vals []string
	var walk func(any)
	walk = func(v any) {
		switch m := v.(type) {
		case map[string]any:
			for k, item := range m {
				lk := strings.ToLower(strings.TrimSpace(k))
				if lk == "status" || lk == "state" {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						vals = append(vals, strings.ToLower(strings.TrimSpace(s)))
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range m {
				walk(item)
			}
		}
	}
	walk(v)
	return vals
}

func anyStatusTerminal(statuses []string) bool {
	for _, s := range statuses {
		if terminalMessageStatuses[s] {
			return true
		}
	}
	return false
}

// imageStreamFacts tracks the metadata needed to decide whether the assistant
// turn produced an image, a text refusal, or an error. It mirrors the facts
// extracted by services/protocol/conversation.py:update_conversation_state.
type imageStreamFacts struct {
	role            string
	contentType     string
	status          string
	endTurn         bool
	blocked         bool
	isError         bool
	hasText         bool
	hasAssetPointer bool
	hasImageArgs    bool
	turnUseCase     string
	asyncTaskType   string
	messageType     string
	lastText        string
}

func updateImageStreamFacts(facts *imageStreamFacts, data []byte) {
	if len(data) > 256*1024 {
		return
	}
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return
	}
	if str(event["type"]) == "server_ste_metadata" {
		if meta, ok := event["metadata"].(map[string]any); ok {
			updateFactsFromMetadata(facts, meta)
		}
		return
	}
	if msg, ok := event["message"].(map[string]any); ok {
		updateFactsFromMessage(facts, msg)
	}
	switch v := event["v"].(type) {
	case map[string]any:
		if msg, ok := v["message"].(map[string]any); ok {
			updateFactsFromMessage(facts, msg)
		} else {
			updateFactsFromPatch(facts, v)
		}
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				updateFactsFromPatch(facts, m)
			}
		}
	}
}

func updateFactsFromMessage(facts *imageStreamFacts, msg map[string]any) {
	if author, ok := msg["author"].(map[string]any); ok {
		if r := str(author["role"]); r != "" {
			facts.role = r
		}
	}
	if r := str(msg["role"]); r != "" {
		facts.role = r
	}
	if s := str(msg["status"]); s != "" {
		facts.status = s
	}
	if b, ok := msg["end_turn"].(bool); ok {
		facts.endTurn = b
	}
	if b, ok := msg["blocked"].(bool); ok && b {
		facts.blocked = true
	}
	if b, ok := msg["is_error"].(bool); ok && b {
		facts.isError = true
	}
	if content, ok := msg["content"].(map[string]any); ok {
		updateFactsFromContent(facts, content)
	}
	if meta, ok := msg["metadata"].(map[string]any); ok {
		updateFactsFromMetadata(facts, meta)
	}
}

func updateFactsFromContent(facts *imageStreamFacts, content map[string]any) {
	if ct := str(content["content_type"]); ct != "" {
		facts.contentType = ct
	}
	if t, ok := content["text"].(string); ok && strings.TrimSpace(t) != "" {
		facts.hasText = true
		if facts.role == "assistant" && (facts.contentType == "text" || facts.contentType == "code") {
			facts.lastText = strings.TrimSpace(t)
			if facts.contentType == "code" && looksLikeImageGenerationArguments(facts.lastText) {
				facts.hasImageArgs = true
			}
		}
	}
	if parts, ok := content["parts"].([]any); ok {
		for _, p := range parts {
			updateFactsFromPartValue(facts, p)
		}
	}
}

func updateFactsFromPartValue(facts *imageStreamFacts, v any) {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		facts.hasText = true
		if facts.role == "assistant" && (facts.contentType == "text" || facts.contentType == "code") {
			facts.lastText = strings.TrimSpace(s)
			if facts.contentType == "code" && looksLikeImageGenerationArguments(facts.lastText) {
				facts.hasImageArgs = true
			}
		}
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if ct := str(m["content_type"]); ct == "image_asset_pointer" {
		facts.hasAssetPointer = true
	}
	if ap := str(m["asset_pointer"]); strings.HasPrefix(ap, "file-service://") || strings.HasPrefix(ap, "sediment://") {
		facts.hasAssetPointer = true
	}
	if parts, ok := m["parts"].([]any); ok {
		for _, p := range parts {
			updateFactsFromPartValue(facts, p)
		}
	}
	if t, ok := m["text"].(string); ok {
		updateFactsFromPartValue(facts, t)
	}
}

func updateFactsFromMetadata(facts *imageStreamFacts, meta map[string]any) {
	for _, name := range []string{"async_task_type", "turn_use_case", "message_type"} {
		if v := str(meta[name]); v != "" {
			switch name {
			case "async_task_type":
				facts.asyncTaskType = v
			case "turn_use_case":
				facts.turnUseCase = v
			case "message_type":
				facts.messageType = v
			}
		}
	}
	if b, ok := meta["blocked"].(bool); ok && b {
		facts.blocked = true
	}
	if b, ok := meta["is_error"].(bool); ok && b {
		facts.isError = true
	}
	if s := str(meta["status"]); s != "" {
		facts.status = s
	}
}

func updateFactsFromPatch(facts *imageStreamFacts, op map[string]any) {
	p := str(op["p"])
	v := op["v"]
	switch {
	case strings.HasSuffix(p, "/message/author/role"):
		if s, ok := v.(string); ok {
			facts.role = strings.ToLower(strings.TrimSpace(s))
		}
	case strings.HasSuffix(p, "/message/content/content_type"):
		if s, ok := v.(string); ok {
			facts.contentType = strings.ToLower(strings.TrimSpace(s))
		}
	case strings.HasSuffix(p, "/message/content/text"):
		if s, ok := v.(string); ok {
			updateFactsFromPartValue(facts, s)
		}
	case strings.HasSuffix(p, "/message/content/parts"):
		if arr, ok := v.([]any); ok {
			for _, it := range arr {
				updateFactsFromPartValue(facts, it)
			}
		}
	case strings.Contains(p, "/message/content/parts/"):
		updateFactsFromPartValue(facts, v)
	case strings.HasSuffix(p, "/message/status"), strings.HasSuffix(p, "/message/metadata/status"):
		if s, ok := v.(string); ok {
			facts.status = strings.ToLower(strings.TrimSpace(s))
		}
	case strings.HasSuffix(p, "/message/end_turn"):
		if b, ok := v.(bool); ok {
			facts.endTurn = b
		}
	case strings.HasSuffix(p, "/message/blocked"), strings.HasSuffix(p, "/message/metadata/blocked"):
		if b, ok := v.(bool); ok && b {
			facts.blocked = true
		}
	case strings.HasSuffix(p, "/message/is_error"), strings.HasSuffix(p, "/message/metadata/is_error"):
		if b, ok := v.(bool); ok && b {
			facts.isError = true
		}
	case strings.HasSuffix(p, "/message/metadata"):
		if m, ok := v.(map[string]any); ok {
			updateFactsFromMetadata(facts, m)
		}
	default:
		parts := strings.Split(p, "/")
		if len(parts) > 0 {
			switch parts[len(parts)-1] {
			case "async_task_type":
				if s, ok := v.(string); ok {
					facts.asyncTaskType = strings.ToLower(strings.TrimSpace(s))
				}
			case "turn_use_case":
				if s, ok := v.(string); ok {
					facts.turnUseCase = strings.ToLower(strings.TrimSpace(s))
				}
			case "message_type":
				if s, ok := v.(string); ok {
					facts.messageType = strings.ToLower(strings.TrimSpace(s))
				}
			}
		}
	}
}

func looksLikeImageGenerationArguments(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return false
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return false
	}
	hasPrompt := false
	for _, k := range []string{"prompt", "instructions"} {
		if v, ok := m[k]; ok {
			if vs, ok := v.(string); ok && strings.TrimSpace(vs) != "" {
				hasPrompt = true
			}
		}
	}
	hasOptions := false
	hasN := false
	hasSize := false
	for _, k := range []string{"size", "n", "transparent_background", "is_style_transfer", "referenced_image_ids"} {
		if _, ok := m[k]; ok {
			hasOptions = true
			if k == "n" {
				hasN = true
			}
			if k == "size" {
				hasSize = true
			}
		}
	}
	return (hasPrompt && hasOptions) || (hasN && hasSize)
}

func shouldPollForImage(facts *imageStreamFacts, hasReferences bool) bool {
	if hasReferences {
		return true
	}
	if facts.asyncTaskType == "image_gen" || strings.Contains(facts.asyncTaskType, "image") {
		return true
	}
	if facts.turnUseCase == "image gen" || strings.Contains(facts.turnUseCase, "image") {
		return true
	}
	if facts.messageType == "image_gen" || facts.messageType == "image_generation" || strings.Contains(facts.messageType, "image") {
		return true
	}
	if facts.hasImageArgs {
		return true
	}
	if facts.hasAssetPointer {
		return true
	}
	return false
}

func decodeImageBytes(imageStr string) ([]byte, error) {
	s := strings.TrimSpace(imageStr)
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i >= 0 {
			s = s[i+1:]
		}
	}
	return base64.StdEncoding.DecodeString(s)
}

func detectImageMIME(data []byte) string {
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return "image/png"
}

func extractConversationID(s string) string {
	if m := conversationIDRe.FindStringSubmatch(s); len(m) > 1 {
		return m[1]
	}
	var obj map[string]any
	if json.Unmarshal([]byte(s), &obj) == nil {
		if id, _ := obj["conversation_id"].(string); id != "" {
			return id
		}
		if v, ok := obj["v"].(map[string]any); ok {
			if id, _ := v["conversation_id"].(string); id != "" {
				return id
			}
			if msg, ok := v["message"].(map[string]any); ok {
				if id, _ := msg["conversation_id"].(string); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func appendUnique(dst []string, items ...string) []string {
	seen := map[string]struct{}{}
	for _, d := range dst {
		seen[d] = struct{}{}
	}
	for _, it := range items {
		if it == "" {
			continue
		}
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		dst = append(dst, it)
	}
	return dst
}

// downloadImageBytes mirrors openai_backend_api.download_image_bytes:
// same session-style headers as all other backend calls (including Authorization).
// Root project uses one curl_cffi Session for both resolve-url and GET(url).
func (c *chatgptClient) downloadImageBytes(rawURL string) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("empty download url")
	}
	if strings.HasPrefix(rawURL, "/") {
		rawURL = baseURL + rawURL
	}

	// Same default headers as backend session (Authorization included), matching root.
	headers := c.baseHeaders("", map[string]string{
		"Accept": "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8",
	})
	// For cross-origin CDN signed URLs, Sec-Fetch-Site should not claim same-origin.
	if !strings.Contains(rawURL, "chatgpt.com") {
		headers["Sec-Fetch-Site"] = "cross-site"
		headers["Sec-Fetch-Mode"] = "no-cors"
		headers["Sec-Fetch-Dest"] = "image"
	}

	status, respHeaders, body, err := c.http("GET", rawURL, headers, nil, 120)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("download HTTP %d: %s", status, truncate(string(body), 200))
	}
	if err := ensureImageBytes(body, headerGet(respHeaders, "Content-Type")); err != nil {
		return nil, err
	}
	return body, nil
}

func ensureImageBytes(body []byte, contentType string) error {
	if len(body) < 24 {
		return fmt.Errorf("download too small (%d bytes): %s", len(body), truncate(string(body), 120))
	}
	// Detect JSON error masquerading as image (File stream access denied, etc.)
	trim := bytes.TrimSpace(body)
	if len(trim) > 0 && (trim[0] == '{' || trim[0] == '[') {
		return fmt.Errorf("download returned JSON error, not image: %s", truncate(string(trim), 200))
	}
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "json") || strings.Contains(ct, "text/html") || strings.Contains(ct, "text/plain") {
		return fmt.Errorf("download content-type=%s body=%s", contentType, truncate(string(body), 160))
	}
	// Magic bytes: PNG / JPEG / GIF / WEBP
	if bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
		return nil
	}
	if len(body) >= 3 && body[0] == 0xff && body[1] == 0xd8 && body[2] == 0xff {
		return nil
	}
	if bytes.HasPrefix(body, []byte("GIF87a")) || bytes.HasPrefix(body, []byte("GIF89a")) {
		return nil
	}
	if len(body) >= 12 && string(body[0:4]) == "RIFF" && string(body[8:12]) == "WEBP" {
		return nil
	}
	return fmt.Errorf("download is not a recognized image (len=%d ct=%s head=%q)", len(body), contentType, truncate(string(body), 80))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
