# chatgpt2api-cpa-plugin

CLIProxyAPI 独立插件仓库：用 ChatGPT **access_token** 直连 `chatgpt.com`。

- 作者 / 仓库：[`LinkVibe/chatgpt2api-cpa-plugin`](https://github.com/LinkVibe/chatgpt2api-cpa-plugin)
- 插件 ID：`chatgpt2api-cpa-plugin`
- 版本：`0.3.6`

## v0.3.6（设置窗口 + 额度套餐 + CPU 修复）

- **设置窗口**：面板新增"插件设置"卡片，可视化配置 `model_prefix` / `default_model` / 图片轮询 / 自动禁用；保存立即生效（当前进程），并生成可直接粘贴到 CPA `config.json` 的 YAML 片段用于持久化（宿主 RPC 无 `host.config`，持久化走 CPA 原生 ConfigYAML）
- **可配置的插件级模型前缀**：`model_prefix` 留空 = 原生 slug（`gpt-image-2` / `gpt-5` / `auto`）；填 `web-` 则注册 `web-gpt-*`，请求时自动剥掉再调上游；`validateModel` / `isImageModel` 前缀感知，仍拦截 codex
- **额度 / 套餐**（对齐 chatgpt2api 语义）：`quota_info` 存于每个 auth 的 `StorageJSON`；检测时从上游 `GET /backend-api/conversation/init` 同步 `image_gen` 剩余额度 / `reset_after` / `plan_type`，图片生成成功本地扣 1（仅展示，不拦截请求）；面板新增"额度 / 恢复时间"列 + "额度用尽 / 额度未知"筛选
- **CPU 修复**：`host.http.stream_read` 空读加 100ms backoff（宿主无数据立即返回时不再忙轮询）；PoW 求解循环每 4096 次 `runtime.Gosched()`，避免独占核心
- `go test` 全绿

## v0.3.5（独立删除 + 原生模型名）

- **管理面板"删除"**：先调用宿主 `PATCH /v0/management/auth-files/status` 禁用（退出请求池，防止删除后下一次请求又重建文件），再调用 `DELETE /v0/management/auth-files?name=<文件>` 物理删除；宿主删除不可用时退回仅禁用。支持单行 + 批量
- **去掉 `web-` 前缀**：模型直接使用原生 slug（`gpt-image-2` / `gpt-5` / `auto`），注册名 = 上游名，走通 `/v1/images/*`
- 文本路径 `gpt-image-2` 上游映射为 `auto`（picture_v2）；图片路径恒走 `auto`
- `auth_index` 读取优先级修正（面板检测 / 自动禁用不再拿 `id` 当 `auth_index`）
- `go test` 全绿

## v0.3.4（Web 图片模型注册重构）

- 模型直接使用原生 slug（`gpt-image-2` / `gpt-5` / `auto`）：**注册名 = 上游名**，以原生 slug 走通 `/v1/images/*`，无需改 CLIProxyAPI
- 图片模型加 `Type: "openai-image"`，使主程序 `isOpenAICompatImagesModel` 白名单直接放行
- 文本路径 `gpt-image-2` 上游映射为 `auto`（picture_v2）；图片路径恒走 `auto`
- 移除官方 `auth.Prefix` 依赖，不在导入时写入 prefix 字段，模型名唯一、无双份注册
- 管理面板"检测"：修复 `no access_token in auth file` 的读取路径
- `go test` 全绿

## v0.3.3（审计修复）

- host.http 失败时**不再静默直连**（避免绕过 CPA 代理）
- token 失效判定收紧；disable 尽量写回**真实 auth 文件名**
- 统一 `buildAuthStorage` / `decodeExecutorRequest`
- Cookie 单路径解析 + 读写锁；流式请求也会 ingest Set-Cookie
- 基础单测：`go test`

## v0.3.0 能力

1. **稳定 device/session id**（按 access_token 哈希，写入 auth JSON）
2. 每次请求 **Authorization + OAI-\*** 头
3. **Cookie 罐**：bootstrap/sentinel 的 `Set-Cookie` → 后续请求 `Cookie` 头
4. 模型默认原生 slug（`gpt-image-2` / `gpt-5` / `auto`），可配 `model_prefix` 加前缀；列表优先从官网 `GET /backend-api/models` 拉取（失败则 fallback `gpt-image-2` / `auto`）
5. 图片 **poll / settle / timeout** + `/backend-api/tasks` 错误检测
6. **access_token 失效自动 disabled**
7. **多图编辑**：参考图 upload → multimodal_text + picture_v2

## 目录

```text
.
├── main.go / host.go / client.go / management.go
├── pow.go / turnstile.go / envelope.go
├── go.mod
├── store/registry.json
└── .github/workflows/release.yml   # 多平台编译 + 自动 Release
```

## 能力

- Sentinel：`prepare` + PoW + Turnstile + `finalize`
- 图片：文生图 / 参考图编辑
- 文本：conversation SSE
- 凭证：`access_token`；导入时 JWT 取 email，否则 `chatgpt2api-<id>`
- 管理面板：列表、每行 token 导入、禁用、删除、**设置窗口**（模型前缀 / 默认模型 / 图片轮询 / 自动禁用，含 config.json YAML 片段）
- **额度 / 套餐**：面板展示每账号的套餐（JWT 或上游 `conversation/init` 的 `plan_type`）、图片额度（上游 `limits_progress[image_gen].remaining`）、状态（正常 / 限流 / 未知）与恢复时间；图片生成成功本地扣 1 额度并持久化到 `StorageJSON`（`quota_info`）

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

## 发布（自动 Release）

```bash
git tag v0.2.0
git push origin v0.2.0
```

或 Actions → Run workflow → `version=0.2.0`。

产物：5 平台 zip + `checksums.txt`（CPA 商店格式）。详见 [store/README.md](./store/README.md)。

## CPA 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    chatgpt2api-cpa-plugin:
      enabled: true
      priority: 20
  # 可选：自建商店源
  store-sources:
    - "https://raw.githubusercontent.com/LinkVibe/chatgpt2api-cpa-plugin/main/store/registry.json"
```

## 控制面板

- 网页：`/v0/resource/plugins/chatgpt2api-cpa-plugin/accounts`
- 列表：`GET /v0/management/plugins/chatgpt2api-cpa-plugin/api/list`
- 导入：`POST .../api/import` `{"text":"eyJ...\\nemail----eyJ..."}`
- 禁用（兜底，宿主不可用时）：`POST .../api/delete` `{"name":"...json"}`
- 删除：面板"删除"按钮会先调用宿主 `PATCH /v0/management/auth-files/status` 禁用（防止删除后请求又把文件重建），再调用 `DELETE /v0/management/auth-files?name=<文件>` 物理删除。

## 凭证示例

```json
{
  "type": "chatgpt2api-cpa-plugin",
  "email": "user@example.com",
  "access_token": "eyJhbGciOi..."
}
```

## 说明

- Arkose 未实现
- 图片从 SSE / conversation 取 file id 再下载
