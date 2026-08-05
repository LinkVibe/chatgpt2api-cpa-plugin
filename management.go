package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var jwtLikeRe = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

func handleManagementRegister() ([]byte, error) {
	return okEnvelope(map[string]any{
		// Browser page: /v0/resource/plugins/chatgpt2api-cpa-plugin/accounts
		"resources": []map[string]any{{
			"Path":        "/accounts",
			"Menu":        "ChatGPT Access Token",
			"Description": "Manage access_token accounts; supports one-token-per-line bulk import.",
		}},
		// JSON APIs under /v0/management/... — paths must NOT end with bare /accounts
		// (old bug matched HasSuffix("/accounts") and returned HTML).
		"routes": []map[string]any{
			{"Method": "GET", "Path": "/plugins/chatgpt2api-cpa-plugin/api/list"},
			{"Method": "POST", "Path": "/plugins/chatgpt2api-cpa-plugin/api/import"},
			{"Method": "POST", "Path": "/plugins/chatgpt2api-cpa-plugin/api/delete"},
			{"Method": "POST", "Path": "/plugins/chatgpt2api-cpa-plugin/api/enable"},
			{"Method": "POST", "Path": "/plugins/chatgpt2api-cpa-plugin/api/probe"},
		},
	})
}

func handleManagement(request []byte) ([]byte, error) {
	var req map[string]any
	_ = json.Unmarshal(request, &req)

	method := strings.ToUpper(firstNonEmpty(str(req["Method"]), str(req["method"]), "GET"))
	path := firstNonEmpty(str(req["Path"]), str(req["path"]))
	path = strings.TrimSpace(path)
	lowerPath := strings.ToLower(path)

	// Only paths containing "/api/" are JSON management APIs.
	// Everything else GET is the browser HTML panel (CPA resource page).
	// This avoids mistaking /v0/resource/plugins/<id>/accounts for a list API.
	if strings.Contains(lowerPath, "/api/") {
		if method == "GET" && strings.Contains(lowerPath, "/api/list") {
			list, err := listPluginAccounts()
			if err != nil {
				return okEnvelope(mgmtJSON(http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()}))
			}
			return okEnvelope(mgmtJSON(http.StatusOK, map[string]any{"ok": true, "accounts": list}))
		}
		if method == "POST" && strings.Contains(lowerPath, "/api/import") {
			body := extractRequestBody(req)
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(body, &payload)
			if strings.TrimSpace(payload.Text) == "" {
				if t := strings.TrimSpace(string(body)); t != "" && !strings.HasPrefix(t, "{") {
					payload.Text = t
				}
			}
			result := importTokensLineByLine(payload.Text)
			return okEnvelope(mgmtJSON(http.StatusOK, result))
		}
		if method == "POST" && strings.Contains(lowerPath, "/api/delete") {
			name, errResp := mgmtNameParam(req)
			if errResp != nil {
				return okEnvelope(errResp)
			}
			if err := setAuthDisabled(name, true); err != nil {
				return okEnvelope(mgmtJSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()}))
			}
			return okEnvelope(mgmtJSON(http.StatusOK, map[string]any{"ok": true}))
		}
		if method == "POST" && strings.Contains(lowerPath, "/api/enable") {
			name, errResp := mgmtNameParam(req)
			if errResp != nil {
				return okEnvelope(errResp)
			}
			if err := setAuthDisabled(name, false); err != nil {
				return okEnvelope(mgmtJSON(http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()}))
			}
			return okEnvelope(mgmtJSON(http.StatusOK, map[string]any{"ok": true}))
		}
		if method == "POST" && strings.Contains(lowerPath, "/api/probe") {
			name, errResp := mgmtNameParam(req)
			if errResp != nil {
				return okEnvelope(errResp)
			}
			return okEnvelope(mgmtJSON(http.StatusOK, probeAccount(name)))
		}
		return okEnvelope(mgmtJSON(http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": "not found: " + method + " " + path,
		}))
	}

	// Resource / default page → always HTML for GET
	if method == "GET" || method == "" {
		return okEnvelope(pluginapiManagementHTML())
	}

	return okEnvelope(mgmtJSON(http.StatusMethodNotAllowed, map[string]any{
		"ok":    false,
		"error": "method not allowed: " + method + " " + path,
	}))
}

func pluginapiManagementHTML() map[string]any {
	// Body must be []byte so ABI JSON encodes it as base64 (CPA decodes to raw HTML).
	return map[string]any{
		"StatusCode": http.StatusOK,
		"Headers": map[string][]string{
			"Content-Type":  {"text/html; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		"Body": accountsPage,
	}
}

func extractRequestBody(req map[string]any) []byte {
	body := asBytes(req["Body"])
	if len(body) == 0 {
		body = asBytes(req["body"])
	}
	return body
}

// mgmtNameParam pulls the {"name": ...} body shared by the mutating endpoints.
// Returns a ready-to-send error response when it is missing.
func mgmtNameParam(req map[string]any) (string, map[string]any) {
	var payload struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(extractRequestBody(req), &payload)
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		return "", mgmtJSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "name required"})
	}
	return name, nil
}

func mgmtJSON(status int, v any) map[string]any {
	return map[string]any{
		"StatusCode": status,
		"Headers": map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		// []byte → base64 in ABI JSON; host decodes to raw JSON bytes
		"Body": mustJSON(v),
	}
}

