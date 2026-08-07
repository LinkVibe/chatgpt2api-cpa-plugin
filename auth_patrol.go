package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

var (
	patrolOnce sync.Once
	patrolStop = make(chan struct{})
)

// startTokenPatrol starts a background goroutine that periodically probes each
// plugin-managed access token. If the probe returns a token-invalid error, the
// auth file is rewritten with disabled=true so the host's file watcher will
// disable the account without needing host-side changes.
func startTokenPatrol() {
	patrolOnce.Do(func() {
		go tokenPatrolLoop()
	})
}

func tokenPatrolLoop() {
	// Wait briefly for the host and config to settle before the first probe.
	time.Sleep(5 * time.Second)
	for {
		if hostPresent.Load() {
			tokenPatrolOnce()
		}
		interval := time.Duration(currentRuntimeConfig().TokenPatrolIntervalSecs) * time.Second
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		select {
		case <-time.After(interval):
		case <-patrolStop:
			return
		}
	}
}

func tokenPatrolOnce() {
	if !currentRuntimeConfig().DisableInvalidToken {
		return
	}
	records, err := listAccountRecords()
	if err != nil {
		hostLog("warn", "token patrol list failed: "+err.Error())
		return
	}
	for _, rec := range records {
		if rec.token == "" || rec.view.Disabled {
			continue
		}
		// skip non-plugin providers unless the auth carries our token
		if rec.view.Provider != "" && rec.view.Provider != providerID && !strings.Contains(rec.view.Provider, "chatgpt") {
			continue
		}
		var storageMap map[string]any
		_ = json.Unmarshal(rec.storage, &storageMap)
		client := newChatGPTClientFromStorage(rec.token, storageMap, rec.view.Name)
		_, err := client.patrolProbe()
		if err != nil {
			// http wrapper already invokes maybeDisableToken for invalid-token errors.
			hostLog("debug", "token patrol probe failed: "+err.Error())
		}
	}
}

func (c *chatgptClient) patrolProbe() ([]byte, error) {
	if err := c.bootstrap(); err != nil {
		return nil, err
	}
	path := "/backend-api/models?history_and_training_disabled=false"
	route := "/backend-api/models"
	if c.accessToken == "" {
		path = "/backend-anon/models?iim=false&is_gizmo=false"
		route = "/backend-anon/models"
	}
	_, _, body, err := c.http("GET", baseURL+path, c.baseHeaders(route, map[string]string{
		"Accept": "application/json",
	}), nil, 30)
	return body, err
}
