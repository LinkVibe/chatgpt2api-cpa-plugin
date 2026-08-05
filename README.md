# chatgpt2api-cpa-plugin

**English** · [简体中文](./README.zh-CN.md)

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that talks directly to `chatgpt.com` using a ChatGPT **access_token**.

- Repository: [`LinkVibe/chatgpt2api-cpa-plugin`](https://github.com/LinkVibe/chatgpt2api-cpa-plugin)
- Plugin ID: `chatgpt2api-cpa-plugin`

## Features

- **Stable device/session id** — derived from the access_token hash, persisted in the auth JSON.
- **Authorization + OAI-\* headers** on every upstream request.
- **Cookie jar** — `Set-Cookie` from bootstrap/sentinel is replayed as a `Cookie` header on subsequent requests.
- **Native model slugs** (`gpt-image-2` / `gpt-5` / `auto`); optional plugin-level `model_prefix` namespaces the registered ids (e.g. `web-gpt-5`) and is stripped before the upstream call. Model list is fetched from `GET /backend-api/models` with a `gpt-image-2` / `auto` fallback.
- **Sentinel flow** — `prepare` + PoW + Turnstile + `finalize`.
- **Image generation** — text-to-image and reference-image edit; poll / settle / timeout with `/backend-api/tasks` error detection; multi-image edit via upload → `multimodal_text` + `picture_v2`.
- **Text chat** — conversation SSE streaming.
- **Auto-disable** auth file when the access_token is invalidated.
- **Quota / plan** — the panel shows each account's subscription plan and remaining image quota (synced from upstream `conversation/init` + `accounts/check`); a successful image generation decrements the local quota (display only, never blocks requests).
- **Management panel** — list, per-line token import, disable, delete, and a settings modal (model prefix, default model, image polling, auto-disable). Saving applies immediately to the running process and persists to the CPA config.

## Directory

```text
.
├── main.go / host.go / client.go / management.go
├── pow.go / turnstile.go / envelope.go
├── go.mod
├── store/registry.json
└── .github/workflows/release.yml
```

## Local Build

Requires CGO.

```bash
go mod tidy

# Windows
set CGO_ENABLED=1
go build -buildmode=c-shared -o chatgpt2api-cpa-plugin.dll .

# Linux
CGO_ENABLED=1 go build -buildmode=c-shared -o chatgpt2api-cpa-plugin.so .

# macOS
CGO_ENABLED=1 go build -buildmode=c-shared -o chatgpt2api-cpa-plugin.dylib .
```

Install into CPA:

```text
plugins/<goos>/<goarch>/chatgpt2api-cpa-plugin.{dll|so|dylib}
```

## CPA Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    chatgpt2api-cpa-plugin:
      enabled: true
      priority: 20
      # optional:
      # model_prefix: "web-"
      # default_model: "gpt-image-2"
      # image_poll_timeout_secs: 180
      # image_poll_interval_secs: 5
      # image_initial_wait_secs: 8
      # image_settle_enabled: true
      # image_settle_wait_secs: 2
      # disable_invalid_token: true
  # optional: self-hosted store source
  store-sources:
    - "https://raw.githubusercontent.com/LinkVibe/chatgpt2api-cpa-plugin/main/store/registry.json"
```

## Management Panel & API

- Web UI: `/v0/resource/plugins/chatgpt2api-cpa-plugin/accounts`
- List: `GET /v0/management/plugins/chatgpt2api-cpa-plugin/api/list`
- Import: `POST .../api/import` `{"text":"eyJ...\nemail----eyJ..."}`
- Probe: `POST .../api/probe` `{"name":"...json"}`
- Disable / Enable: `POST .../api/delete` / `.../api/enable` `{"name":"...json"}`
- Config: `GET/POST .../api/config`
- Delete: the panel "delete" button first disables via host `PATCH /v0/management/auth-files/status` (so the auth leaves the request pool before the file is removed), then deletes via `DELETE /v0/management/auth-files?name=<file>`.

## Credential Example

```json
{
  "type": "chatgpt2api-cpa-plugin",
  "email": "user@example.com",
  "access_token": "eyJhbGciOi..."
}
```

## Notes

- Arkose is not implemented.
- Images: the file id is obtained from SSE / conversation and then downloaded.
