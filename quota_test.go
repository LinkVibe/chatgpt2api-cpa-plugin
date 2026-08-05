package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuotaInfoRoundTrip(t *testing.T) {
	qi := quotaInfo{Plan: "Plus", Quota: 3, Known: true, Status: "正常", RestoreAt: "2026-01-01T00:00:00Z", LastSyncAt: "2026-01-01T00:00:00Z"}
	storage := map[string]any{"type": providerID, "access_token": "eyJabc", quotaInfoKey: qi}
	raw := mustJSON(storage)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	got := quotaInfoFromMap(m)
	if got.Plan != "Plus" || got.Quota != 3 || !got.Known || got.Status != "正常" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestQuotaInfoMissing(t *testing.T) {
	var m map[string]any
	got := quotaInfoFromMap(m)
	if got.Known {
		t.Fatal("missing quota_info must not be known")
	}
	if st := got.normalizedStatus(); st != "未知" {
		t.Fatalf("expected 未知, got %s", st)
	}
}

func TestQuotaStatusTransitions(t *testing.T) {
	cases := []struct {
		known bool
		quota int
		want  string
	}{
		{false, 0, "未知"},
		{true, 5, "正常"},
		{true, 0, "限流"},
		{true, -1, "限流"},
	}
	for _, tc := range cases {
		qi := quotaInfo{Known: tc.known, Quota: tc.quota}
		if got := qi.normalizedStatus(); got != tc.want {
			t.Fatalf("known=%v quota=%d got %q want %q", tc.known, tc.quota, got, tc.want)
		}
	}
}

func TestQuotaInfoFromJWTSetsPlan(t *testing.T) {
	// "eyJ..." with a chatgpt_plan_type claim in the payload.
	qi := quotaInfoFromJWT("")
	if qi.Known {
		t.Fatal("empty token must stay unknown")
	}
}

func TestFetchQuotaInfoEndpoints(t *testing.T) {
	var initMethod string
	var initBodyOK, planEndpoint bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/conversation/init":
			initMethod = r.Method
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("conversation/init body not JSON: %v", err)
			}
			if off, _ := body["timezone_offset_min"].(float64); off == -480 {
				initBodyOK = true
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"default_model_slug":"gpt-5","limits_progress":[` +
				`{"feature_name":"image_gen","remaining":6,"reset_after":"2026-01-01T00:00:00Z"},` +
				`{"feature_name":"text","remaining":0}]}`))
		case "/backend-api/accounts/check/v4-2023-04-27":
			planEndpoint = true
			_, _ = w.Write([]byte(`{"accounts":{"default":{"account":{"plan_type":"Plus"}}}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	old := apiBaseURL
	apiBaseURL = ts.URL
	defer func() { apiBaseURL = old }()

	c := &chatgptClient{accessToken: "eyJtest", userAgent: defaultUA, deviceID: "dev", sessionID: "sess"}
	qi, err := c.fetchQuotaInfo()
	if err != nil {
		t.Fatalf("fetchQuotaInfo: %v", err)
	}
	if initMethod != "POST" {
		t.Fatalf("conversation/init method = %s, want POST", initMethod)
	}
	if !initBodyOK {
		t.Fatal("conversation/init body must carry timezone_offset_min=-480")
	}
	if !planEndpoint {
		t.Fatal("expected a GET to /backend-api/accounts/check/v4-2023-04-27")
	}
	if qi.Quota != 6 {
		t.Fatalf("quota = %d, want 6", qi.Quota)
	}
	if !qi.Known {
		t.Fatal("known quota expected")
	}
	if qi.Plan != "Plus" {
		t.Fatalf("plan = %q, want Plus", qi.Plan)
	}
	if qi.RestoreAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("restore_at = %q, want 2026-01-01T00:00:00Z", qi.RestoreAt)
	}
}
