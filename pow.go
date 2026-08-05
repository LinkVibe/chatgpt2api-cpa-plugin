package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/sha3"
	"math/rand"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultPOWScript = "https://chatgpt.com/backend-api/sentinel/sdk.js"

var (
	cores             = []int{8, 16, 24, 32}
	documentKeys      = []string{"__reactContainer$fzelfjyxej8", "_reactListening5dehydibo78", "location"}
	screenResolutions = [][2]int{{1920, 1080}, {1440, 900}, {2560, 1440}, {3840, 2160}}
	scriptSrcRe       = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+)["']`)
	dataBuildRe       = regexp.MustCompile(`c/[^/]*/_`)
	htmlBuildRe       = regexp.MustCompile(`(?i)<html[^>]*data-build=["']([^"']*)["']`)
)

func parsePOWResources(html []byte) (sources []string, dataBuild string) {
	matches := scriptSrcRe.FindAllSubmatch(html, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		src := string(m[1])
		sources = append(sources, src)
		if b := dataBuildRe.FindString(src); b != "" {
			dataBuild = b
		}
	}
	if len(sources) == 0 {
		sources = []string{defaultPOWScript}
	}
	if dataBuild == "" {
		if m := htmlBuildRe.FindSubmatch(html); len(m) == 2 {
			dataBuild = string(m[1])
		}
	}
	return sources, dataBuild
}

// POW script sources are parsed out of the public chatgpt.com homepage and are
// identical for every account — they are not token-scoped. Caching them globally
// removes one full homepage GET (and a ~1MB copy of the HTML) per request.
var powResCache struct {
	sync.RWMutex
	sources []string
	build   string
	at      time.Time
}

const powResourceTTL = 10 * time.Minute

// cachedPOWResources returns the shared resources when still fresh. The returned
// slice is treated as immutable by callers and is never mutated after storing.
func cachedPOWResources() (sources []string, build string, ok bool) {
	powResCache.RLock()
	defer powResCache.RUnlock()
	if len(powResCache.sources) == 0 || time.Since(powResCache.at) > powResourceTTL {
		return nil, "", false
	}
	return powResCache.sources, powResCache.build, true
}

func storePOWResources(sources []string, build string) {
	powResCache.Lock()
	defer powResCache.Unlock()
	powResCache.sources = sources
	powResCache.build = build
	powResCache.at = time.Now()
}

func legacyParseTime() string {
	loc := time.FixedZone("EST", -5*3600)
	now := time.Now().In(loc)
	return now.Format("Mon Jan 02 2006 15:04:05") + " GMT-0500 (Eastern Standard Time)"
}

func buildPOWConfig(userAgent string, scriptSources []string, dataBuild string) []any {
	navigatorKeys := []string{
		"registerProtocolHandler鈭抐unction registerProtocolHandler() { [native code] }",
		"storage鈭抂object StorageManager]",
		"locks鈭抂object LockManager]",
		"appCodeName鈭扢ozilla",
		"permissions鈭抂object Permissions]",
		"webdriver鈭抐alse",
		"vendor鈭扜oogle Inc.",
		"cookieEnabled鈭抰rue",
		"product鈭扜ecko",
		"onLine鈭抰rue",
		"language鈭抸h-CN",
		"hardwareConcurrency鈭?2",
	}
	windowKeys := []string{
		"0", "window", "self", "document", "location", "history", "navigator",
		"chrome", "performance", "crypto", "localStorage", "sessionStorage",
		"fetch", "setTimeout", "__NEXT_DATA__",
	}
	scriptSource := defaultPOWScript
	if len(scriptSources) > 0 {
		scriptSource = scriptSources[rand.Intn(len(scriptSources))]
	}
	res := screenResolutions[rand.Intn(len(screenResolutions))]
	perf := float64(time.Now().UnixNano()%1_000_000_000) / 1e6
	return []any{
		res[0] + res[1],
		legacyParseTime(),
		4294705152,
		1,
		userAgent,
		scriptSource,
		dataBuild,
		"en-US",
		"en-US,es-US,en,es",
		rand.Float64(),
		navigatorKeys[rand.Intn(len(navigatorKeys))],
		documentKeys[rand.Intn(len(documentKeys))],
		windowKeys[rand.Intn(len(windowKeys))],
		perf,
		uuid.NewString(),
		"",
		cores[rand.Intn(len(cores))],
		float64(time.Now().UnixMilli()) - perf,
		0, 0, 0, 0, 0, 0,
		0,
	}
}

func buildLegacyRequirementsToken(userAgent string, scriptSources []string, dataBuild string) string {
	cfg := buildPOWConfig(userAgent, scriptSources, dataBuild)
	raw, _ := json.Marshal(cfg)
	return "gAAAAAC" + base64.StdEncoding.EncodeToString(raw)
}

func buildProofToken(seed, difficulty, userAgent string, scriptSources []string, dataBuild string) (string, error) {
	cfg := buildPOWConfig(userAgent, scriptSources, dataBuild)
	answer, solved := powGenerate(seed, difficulty, cfg, 500000)
	if !solved {
		return "", fmt.Errorf("failed to solve proof token: difficulty=%s", difficulty)
	}
	return "gAAAAAB" + answer, nil
}

