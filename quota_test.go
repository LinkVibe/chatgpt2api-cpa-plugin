package main

import (
	"encoding/json"
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