type accountView struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	Provider string `json:"provider"`
	Preview  string `json:"preview"`

	// Derived from the JWT claims — no extra network call.
	AccountID     string `json:"account_id,omitempty"`
	Plan          string `json:"plan,omitempty"`
	PlanActive    bool   `json:"plan_active"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	ExpiresInDays int    `json:"expires_in_days"`
	Expired       bool   `json:"expired"`

	// Bookkeeping written by the auto-disable path / importer.
	DisabledReason string `json:"disabled_reason,omitempty"`
	DisabledAt     string `json:"disabled_at,omitempty"`
	ImportedAt     string `json:"imported_at,omitempty"`
}

// accountRecord pairs a view with the storage bytes it came from, so callers that
// need to mutate an auth file (enable/disable/probe) do not have to re-list and
// re-fetch it. The previous disable path called listPluginAccounts() and then
// hostAuthGet() again, doubling the host round-trips.
type accountRecord struct {
	view    accountView
	storage []byte
	token   string
}

func listPluginAccounts() ([]accountView, error) {
	records, err := listAccountRecords()
	if err != nil {
		return nil, err
	}
	out := make([]accountView, 0, len(records))
	for _, r := range records {
		out = append(out, r.view)
	}
	return out, nil
}

func listAccountRecords() ([]accountRecord, error) {
	raw, err := hostCall("host.auth.list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("host.auth.list: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		// bare array
		var arr []any
		if err2 := json.Unmarshal(raw, &arr); err2 == nil {
			root = map[string]any{"files": arr}
		} else {
			return nil, fmt.Errorf("decode host.auth.list: %w; raw=%s", err, truncate(string(raw), 300))
		}
	}
	files := firstSlice(root, "files", "Files", "items", "auths", "Auths", "Entries", "entries")
	out := make([]accountRecord, 0, len(files))
	for _, item := range files {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		name := firstNonEmpty(str(m["name"]), str(m["Name"]), str(m["file_name"]), str(m["FileName"]), str(m["path"]))
		provider := firstNonEmpty(str(m["provider"]), str(m["Provider"]), str(m["auth_provider"]))
		id := firstNonEmpty(str(m["auth_index"]), str(m["AuthIndex"]), str(m["id"]), str(m["ID"]))
		email := firstNonEmpty(str(m["email"]), str(m["label"]), str(m["Label"]))
		disabled := false
		if b, ok := m["disabled"].(bool); ok {
			disabled = b
		}
		if b, ok := m["Disabled"].(bool); ok {
			disabled = b
		}
		storage := asBytes(m["StorageJSON"])
		if len(storage) == 0 {
			storage = asBytes(m["storage_json"])
		}
		token, meta := parseAccessToken(storage)
		if email == "" {
			email = meta["email"]
		}
		if token == "" && provider != "" && provider != providerID {
			continue
		}
		if token == "" && id != "" {
			if got, err := hostAuthGet(id); err == nil {
				// Keep the fetched body: the type probe and the disabled_* fields
				// below need it too, and re-fetching later would cost another call.
				storage = got
				token, meta = parseAccessToken(got)
				if email == "" {
					email = meta["email"]
				}
			}
		}
		if token == "" {
			if !strings.Contains(strings.ToLower(name), "chatgpt2api") && !strings.Contains(strings.ToLower(name), "c2a-") && provider != providerID {
				continue
			}
		} else {
			var probe map[string]any
			_ = json.Unmarshal(storage, &probe)
			typ := str(probe["type"])
			if typ != "" && typ != providerID && !strings.Contains(typ, "chatgpt") {
				continue
			}
		}

		view := accountView{
			Name:     name,
			Email:    email,
			ID:       id,
			Disabled: disabled,
			Provider: firstNonEmpty(provider, providerID),
		}
		if token != "" {
			view.Preview = maskToken(token)
			applyTokenClaims(&view, token)
		}
		applyStorageFields(&view, storage)

		out = append(out, accountRecord{view: view, storage: storage, token: token})
	}
	return out, nil
}

// applyTokenClaims fills plan / expiry straight from the JWT. These are the only
// quota-ish facts obtainable without spending a request.
func applyTokenClaims(v *accountView, token string) {
	claims := decodeJWTPayload(token)
	if claims == nil {
		return
	}
	v.AccountID = jwtAccountID(claims)
	v.Plan, v.PlanActive = jwtPlan(claims)
	if exp := jwtExpiry(claims); !exp.IsZero() {
		v.ExpiresAt = exp.UTC().Format(time.RFC3339)
		v.ExpiresInDays = int(time.Until(exp).Hours() / 24)
		v.Expired = time.Now().After(exp)
	}
}

func applyStorageFields(v *accountView, storage []byte) {
	if len(storage) == 0 {
		return
	}
	var m map[string]any
	if json.Unmarshal(storage, &m) != nil {
		return
	}
	// The storage flag is what we write, so it wins in both directions —
	// otherwise re-enabling would never clear a stale disabled=true from the
	// host's list metadata.
	if b, ok := m["disabled"].(bool); ok {
		v.Disabled = b
	}
	v.DisabledReason = str(m["disabled_reason"])
	v.DisabledAt = str(m["disabled_at"])
	v.ImportedAt = str(m["imported_at"])
	if v.Email == "" {
		v.Email = firstNonEmpty(str(m["email"]), str(m["label"]))
	}
}

// jwtPlan reads the ChatGPT plan tier from the auth claim namespace.
func jwtPlan(claims map[string]any) (plan string, active bool) {
	auth, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return "", false
	}
	plan = strings.TrimSpace(str(auth["chatgpt_plan_type"]))
	if b, ok := auth["chatgpt_subscription_active"].(bool); ok {
		active = b
	}
	return plan, active
}

func jwtExpiry(claims map[string]any) time.Time {
	v, ok := claims["exp"].(float64)
	if !ok || v <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(v), 0)
}

func hostAuthGet(authIndex string) ([]byte, error) {
	raw, err := hostCall("host.auth.get", map[string]any{
		"AuthIndex":  authIndex,
		"auth_index": authIndex,
	})
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return raw, nil
	}
	if b := asBytes(root["JSON"]); len(b) > 0 {
		return b, nil
	}
	if b := asBytes(root["json"]); len(b) > 0 {
		return b, nil
	}
	if b := asBytes(root["StorageJSON"]); len(b) > 0 {
		return b, nil
	}
	if b := asBytes(root["content"]); len(b) > 0 {
		return b, nil
	}
	return raw, nil
}

func hostAuthSave(name string, content []byte) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("auth file name is empty")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		name += ".json"
	}
	// pluginapi.HostAuthSaveRequest uses json tags: name, json
	_, err := hostCall("host.auth.save", map[string]any{
		"name": name,
		"json": json.RawMessage(content),
	})
	if err != nil {
		return fmt.Errorf("host.auth.save(%s): %w", name, err)
	}
	return nil
}

// findAccountRecord locates an account by file name or auth id.
func findAccountRecord(name string) (accountRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return accountRecord{}, fmt.Errorf("name required")
	}
	records, err := listAccountRecords()
	if err != nil {
		return accountRecord{}, err
	}
	for _, r := range records {
		if r.view.Name == name || r.view.ID == name {
			return r, nil
		}
	}
	return accountRecord{}, fmt.Errorf("account not found: %s", name)
}

// setAuthDisabled flips the disabled flag on an auth file. Enabling also clears
// the auto-disable bookkeeping so a recovered token does not keep showing the
// stale reason it was disabled for.
func setAuthDisabled(name string, disabled bool) error {
	rec, err := findAccountRecord(name)
	if err != nil {
		return err
	}
	obj := map[string]any{}
	if len(rec.storage) > 0 {
		_ = json.Unmarshal(rec.storage, &obj)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj["type"] = providerID
	obj["disabled"] = disabled
	if disabled {
		obj["disabled_at"] = time.Now().UTC().Format(time.RFC3339)
		if str(obj["disabled_reason"]) == "" {
			obj["disabled_reason"] = "manual"
		}
	} else {
		delete(obj, "disabled_at")
		delete(obj, "disabled_reason")
	}
	fileName := rec.view.Name
	if fileName == "" {
		fileName = name
	}
	return hostAuthSave(ensureJSONExt(fileName), mustJSON(obj))
}

// probeAccount performs a real upstream call to tell whether the token still
// works. Deliberately on-demand only: it costs a request per account.
func probeAccount(name string) map[string]any {
	rec, err := findAccountRecord(name)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if rec.token == "" {
		return map[string]any{"ok": false, "error": "no access_token in auth file"}
	}
	var storageMap map[string]any
	_ = json.Unmarshal(rec.storage, &storageMap)
	client := newChatGPTClientFromStorage(rec.token, storageMap, rec.view.Name)
	models, err := client.listOfficialModels()
	if err != nil {
		return map[string]any{
			"ok":            false,
			"error":         err.Error(),
			"token_invalid": isTokenInvalidError(err.Error()),
		}
	}
	return map[string]any{
		"ok":     true,
		"models": len(models),
		"plan":   rec.view.Plan,
	}
}

func importTokensLineByLine(text string) map[string]any {
	lines := splitImportLines(text)
	added, skipped, failed := 0, 0, 0
	errors := make([]string, 0)
	saved := make([]string, 0)

	existing := map[string]struct{}{}
	if list, err := listPluginAccounts(); err == nil {
		for _, a := range list {
			if a.Preview != "" {
				existing[a.Preview] = struct{}{}
			}
			if a.ID != "" {
				existing[a.ID] = struct{}{}
			}
		}
	}

	for i, line := range lines {
		token, email := normalizeImportLine(line)
		if token == "" {
			skipped++
			continue
		}
		if !looksLikeAccessToken(token) {
			failed++
			errors = append(errors, fmt.Sprintf("line %d: not a jwt access_token", i+1))
			continue
		}
		fp := maskToken(token)
		if _, ok := existing[fp]; ok {
			skipped++
			continue
		}
		email, accountID, label := resolveAccountIdentity(token, email)
		fileName := fmt.Sprintf("chatgpt2api-cpa-plugin-%s.json", tokenFileSuffix(token))
		sess := getOrCreateSession(token)
		storage := buildAuthStorage(token, email, accountID, label, sess, false, map[string]any{
			"imported_at": time.Now().UTC().Format(time.RFC3339),
			"file_name":   fileName,
		})
		payload := mustJSON(storage)
		if err := hostAuthSave(fileName, payload); err != nil {
			failed++
			errors = append(errors, fmt.Sprintf("line %d: save failed: %v", i+1, err))
			continue
		}
		existing[fp] = struct{}{}
		added++
		saved = append(saved, fileName)
	}

	msg := fmt.Sprintf("imported %d, skipped %d, failed %d", added, skipped, failed)
	if len(errors) > 0 {
		// surface first few errors in message for UI
		n := len(errors)
		if n > 3 {
			n = 3
		}
		msg = msg + "; " + strings.Join(errors[:n], "; ")
	}
	if len(lines) == 0 {
		msg = "no lines to import (paste one access_token per line)"
	}
	return map[string]any{
		"ok":      failed == 0 && added > 0,
		"total":   len(lines),
		"added":   added,
		"skipped": skipped,
		"failed":  failed,
		"errors":  errors,
		"files":   saved,
		"message": msg,
	}
}

func splitImportLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	parts := strings.Split(text, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func normalizeImportLine(line string) (token, email string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}
	for _, sep := range []string{"----", "\t", ",", " "} {
		if strings.Contains(line, sep) {
			bits := strings.SplitN(line, sep, 2)
			a := strings.TrimSpace(bits[0])
			b := strings.TrimSpace(bits[1])
			if looksLikeAccessToken(a) {
				return a, pickEmail(b)
			}
			if looksLikeAccessToken(b) {
				return b, pickEmail(a)
			}
		}
	}
	if strings.HasPrefix(line, "{") {
		t, meta := parseAccessToken([]byte(line))
		return t, meta["email"]
	}
	if looksLikeAccessToken(line) {
		return line, ""
	}
	return "", ""
}

func pickEmail(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "@") {
		return s
	}
	return ""
}

// resolveAccountIdentity fills email from JWT claims when possible.
// Fallback display name: chatgpt2api-<random8>.
func resolveAccountIdentity(token, emailHint string) (email, accountID, label string) {
	email = strings.TrimSpace(emailHint)
	claims := decodeJWTPayload(token)
	if email == "" {
		email = jwtProfileEmail(claims)
	}
	accountID = jwtAccountID(claims)
	if email != "" {
		label = email
		return email, accountID, label
	}
	// no email in line and no email in JWT → chatgpt2api-<random id>
	id := randomShortID(8)
	if accountID != "" && len(accountID) >= 8 {
		id = accountID[len(accountID)-8:]
	} else {
		// prefer stable-ish id from token hash so re-import stays recognizable
		id = tokenFileSuffix(token)
		if len(id) > 8 {
			id = id[:8]
		}
	}
	label = "chatgpt2api-" + id
	email = label
	return email, accountID, label
}

func decodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload := parts[1]
	// base64url
	if b, err := decodeLooseB64URL(payload); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			return m
		}
	}
	return nil
}

func decodeLooseB64URL(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// pad
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	return decodeLooseB64(s)
}

func jwtProfileEmail(claims map[string]any) string {
	if claims == nil {
		return ""
	}
	if v := str(claims["email"]); strings.Contains(v, "@") {
		return strings.TrimSpace(v)
	}
	if profile, ok := claims["https://api.openai.com/profile"].(map[string]any); ok {
		if v := str(profile["email"]); strings.Contains(v, "@") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func jwtAccountID(claims map[string]any) string {
	if claims == nil {
		return ""
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if v := str(auth["chatgpt_account_id"]); v != "" {
			return v
		}
	}
	return ""
}

func randomShortID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	// use token-independent entropy from time+sha of nano
	seed := fmt.Sprintf("%d", time.Now().UnixNano())
	sum := sha1.Sum([]byte(seed))
	for i := 0; i < n; i++ {
		b[i] = alphabet[int(sum[i%len(sum)])%len(alphabet)]
	}
	return string(b)
}

func looksLikeAccessToken(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 40 {
		return false
	}
	return jwtLikeRe.MatchString(s) || strings.HasPrefix(s, "eyJ")
}

func maskToken(token string) string {
	if len(token) <= 16 {
		return token
	}
	return token[:8] + "..." + token[len(token)-8:]
}

func tokenFileSuffix(token string) string {
	sum := sha1.Sum([]byte(token))
	return hex.EncodeToString(sum[:6])
}

func firstSlice(root map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := root[k].([]any); ok {
			return v
		}
	}
	return nil
}

// accountsPage is served as-is on every panel GET. Kept as a package-level
// value so each request does not rebuild the string and copy it again.
var accountsPage = []byte(accountsHTML())

func accountsHTML() string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>ChatGPT Access Token</title>
<style>
:root{
  --bg:#f6f7f9; --card:#fff; --border:#e3e6ea; --border-strong:#d0d7de;
  --text:#1f2328; --muted:#656d76; --subtle:#f6f8fa;
  --accent:#1f6feb; --accent-hover:#1a5fd0;
  --ok:#1a7f37; --ok-bg:#dafbe1; --warn:#9a6700; --warn-bg:#fff8c5;
  --bad:#cf222e; --bad-bg:#ffebe9; --neutral-bg:#eff2f5;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",Roboto,Helvetica,Arial,sans-serif;
  font-size:14px;line-height:1.5;-webkit-font-smoothing:antialiased}
.wrap{max-width:1180px;margin:0 auto;padding:28px 20px 60px}
header{margin-bottom:20px}
h1{font-size:22px;font-weight:600;margin:0 0 4px;letter-spacing:-.01em}
.sub{color:var(--muted);font-size:13px}
.sub code{background:var(--subtle);border:1px solid var(--border);border-radius:4px;padding:1px 5px;font-size:12px}
.card{background:var(--card);border:1px solid var(--border);border-radius:10px;margin-bottom:16px}
.card-head{padding:14px 16px;border-bottom:1px solid var(--border);display:flex;align-items:center;gap:12px;flex-wrap:wrap}
.card-head h2{font-size:14px;font-weight:600;margin:0;flex:none}
.card-body{padding:16px}
label.fld{display:block;font-size:12px;font-weight:500;color:var(--muted);margin-bottom:6px}
input[type=text],input[type=password],textarea,select{
  width:100%;background:#fff;border:1px solid var(--border-strong);border-radius:6px;
  color:var(--text);padding:7px 10px;font-size:13px;font-family:inherit;outline:none}
input:focus,textarea:focus,select:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(31,111,235,.12)}
textarea{min-height:130px;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;resize:vertical}
.row{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-top:12px}
button{background:var(--accent);color:#fff;border:1px solid transparent;border-radius:6px;
  padding:7px 13px;cursor:pointer;font-size:13px;font-weight:500;font-family:inherit;white-space:nowrap}
button:hover{background:var(--accent-hover)}
button:disabled{opacity:.5;cursor:not-allowed}
button.sec{background:#fff;color:var(--text);border-color:var(--border-strong)}
button.sec:hover{background:var(--subtle)}
button.danger{background:#fff;color:var(--bad);border-color:#f0b8bc}
button.danger:hover{background:var(--bad-bg)}
button.sm{padding:4px 9px;font-size:12px}
.toolbar{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-left:auto}
.toolbar input[type=text]{width:200px}
.toolbar select{width:auto;min-width:110px}
.stats{display:flex;gap:18px;padding:12px 16px;border-bottom:1px solid var(--border);flex-wrap:wrap}
.stat{display:flex;align-items:baseline;gap:6px}
.stat b{font-size:17px;font-weight:600}
.stat span{font-size:12px;color:var(--muted)}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{padding:9px 12px;text-align:left;border-bottom:1px solid var(--border);vertical-align:middle}
th{color:var(--muted);font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:.03em;
  background:var(--subtle);user-select:none}
th.sortable{cursor:pointer}
th.sortable:hover{color:var(--text)}
th .arrow{opacity:.4;font-size:9px}
tbody tr:hover{background:#fafbfc}
tbody tr:last-child td{border-bottom:0}
td.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;color:var(--muted)}
td.name{font-weight:500;color:var(--text)}
.badge{display:inline-block;padding:1px 8px;border-radius:999px;font-size:11px;font-weight:600;
  background:var(--neutral-bg);color:var(--muted);white-space:nowrap}
.badge.ok{background:var(--ok-bg);color:var(--ok)}
.badge.warn{background:var(--warn-bg);color:var(--warn)}
.badge.bad{background:var(--bad-bg);color:var(--bad)}
.acts{display:flex;gap:6px;justify-content:flex-end}
.msg{font-size:13px;color:var(--muted);margin-top:10px}
.msg.ok{color:var(--ok)}.msg.bad{color:var(--bad)}
.empty{padding:36px;text-align:center;color:var(--muted);font-size:13px}
.hint{font-size:12px;color:var(--muted);margin-top:8px}
.errs{margin:10px 0 0;padding:10px 12px;background:var(--bad-bg);border:1px solid #f0b8bc;
  border-radius:6px;font-size:12px;color:#7d2028;max-height:180px;overflow:auto}
.errs div{padding:1px 0;font-family:ui-monospace,Consolas,monospace}
.probe{font-size:11px;margin-top:3px;color:var(--muted)}
input[type=checkbox]{width:14px;height:14px;cursor:pointer;accent-color:var(--accent);margin:0}
.bulk{display:none;gap:8px;align-items:center;padding:10px 16px;background:#ddf4ff;
  border-bottom:1px solid #b6e3ff;font-size:13px}
.bulk.on{display:flex}
</style>
</head>
<body>
<div class="wrap">
  <header>
    <h1>ChatGPT Access Token</h1>
    <div class="sub">Plugin <code>chatgpt2api-cpa-plugin</code> · 每行一个 access_token，或 <code>email----token</code></div>
  </header>

  <div class="card">
    <div class="card-head"><h2>管理密钥</h2></div>
    <div class="card-body">
      <label class="fld" for="mgmtKey">Management key（保存在 localStorage，用于调用 /v0/management）</label>
      <input id="mgmtKey" type="password" placeholder="CPA management secret / API key" autocomplete="off"/>
      <div class="row">
        <button type="button" id="saveKey">保存密钥</button>
        <button type="button" class="sec" id="reloadBtn">刷新列表</button>
      </div>
      <div class="hint">若 CPA 管理端已登录，可能会自动读取到密钥。导入与启停仅走 management API。</div>
    </div>
  </div>

  <div class="card">
    <div class="card-head"><h2>批量导入</h2></div>
    <div class="card-body">
      <label class="fld" for="importText">每行一个 access_token</label>
      <textarea id="importText" placeholder="eyJhbGciOi...&#10;user@example.com----eyJhbGciOi...&#10;{"access_token":"eyJ...","email":"a@b.c"}"></textarea>
      <div class="row">
        <button type="button" id="importBtn">导入</button>
        <button type="button" class="sec" id="clearBtn">清空</button>
      </div>
      <div id="importMsg" class="msg"></div>
      <div id="importErrs" class="errs" style="display:none"></div>
    </div>
  </div>

  <div class="card">
    <div class="card-head">
      <h2>账号</h2>
      <div class="toolbar">
        <input type="text" id="search" placeholder="搜索文件名 / 邮箱 / token"/>
        <select id="statusFilter">
          <option value="all">全部状态</option>
          <option value="enabled">仅启用</option>
          <option value="disabled">仅禁用</option>
          <option value="expiring">即将过期</option>
          <option value="expired">已过期</option>
        </select>
      </div>
    </div>
    <div class="stats" id="stats"></div>
    <div class="bulk" id="bulk">
      <span id="bulkCount"></span>
      <button type="button" class="sec sm" id="bulkEnable">批量启用</button>
      <button type="button" class="danger sm" id="bulkDisable">批量禁用</button>
      <button type="button" class="danger sm" id="bulkDelete">批量删除</button>
      <button type="button" class="sec sm" id="bulkProbe">批量检测</button>
      <button type="button" class="sec sm" id="bulkClear">取消选择</button>
    </div>
    <table>
      <thead><tr>
        <th style="width:32px"><input type="checkbox" id="selAll"/></th>
        <th class="sortable" data-sort="name">文件 <span class="arrow"></span></th>
        <th class="sortable" data-sort="email">邮箱 / 标签 <span class="arrow"></span></th>
        <th class="sortable" data-sort="plan">套餐 <span class="arrow"></span></th>
        <th class="sortable" data-sort="expires">Token 有效期 <span class="arrow"></span></th>
        <th class="sortable" data-sort="status">状态 <span class="arrow"></span></th>
        <th style="width:180px"></th>
      </tr></thead>
      <tbody id="tbody"></tbody>
    </table>
    <div id="listMsg" class="empty">加载中…</div>
  </div>
</div>
<script>
(function(){
  var PLUGIN = 'chatgpt2api-cpa-plugin';
  var API = '/v0/management/plugins/' + PLUGIN + '/api';
  var KEY_STORE = 'chatgpt2api_cpa_plugin_mgmt_key';
  var FALLBACK_KEYS = ['cpa_management_key','management_key','cliproxy_management_key','auth_key','managementKey'];

  var accounts = [];
  var probes = {};
  var selected = {};
  var sortKey = 'name', sortDir = 1;

  function $(id){ return document.getElementById(id); }
  function esc(s){
    return String(s == null ? '' : s).replace(/[&<>"']/g, function(c){
      return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c];
    });
  }

  function getKey(){
    var manual = localStorage.getItem(KEY_STORE) || '';
    if (manual) return manual;
    for (var i=0;i<FALLBACK_KEYS.length;i++){
      var v = localStorage.getItem(FALLBACK_KEYS[i]);
      if (v) return v;
    }
    return $('mgmtKey').value.trim();
  }

  async function api(path, opts){
    var key = getKey();
    if (!key) throw new Error('请先填写并保存 management key');
    var res = await fetch('/v0/management' + path, Object.assign({
      headers: {
        'Authorization': 'Bearer ' + key,
        'Content-Type': 'application/json',
        'Accept': 'application/json'
      }
    }, opts || {}));
    var ct = (res.headers.get('content-type') || '').toLowerCase();
    var text = await res.text();
    if (ct.indexOf('text/html') >= 0 || text.trimStart().indexOf('<!doctype') === 0 || text.trimStart().indexOf('<html') === 0) {
      throw new Error('接口返回了 HTML 而不是 JSON（路由匹配问题）status=' + res.status);
    }
    var data;
    try { data = JSON.parse(text); }
    catch(e){ throw new Error('返回的不是合法 JSON (status ' + res.status + '): ' + text.slice(0,200)); }
    if (!res.ok) throw new Error((data && (data.error || data.message)) || ('HTTP ' + res.status));
    return data;
  }

  function planBadge(a){
    if (!a.plan) return '<span class="badge">—</span>';
    var cls = a.plan_active ? 'ok' : '';
    return '<span class="badge ' + cls + '">' + esc(a.plan) + '</span>';
  }

  function expiryCell(a){
    if (!a.expires_at) return '<span class="badge">未知</span>';
    var d = a.expires_in_days;
    var date = esc(a.expires_at.slice(0,10));
    if (a.expired) return '<span class="badge bad">已过期</span> <span class="probe">' + date + '</span>';
    if (d <= 7) return '<span class="badge warn">' + d + ' 天</span> <span class="probe">' + date + '</span>';
    return '<span class="badge ok">' + d + ' 天</span> <span class="probe">' + date + '</span>';
  }

  function statusCell(a){
    var out;
    if (a.disabled) {
      out = '<span class="badge bad">已禁用</span>';
      if (a.disabled_reason) out += ' <span class="probe">' + esc(a.disabled_reason) + '</span>';
    } else {
      out = '<span class="badge ok">启用</span>';
    }
    var p = probes[a.name];
    if (p) {
      out += '<div class="probe">' + (p.ok
        ? '✓ 可用 · ' + esc(String(p.models)) + ' models'
        : '✗ ' + esc(String(p.error || '').slice(0,80))) + '</div>';
    }
    return out;
  }

  function sortVal(a, key){
    if (key === 'name')   return (a.name || '').toLowerCase();
    if (key === 'email')  return (a.email || '').toLowerCase();
    if (key === 'plan')   return (a.plan || '').toLowerCase();
    if (key === 'expires') return a.expires_at ? a.expires_in_days : 1e9;
    if (key === 'status') return a.disabled ? 1 : 0;
    return '';
  }

  function visible(){
    var q = $('search').value.trim().toLowerCase();
    var st = $('statusFilter').value;
    var out = accounts.filter(function(a){
      if (q) {
        var hay = ((a.name||'') + ' ' + (a.email||'') + ' ' + (a.preview||'') + ' ' + (a.plan||'')).toLowerCase();
        if (hay.indexOf(q) < 0) return false;
      }
      if (st === 'enabled'  && a.disabled) return false;
      if (st === 'disabled' && !a.disabled) return false;
      if (st === 'expired'  && !a.expired) return false;
      if (st === 'expiring' && !(a.expires_at && !a.expired && a.expires_in_days <= 7)) return false;
      return true;
    });
    out.sort(function(x,y){
      var vx = sortVal(x, sortKey), vy = sortVal(y, sortKey);
      if (vx < vy) return -sortDir;
      if (vx > vy) return sortDir;
      return 0;
    });
    return out;
  }

  function renderStats(){
    var total = accounts.length;
    var off = accounts.filter(function(a){ return a.disabled; }).length;
    var exp = accounts.filter(function(a){ return a.expired; }).length;
    var soon = accounts.filter(function(a){ return a.expires_at && !a.expired && a.expires_in_days <= 7; }).length;
    $('stats').innerHTML =
      '<div class="stat"><b>' + total + '</b><span>总计</span></div>' +
      '<div class="stat"><b>' + (total - off) + '</b><span>启用</span></div>' +
      '<div class="stat"><b>' + off + '</b><span>禁用</span></div>' +
      '<div class="stat"><b>' + soon + '</b><span>7 天内过期</span></div>' +
      '<div class="stat"><b>' + exp + '</b><span>已过期</span></div>';
  }

  function renderBulk(){
    var names = Object.keys(selected).filter(function(n){ return selected[n]; });
    $('bulk').className = names.length ? 'bulk on' : 'bulk';
    $('bulkCount').textContent = '已选 ' + names.length + ' 项';
  }

  function render(){
    var rows = visible();
    var tb = $('tbody');
    renderStats();

    document.querySelectorAll('th.sortable').forEach(function(th){
      var arrow = th.querySelector('.arrow');
      arrow.textContent = th.dataset.sort === sortKey ? (sortDir > 0 ? '▲' : '▼') : '';
    });

    if (!rows.length) {
      tb.innerHTML = '';
      $('listMsg').style.display = 'block';
      $('listMsg').textContent = accounts.length ? '没有匹配的账号' : '暂无账号，先在上面导入 access_token';
      renderBulk();
      return;
    }
    $('listMsg').style.display = 'none';

    tb.innerHTML = rows.map(function(a){
      var n = esc(a.name || '');
      return '<tr>' +
        '<td><input type="checkbox" data-sel="' + n + '"' + (selected[a.name] ? ' checked' : '') + '/></td>' +
        '<td class="name">' + n + '</td>' +
        '<td>' + esc(a.email || '—') + '<div class="probe">' + esc(a.preview || '') + '</div></td>' +
        '<td>' + planBadge(a) + '</td>' +
        '<td>' + expiryCell(a) + '</td>' +
        '<td>' + statusCell(a) + '</td>' +
        '<td><div class="acts">' +
          '<button class="sec sm" data-act="probe" data-name="' + n + '">检测</button>' +
          (a.disabled
            ? '<button class="sec sm" data-act="enable" data-name="' + n + '">启用</button>'
            : '<button class="danger sm" data-act="disable" data-name="' + n + '">禁用</button>') +
          '<button class="danger sm" data-act="delete" data-name="' + n + '">删除</button>' +
        '</div></td>' +
      '</tr>';
    }).join('');
    renderBulk();
  }

  async function loadList(){
    $('listMsg').style.display = 'block';
    $('listMsg').textContent = '加载中…';
    try {
      var data = await api('/plugins/' + PLUGIN + '/api/list');
      accounts = (data && data.accounts) || [];
      render();
    } catch(e){
      accounts = [];
      $('tbody').innerHTML = '';
      $('stats').innerHTML = '';
      $('listMsg').style.display = 'block';
      $('listMsg').textContent = String(e.message || e);
    }
  }

  async function act(kind, name){
    if (kind === 'probe') {
      probes[name] = { ok:false, error:'检测中…' };
      render();
      try { probes[name] = await api('/plugins/' + PLUGIN + '/api/probe', {method:'POST', body: JSON.stringify({name:name})}); }
      catch(e){ probes[name] = { ok:false, error:String(e.message||e) }; }
      render();
      return;
    }
    // 宿主的 /auth-files/status 是启停的权威来源（运行时认证状态）。
    // 插件自有路由只写 StorageJSON，宿主不可用时作为兜底。
    if (kind === 'delete') {
      // 删除前先禁用：让该 auth 退出请求池，避免删除后下一次请求又把文件重建回来。
      try { await api('/auth-files/status', {method:'PATCH', body: JSON.stringify({name:name, disabled:true})}); }
      catch(e){ /* 禁用失败不阻塞删除，继续尝试 */ }
      try {
        await api('/auth-files?name=' + encodeURIComponent(name), {method:'DELETE'});
      } catch(e){
        // 宿主删除不可用（如 409 插件虚拟认证）：退回仅禁用。
        await api('/plugins/' + PLUGIN + '/api/delete', {method:'POST', body: JSON.stringify({name:name})});
        throw new Error('文件删除失败（已退回禁用）：' + (e.message || e));
      }
      return;
    }
    var disabled = (kind !== 'enable');
    try {
      await api('/auth-files/status', {method:'PATCH', body: JSON.stringify({name:name, disabled:disabled})});
    } catch(e){
      await api('/plugins/' + PLUGIN + (disabled ? '/api/delete' : '/api/enable'),
                {method:'POST', body: JSON.stringify({name:name})});
    }
  }

  $('tbody').addEventListener('click', async function(ev){
    var btn = ev.target.closest('button[data-act]');
    if (btn) {
      var kind = btn.dataset.act, name = btn.dataset.name;
      if (kind === 'delete') {
        if (!confirm('确认删除 ' + name + ' ？\n将调用宿主删除接口删除该认证文件，且不再自动恢复。')) return;
      } else if (kind === 'disable' && !confirm('确认禁用 ' + name + ' ?')) return;
      btn.disabled = true;
      try { await act(kind, name); if (kind !== 'probe') await loadList(); }
      catch(e){ alert(e.message || e); }
      finally { btn.disabled = false; }
    }
  });

  $('tbody').addEventListener('change', function(ev){
    var cb = ev.target.closest('input[data-sel]');
    if (!cb) return;
    selected[cb.dataset.sel] = cb.checked;
    renderBulk();
  });

  $('selAll').onchange = function(){
    var on = $('selAll').checked;
    visible().forEach(function(a){ selected[a.name] = on; });
    render();
  };

  async function bulk(kind){
    var names = Object.keys(selected).filter(function(n){ return selected[n]; });
    if (!names.length) return;
    var verb = kind === 'enable' ? '启用' : (kind === 'disable' ? '禁用' : (kind === 'delete' ? '删除' : '检测'));
    if (kind !== 'probe' && !confirm('确认' + verb + ' ' + names.length + ' 个账号？' + (kind === 'delete' ? '\n删除不可恢复。' : ''))) return;
    for (var i=0;i<names.length;i++){
      try { await act(kind, names[i]); } catch(e){ /* 单个失败不中断批量 */ }
    }
    if (kind !== 'probe') { selected = {}; await loadList(); }
  }

  $('bulkEnable').onclick  = function(){ bulk('enable'); };
  $('bulkDisable').onclick = function(){ bulk('disable'); };
  $('bulkDelete').onclick  = function(){ bulk('delete'); };
  $('bulkProbe').onclick   = function(){ bulk('probe'); };
  $('bulkClear').onclick   = function(){ selected = {}; $('selAll').checked = false; render(); };

  document.querySelectorAll('th.sortable').forEach(function(th){
    th.onclick = function(){
      var k = th.dataset.sort;
      if (sortKey === k) sortDir = -sortDir; else { sortKey = k; sortDir = 1; }
      render();
    };
  });

  $('search').oninput = render;
  $('statusFilter').onchange = render;
  $('reloadBtn').onclick = loadList;
  $('clearBtn').onclick = function(){ $('importText').value = ''; };

  $('mgmtKey').value = localStorage.getItem(KEY_STORE) || '';
  $('saveKey').onclick = function(){
    localStorage.setItem(KEY_STORE, $('mgmtKey').value.trim());
    var m = $('importMsg');
    m.textContent = '密钥已保存';
    m.className = 'msg ok';
    loadList();
  };

  $('importBtn').onclick = async function(){
    var msg = $('importMsg'), errBox = $('importErrs');
    msg.className = 'msg';
    msg.textContent = '导入中…';
    errBox.style.display = 'none';
    try {
      var text = $('importText').value;
      var data = await api('/plugins/' + PLUGIN + '/api/import', {method:'POST', body: JSON.stringify({text:text})});
      msg.textContent = '成功 ' + (data.added||0) + ' · 跳过 ' + (data.skipped||0) + ' · 失败 ' + (data.failed||0)
        + '（共 ' + (data.total||0) + ' 行）';
      msg.className = 'msg ' + ((data.failed || !data.ok) ? 'bad' : 'ok');
      if (Array.isArray(data.errors) && data.errors.length) {
        errBox.innerHTML = data.errors.map(function(e){ return '<div>' + esc(e) + '</div>'; }).join('');
        errBox.style.display = 'block';
      }
      if (data.added) $('importText').value = '';
      await loadList();
    } catch(e){
      msg.textContent = String(e.message || e);
      msg.className = 'msg bad';
    }
  };

  loadList();
})();
</script>
</body>
</html>`
}
