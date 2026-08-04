# CPA 插件商店发布

依据 [官方格式](https://help.router-for.me/cn/plugin/development.html#%E6%8F%92%E4%BB%B6%E5%95%86%E5%BA%97%E5%8F%91%E5%B8%83%E6%A0%BC%E5%BC%8F)。

## 独立仓发布（本仓库）

仓库：`https://github.com/LinkVibe/chatgpt2api-cpa-plugin`

1. 确认 `registry.json` 的 `repository` 指向本仓  
2. 打 tag 触发 `.github/workflows/release.yml`：

```bash
git tag v0.2.0
git push origin v0.2.0
```

或 Actions → Run workflow → `version=0.2.0`。

Release 资产：

```text
chatgpt2api-cpa-plugin_0.2.0_linux_amd64.zip
chatgpt2api-cpa-plugin_0.2.0_linux_arm64.zip
chatgpt2api-cpa-plugin_0.2.0_windows_amd64.zip
chatgpt2api-cpa-plugin_0.2.0_darwin_amd64.zip
chatgpt2api-cpa-plugin_0.2.0_darwin_arm64.zip
checksums.txt
```

zip 根目录只有：`chatgpt2api-cpa-plugin.{so|dll|dylib}`

3. 商店登记：

- PR 到 [CLIProxyAPI-Plugins-Store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store)（用 `registry.entry.json`）
- 或 CPA 自建源：

```yaml
plugins:
  store-sources:
    - "https://raw.githubusercontent.com/LinkVibe/chatgpt2api-cpa-plugin/main/store/registry.json"
```

## 自检

- [ ] id = 动态库文件名 = `chatgpt2api-cpa-plugin`
- [ ] zip 无子目录
- [ ] checksums 与 zip 同 release
- [ ] latest release 含上述资产
- [ ] tag 去 `v` 后与 zip 内 version 一致