func powGenerate(seed, difficulty string, config []any, limit int) (string, bool) {
	target, err := hex.DecodeString(difficulty)
	if err != nil {
		return "", false
	}
	diffLen := len(target)
	seedBytes := []byte(seed)

	prefixRaw, _ := json.Marshal(config[:3])
	// config[:3] JSON is like [a,b,c] -> drop trailing ]
	static1 := append(prefixRaw[:len(prefixRaw)-1], ',')

	midRaw, _ := json.Marshal(config[4:9])
	// [x,y,...] -> ,x,y,...
	static2 := append([]byte{','}, midRaw[1:len(midRaw)-1]...)
	static2 = append(static2, ',')

	tailRaw, _ := json.Marshal(config[10:])
	static3 := append([]byte{','}, tailRaw[1:]...)

	for i := 0; i < limit; i++ {
		finalJSON := make([]byte, 0, len(static1)+len(static2)+len(static3)+24)
		finalJSON = append(finalJSON, static1...)
		finalJSON = append(finalJSON, []byte(strconv.Itoa(i))...)
		finalJSON = append(finalJSON, static2...)
		finalJSON = append(finalJSON, []byte(strconv.Itoa(i>>1))...)
		finalJSON = append(finalJSON, static3...)
		encoded := base64.StdEncoding.EncodeToString(finalJSON)
		h := sha3.New512()
		h.Write(seedBytes)
		h.Write([]byte(encoded))
		digest := h.Sum(nil)
		if bytesLE(digest[:diffLen], target) {
			return encoded, true
		}
	}
	fallback := "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + base64.StdEncoding.EncodeToString([]byte(`"`+seed+`"`))
	return fallback, false
}

func bytesLE(a, b []byte) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return len(a) <= len(b)
}

// Login-flow sentinel (password_verify style) using FNV.
type sentinelLoginGen struct {
	deviceID  string
	userAgent string
	sid       string
}

func newSentinelLoginGen(deviceID, ua string) *sentinelLoginGen {
	return &sentinelLoginGen{deviceID: deviceID, userAgent: ua, sid: uuid.NewString()}
}

func fnv1a32(text string) string {
	h := uint32(2166136261)
	for i := 0; i < len(text); i++ {
		h ^= uint32(text[i])
		h = (h * 16777619) & 0xFFFFFFFF
	}
	h ^= h >> 16
	h = (h * 2246822507) & 0xFFFFFFFF
	h ^= h >> 13
	h = (h * 3266489909) & 0xFFFFFFFF
	h ^= h >> 16
	return fmt.Sprintf("%08x", h&0xFFFFFFFF)
}

func (g *sentinelLoginGen) getConfig() []any {
	perf := 1000 + rand.Float64()*49000
	nav := []string{"vendorSub-undefined", "plugins-undefined", "mimeTypes-undefined", "hardwareConcurrency-undefined"}
	doc := []string{"location", "implementation", "URL", "documentURI", "compatMode"}
	win := []string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}
	coresLocal := []int{4, 8, 12, 16}
	return []any{
		"1920x1080",
		time.Now().UTC().Format("Mon Jan 02 2006 15:04:05") + " GMT+0000 (Coordinated Universal Time)",
		4294705152,
		rand.Float64(),
		g.userAgent,
		"https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js",
		nil,
		nil,
		"en-US",
		rand.Float64(),
		nav[rand.Intn(len(nav))],
		doc[rand.Intn(len(doc))],
		win[rand.Intn(len(win))],
		perf,
		g.sid,
		"",
		coresLocal[rand.Intn(len(coresLocal))],
		float64(time.Now().UnixMilli()) - perf,
	}
}

func (g *sentinelLoginGen) b64(data any) string {
	raw, _ := json.Marshal(data)
	return base64.StdEncoding.EncodeToString(raw)
}

func (g *sentinelLoginGen) generateRequirementsToken() string {
	data := g.getConfig()
	data[3] = 1
	data[9] = int(5 + rand.Float64()*45)
	return "gAAAAAC" + g.b64(data)
}

func (g *sentinelLoginGen) generateToken(seed, difficulty string) string {
	start := time.Now()
	data := g.getConfig()
	if difficulty == "" {
		difficulty = "0"
	}
	for i := 0; i < 500000; i++ {
		data[3] = i
		data[9] = int(time.Since(start).Milliseconds())
		payload := g.b64(data)
		h := fnv1a32(seed + payload)
		prefix := h
		if len(difficulty) > 0 && len(prefix) > len(difficulty) {
			prefix = prefix[:len(difficulty)]
		}
		if prefix <= difficulty {
			return "gAAAAAB" + payload + "~S"
		}
		if i%4096 == 4095 {
			// Yield periodically: the solve loop is CPU-bound and can run for
			// many iterations, so hand the core back so other goroutines (host
			// RPC, other streams) keep making progress.
			runtime.Gosched()
		}
	}
	return "gAAAAAB" + "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + g.b64(nil)
}
