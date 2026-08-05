# chatgpt2api-cpa-plugin

[English](./README.md) · **简体中文**

一个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件，用 ChatGPT **access_token** 直连 `chatgpt.com`。

- 仓库：[`LinkVibe/chatgpt2api-cpa-plugin`](https://github.com/LinkVibe/chatgpt2api-cpa-plugin)
- 插件 ID：`chatgpt2api-cpa-plugin`

## 能力

- **稳定的 device/session id**——按 access_token 哈希生成，写入 auth JSON。
- 每次请求都带 **Authorization + OAI-\*** 头。
- **Cookie 罐**——bootstrap/sentinel 的 `Set-Cookie` 会在后续请求里作为 `Cookie` 头回放。
- **原生模型 slug**（`gpt-image-2` / `gpt-5` / `auto`）；可选的插件级 `model_prefix` 给注册 id 加前缀（如 `web-gpt-5`），请求时自动剥掉再调上游。模型列表优先从 `GET /backend-api/models` 拉取，失败回退 `gpt-image-2` / `auto`。
- **Sentinel 流程**——`prepare` + PoW + Turnstile + `finalize`。
- **图片生成**——文生图与参考图编辑；poll / settle / timeout + `/backend-api/tasks` 错误检测；多图编辑走 upload → `multimodal_text` + `picture_v2`。
- **文本对话**——conversation SSE 流式。
- **access_token 失效自动禁用** auth 文件。
- **额度 / 套餐**——面板展示每个账号的订阅档位与图片剩余额度（从上游 `conversation/init` + `accounts/check` 同步）；图片生成成功本地扣 1 额度（仅展示，不拦截请求）。
- **管理面板**——列表、每行 token 导入、禁用、删除，以及设置弹窗（模型前缀 / 默认模型 / 图片轮询 / 自动禁用）。保存立即在当前进程生效，并写入 CPA 配置持久化。

## 目录

```text
.
├── main.go / host.go / client.go / management.go
├── pow.go / turnstile.go / envelope.go
├── go.mod
├── store/registry.json
└── .github/workflows/release.yml
```

## 本地构建

需要 CGO。

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

安装到 CPA：

```text
plugins/<goos>/<goarch>/chatgpt2api-cpa-plugin.{dll|so|dylib}
```

## CPA 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    chatgpt2api-cpa-plugin:
      enabled: true
      priority: 20
      # 可选：
      # model_prefix: "web-"
      # default_model: "gpt-image-2"
      # image_poll_timeout_secs: 180
      # image_poll_interval_secs: 5
      # image_initial_wait_secs: 8
      # image_settle_enabled: true
      # image_settle_wait_secs: 2
      # disable_invalid_token: true
  # 可选：自建商店源
  store-sources:
    - "https://raw.githubusercontent.com/LinkVibe/chatgpt2api-cpa-plugin/main/store/registry.json"
```

## 管理面板与 API

- 网页：`/v0/resource/plugins/chatgpt2api-cpa-plugin/accounts`
- 列表：`GET /v0/management/plugins/chatgpt2api-cpa-plugin/api/list`
- 导入：`POST .../api/import` `{"text":"eyJ...\nemail----eyJ..."}`
- 检测：`POST .../api/probe` `{"name":"...json"}`
- 禁用 / 启用：`POST .../api/delete` / `.../api/enable` `{"name":"...json"}`
- 配置：`GET/POST .../api/config`
- 删除：面板"删除"按钮会先调用宿主 `PATCH /v0/management/auth-files/status` 禁用（让该 auth 退出请求池，防止删除后下一次请求又把文件重建），再调用 `DELETE /v0/management/auth-files?name=<文件>` 物理删除。

## 凭证示例

```json
{
  "type": "chatgpt2api-cpa-plugin",
  "email": "user@example.com",
  "access_token": "eyJhbGciOi..."
}
```

## 说明

- Arkose 未实现。
- 图片从 SSE / conversation 取 file id 再下载。
