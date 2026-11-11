# 本地改动备忘录（上游同步指南）

> 本文件记录本 fork 相对上游 `looplj/axonhub` 的**有意变更**。
> 每次从上游同步（rebase / merge / 重新 clone）后，请按本文逐项核对并重新施加改动。
>
> 上游仓库：`https://github.com/looplj/axonhub`（默认分支 `unstable`）
> 本 fork：`https://github.com/Misaka009982/axonhub`
> 定制起点：`v1.0.0-beta11`（commit `b8bb3421`）
> 当前同步版本：`v1.0.0-rc.3`（commit `e70fb2a2`），从本地 rc2 提交 `58ae5568` 继续合并

---

## 变更总览

**主题：放开「流式首字超时 / 非流式传输超时」的 600s 上限，并明确每一层的 0 值语义。**

### 问题背景

用户反馈：设置里两项超时都填 `0`（文档说 0 = 关闭），但请求仍在 **600s** 被中断。

上游原有**两个独立的 600s 约束**，只改页面设置不能移除请求总时限。

| 来源 | 位置 | 作用 |
|------|------|------|
| ① 设置上限钳制 | `maxRetryResponseTimeoutSeconds = 600` | 把用户填的值强行截到 600 |
| ② 请求总时限 | `server.llm_request_timeout` 原默认 `600s` | 整个 `/v1/*` 路由的 context 超时 |

来源 ② 触发时日志为 `Stream deadline exceeded`，错误是 `context.DeadlineExceeded`
（见 `internal/server/api/chat.go` 的 `writeSSEStreamEnd`），**不是**首字超时错误。

另外 `context.WithTimeout(ctx, 0)` 会立即超时，所以旧代码里
`llm_request_timeout` 填 `0` = **立刻失败**，无法表示"关闭"。

### 修改后的预期行为

- 两项页面设置填 `0` → 只关闭各自的**单次尝试计时器**，不会关闭服务端总时限。
- 填 `> 600` → 设置值不再被钳制；仍不能超过更早的请求级 deadline。
- 首字计时器在首个流式事件到达后停止，不控制后续生成的总时长。
- `server.llm_request_timeout` 默认 `3600s`，控制整个 LLM 请求（包括流式输出和重试）；显式填 `0` 可禁用本机这一层总时限。
- `request_timeout` 或 `llm_request_timeout` 任一为非正值时，共享 HTTP 服务的 `WriteTimeout` 必须为 `0`，不能让另一类请求的有限写时限重新限制它。
- 配置文件与环境变量优先于源码默认值；旧配置不会因升级而自动改变。客户端取消、父 context、上游及反代的限制仍然有效。

### 后续反馈与本次修复边界

用户反馈首字耗时 `15.10s`、总耗时约 `600s`，并说明从 `main` 的 Dockerfile 构建且没有显式配置总时限。仅凭 `context deadline exceeded` 不能确认是哪一层超时。`bf8dd5f3` 的源码默认值已经是 `3600s`，也已删除页面 `(0-600s)` 文案；现场仍显示旧文案与源码不一致，需要核对实际运行产物和日志，不能直接归因于旧配置。

已确认并修复的代码遗漏是：原先 `max(request_timeout, llm_request_timeout)` 会把 `0` 错当成普通数值，例如 `max(30s, 0)` 得到 `30s`，导致声明关闭总时限后仍有有限的全局写期限。**这不是已证实的现场 600s 根因**；SSE 写入逻辑会单独刷新和清除写 deadline，本次保持不变。

---

## 需要重新施加的改动

### 1. `internal/server/biz/system.go`

**a) 删除常量**（在文件顶部 `const` 块内）：

```go
maxRetryResponseTimeoutSeconds  = 600
```

删除这一行即可（保留同块的 `maxChannelSettingUpdateAttempts = 5`）。

**b) 删除 `normalizeRetryPolicy` 里的两段上限钳制**：

```go
// 删除这两段
if policy.StreamFirstEventTimeoutSeconds > maxRetryResponseTimeoutSeconds {
    policy.StreamFirstEventTimeoutSeconds = maxRetryResponseTimeoutSeconds
}
...
if policy.NonStreamResponseTimeoutSeconds > maxRetryResponseTimeoutSeconds {
    policy.NonStreamResponseTimeoutSeconds = maxRetryResponseTimeoutSeconds
}
```

保留 `< 0 → 0` 的两段（负值视为关闭）。

**c) 更新 `RetryPolicy` 结构体两处字段注释**：

```go
// Set to 0 to disable. There is no upper bound; the effective ceiling is the
// server-level llm_request_timeout (AXONHUB_SERVER_LLM_REQUEST_TIMEOUT).
```

### 2. `internal/server/middleware/timeout.go`

`WithTimeout` 开头加早退，让 `<= 0` 表示"不设超时"：

```go
func WithTimeout(ts time.Duration) gin.HandlerFunc {
    return func(c *gin.Context) {
        if ts <= 0 {
            c.Next()
            return
        }

        timeoutCause := fmt.Errorf("server request timeout after %s: %w", ts, context.DeadlineExceeded)
        ctx, cancel := context.WithTimeoutCause(c.Request.Context(), ts, timeoutCause)
        // 保留 defer cancel()、请求 context 替换和 c.Next()
    }
}
```

`c.Next()` 返回后，仅当 `context.Cause(ctx) == timeoutCause` 时记录
`Server request timeout exceeded`，包含 `source=server_request_timeout`、路由模板、配置时限和实际耗时。这样不会把父级 deadline 或客户端取消误报为本层超时；不记录请求正文、查询参数或密钥。

### 3. `internal/server/config.go` 与 `internal/server/server.go`

`Config.writeTimeout()` 必须把非正值当作“无时限”，而不是直接取数值最大值：

```go
func (c Config) writeTimeout() time.Duration {
    if c.RequestTimeout <= 0 || c.LLMRequestTimeout <= 0 {
        return 0
    }

    return max(c.RequestTimeout, c.LLMRequestTimeout)
}
```

`Run()` 将 `srv.Config.writeTimeout()` 的返回值用于 `http.Server.WriteTimeout`。
启动时的 `run server` 日志同时输出实际生效的 `request_timeout`、`llm_request_timeout` 和 `write_timeout`，不输出配置全文。

保留路由上的 context 超时及 `api/chat.go` 的 SSE 单次写入保护，不使用 `context.WithoutCancel` 绕过父级 deadline。

### 4. `conf/conf.go`

`setDefaults` 中：

```go
- v.SetDefault("server.llm_request_timeout", "600s")
+ v.SetDefault("server.llm_request_timeout", "3600s")
```

### 5. `config.example.yml`

示例值保持 `3600s`，注释明确它是整次请求总时限、`0` 仅禁用本机这一层，且环境变量优先。调整部署配置后需要重建/重启应用容器，不能只修改页面首字设置。

### 6. `frontend/src/features/system/components/retry-settings.tsx`

删除两个 `Input` 上的 `max='600'`（`stream-first-event-timeout` 与 `non-stream-response-timeout`），
保留 `min='0'`。

### 7. i18n 文案（2 个文件）

`frontend/src/locales/en/system.json` 与 `frontend/src/locales/zh-CN/system.json`：

- `system.retry.streamFirstEventTimeoutSeconds.description`
- `system.retry.nonStreamResponseTimeoutSeconds.description`

文案说明支持超过 `600s`、首个事件到达后首字计时器停止、页面 `0` 只关闭对应计时器，以及独立的 `server.llm_request_timeout`（默认 `3600s`，需在服务端配置中另设 `0` 才能禁用）。
（历史版本可能有 `zh.json`，当前为 `zh-CN` + `en`。）

### 8. 回归覆盖

- `internal/server/config_timeout_test.go`：任一类请求禁用超时时，全局写时限必须为 `0`；两者为正时取最大值，支持超过 `600s`。
- `internal/server/middleware/timeout_test.go`：禁用不创建 deadline，仍传播客户端取消与父级 deadline；超过 `600s` 不钳制；本层超时保留可识别 cause，`ctx.Err()` 仍为 `context.DeadlineExceeded`。
- `conf/conf_test.go` 的 `TestLLMRequestTimeoutConfig`：默认 `3600s`，显式 `600s` 不会被默认值覆盖，环境变量 `0` / `7200s` 可覆盖配置。
- `internal/server/biz/system_test.go` 的 `TestNormalizeRetryPolicy_ResponseTimeouts`：负值归 `0`，`601`、`900`、`7200` 原样保留。

本次只做静态审查，未运行以上测试；上游同步后应由维护者执行。

### 9. `internal/build/VERSION`

从 rc2 起，每次同步正式标签时，将此文件更新为对应的完整版本号；当前为 `v1.0.0-rc.3`。版本拼写以实际标签为准，rc3 的标签在 `rc` 与 `3` 之间带有点号。

`internal/build/info.go` 将该文件嵌入为默认版本，`Dockerfile` 也直接读取它并注入 `build.Version`。因此从 `main` 自行构建时，必须同步更新此文件，避免代码已升级但页面和版本接口仍显示 beta9。发布工作流显式注入版本时，仍以注入值为准。

### 10. `Dockerfile` 的 Go 构建镜像

rc3 标签中的主模块、`cmd/schema` 和 `cmd/axonhub-desktop` 要求 Go `1.27.4`，但构建镜像仍为 `golang:1.27.3-alpine3.24`。现场日志确认编译及校验脚本自测成功，随后真实依赖校验报错：

```text
go: go.mod requires go >= 1.27.4 (running go 1.27.3; GOTOOLCHAIN=local)
```

原因是官方镜像默认 `GOTOOLCHAIN=local`，`GOTOOLCHAIN=auto go build` 只影响该命令，不会传给 `&&` 后的校验脚本；自测使用模拟的 `go`，也不会检查真实工具链版本。

本 fork 采用 rc3 标签发布后合并的上游 [PR #2983](https://github.com/looplj/axonhub/pull/2983) 中的 Dockerfile 修复，将后端镜像更新为 `golang:1.27.4-alpine3.24`，与模块要求对齐，保留所有依赖校验。这里只同步该文件的版本修正。

后续同步时，核对构建镜像的 Go 版本不低于 `scripts/verify-go-sse-dependency.sh` 遍历的各模块要求；不能仅确认 `go build` 带有自动工具链选项。重建后的日志应继续出现四个模块的 `verified module dependency` 和最终的 `verified production binary dependency`。本次仅做静态核对，实际镜像构建由维护者执行。

---

## 同步上游后的快速核对清单

```bash
# 在仓库根目录执行

# 1. 上限钳制是否被上游"复活"
grep -rn "maxRetryResponseTimeoutSeconds" internal/server/biz/system.go

# 2. 首字/非流超时描述里是否又出现 0-600s
grep -rn "0-600s" frontend/src/locales/

# 3. 表单是否又加了 max='600'
grep -rn "max='600'" frontend/src/features/system/components/retry-settings.tsx

# 4. 服务端默认超时
grep -rn "llm_request_timeout" conf/conf.go config.example.yml

# 5. 0 值语义是否被破坏
grep -n "ts <= 0" internal/server/middleware/timeout.go

# 6. 全局写时限是否保留 0 值语义，以及运行值和超时来源日志
grep -n "writeTimeout\|request_timeout" internal/server/config.go internal/server/server.go internal/server/middleware/timeout.go

# 7. 渠道级响应超时是否又被钳到 600
grep -rn "maxRetryResponseTimeoutSeconds" internal/server/biz/channel_response_timeout.go

# 8. 前端渠道超时上限与文案
grep -n "RESPONSE_TIMEOUT_MAX_SECONDS" frontend/src/config/retry-policy.ts
grep -rn "0 到 600\|0 and 600" frontend/src/locales/en/channels.json frontend/src/locales/zh-CN/channels.json

# 9. 版本文件应与本次同步的正式标签一致
cat internal/build/VERSION

# 10. Go 构建镜像是否满足所有被校验模块的版本要求
grep -n '^FROM golang:' Dockerfile
grep -n '^go ' go.mod llm/go.mod cmd/schema/go.mod cmd/axonhub-desktop/go.mod

# 11. 确认改动清单
git status --short
```

第 1～3 项应无匹配；第 4 项应看到 `3600s` 默认值；第 5～6 项应有对应逻辑；第 9 项当前应为 `v1.0.0-rc.3`；第 10 项镜像 Go 版本应满足所有被校验模块的最低要求。不要把“无输出”一律当作失败。

---

## 现场核对与部署设置

1. 查看启动日志 `run server` 的三个超时字段，确认**实际生效值**，而不是只看源码默认值。没有这三个字段说明运行产物未包含本次诊断改动。
2. 同一请求若出现 `Server request timeout exceeded` 且 `source=server_request_timeout`，表示本机路由总时限触发，`timeout` 是配置时限。父级 deadline、上游返回的错误与客户端取消不会被这个标记误认。
3. `Stream deadline exceeded` 表示流式 writer 观察到请求 context 到期；还应结合上面的来源标记判断是哪层。只有 `Error in stream` 或响应内的超时文本时，不能仅凭错误字符串断定是 AxonHub 的总计时器。
4. 页面仍出现 `(0-600s)` 时，核对容器镜像 ID/digest、是否已重建应用容器、前端是否随同构建，以及反代/CDN/浏览器是否仍提供旧资源。仓库的 `Dockerfile` 会构建前端并嵌入后端；`docker-unstable.yml` 固定构建 `unstable`，不是 `main`。
5. 若需要关闭 AxonHub 的 LLM 总时限，在现有应用服务的 `environment` 中设置以下一项（**合并到原配置，不覆盖其他环境变量**），然后由维护者重建应用容器：

```yaml
environment:
  AXONHUB_SERVER_LLM_REQUEST_TIMEOUT: "0"
```

也可以设置 `config.yml` 的 `server.llm_request_timeout: "0s"`，但同名环境变量优先。这个设置不会关闭客户端、上游或反代的时限，也不会关闭 SSE 单次写入保护。

若要同时关闭页面的两项计时器，再分别设为 `0`；若要让首字等待 `900s`，页面填 `900`，并确保总时限足够大或已禁用。首字耗时与总耗时是不同指标。

本次未读取或修改部署端配置，未操作数据库，未启动构建或部署。现场 `600s` 的最终来源仍需用运行日志确认。

---

## 注意事项 / 风险

- **`middleware.WithTimeout` 是公共函数**，`request_timeout`（默认 30s）也走它。
  改成 `<= 0` 不超时后，**若把 `request_timeout` 设为 0，管理 API 也会失去超时保护**。
  默认值未变（仍 30s），只在用户显式填 0 时行为改变。
- 两类总时限都为正时，`http.Server.WriteTimeout` 取二者最大值；任一非正时，全局写时限为 `0`，其他路由的有限 context 时限仍保留。长连接会更久占用资源，应结合并发限制使用。
- `deploy/nginx.conf` 里 `location /v1/` 仍有 `proxy_read_timeout 600s`。
  这是**上游连续无数据的读取间隔**，不是固定的请求总时长；如有长时间静默，需要部署者单独调整。
- 上游若给 `RetryPolicy` 加了新的钳制逻辑，注意别把这次的放开又覆盖回去。
- **渠道级响应超时（beta12 新增）** 同样不能用 600 挡：`internal/server/biz/channel_response_timeout.go` 的 `validateResponseTimeoutSeconds` 只校验 `>= 0`，前端 `frontend/src/config/retry-policy.ts` 的 `RESPONSE_TIMEOUT_MAX_SECONDS` 只作输入提示（当前 `86400`），`channels-action-dialog.tsx` 的 `parseResponseTimeoutSeconds` 不得再用它拒绝输入。
- 渠道响应超时前端还有两处文案/常量会随上游同步回归，同步后请重新执行上方核对清单第 8 项，并核对版本与 Git 差异。

---

## 渠道级响应超时（beta12 起同步维护）

上游在 `v1.0.0-beta12` 引入了按渠道覆盖响应超时（`feat(channels): support per-channel response timeout override`），它与全局两项共享同一个 600 上限，因此同样需要放开：

| 位置 | beta12 原样 | 本 fork |
|---|---|---|
| `internal/server/biz/channel_response_timeout.go` | `*seconds > maxRetryResponseTimeoutSeconds` → 报错 | 只校验 `>= 0`，无上限 |
| 同文件 `NormalizeResponseTimeout` 注释 | 提到 `0..600` 边界 | 改为仅提示负值非法 |
| `frontend/src/config/retry-policy.ts` | `RESPONSE_TIMEOUT_MAX_SECONDS = 600` | `= 86400`，仅作输入提示 |
| `frontend/src/features/channels/components/channels-action-dialog.tsx` | `parseResponseTimeoutSeconds` 超上限返回 `undefined` | 去掉上限判断 |
| `frontend/src/locales/{en,zh-CN}/channels.json` | `responseTimeout.validation` 写 `0 到 600` | 去掉 600 |
| `internal/server/biz/channel_response_timeout_test.go` | 用 `601` 断言拒绝 | 改为断言 **接受** 601/7200，用 `-1` 断言拒绝 |

语义保持与全局一致：`0` = 关闭该计时器，非负值照填；真正的天花板仍是 `server.llm_request_timeout`。

---

## 分支说明

- 本 fork 上游分支为 `unstable`；本地定制维护在 `main` 分支。
- 当前从 rc2 合并提交 `58ae5568eb6acd7578bfd93f592031945ec1cc92` 继续合并正式标签 `v1.0.0-rc.3`（`e70fb2a2676a0d9374a4665bc52accddd979a4d9`），保留现有超时定制和 rc1 的撤销结果。
- `origin` 指向本 fork；同步上游时先从 `https://github.com/looplj/axonhub.git` 获取指定标签，再合并该标签，避免把 fork 的旧 `unstable` 当作最新上游。
- 上游 `internal/build/VERSION` 在 rc3 中仍写着 `v1.0.0-beta9`；本 fork 已更新为 `v1.0.0-rc.3`，今后随同步标签维护，并结合提交核对源码版本。

---

## rc1 同步与撤销记录（2026-11-10）

按用户要求，本次从 `bf178190` 重新建立同步历史，不合入下面两个提交：

| 提交 | 取消的本地改动 |
|---|---|
| `b68c57a3e8618217de5cc7076a2d40af0ba45de5` | 请求记录创建前移、入站失败兜底记录、当时增加的运行期清理及测试 |
| `e38f073bc177a25795bae5e71b5beedfa63b945a` | 独立请求阶段诊断、`request_diagnostics` 配置、监测和测试，以及对前一次补丁的纠正 |

两提交的最终净变化整体移除；请求处理、持久化和流处理采用 rc1 的上游实现。上游已有的功能按上游保留，不按相似名称删除。

仍保留本文列出的超时定制：全局及渠道响应超时支持超过 `600s`、默认 LLM 总时限 `3600s`、非正值关闭本机对应时限、共享写时限的 `0` 语义，以及启动超时字段和 `server_request_timeout` 来源日志。最后两项来自更早的 `f8a47dc8`，不属于本次撤销的独立请求阶段诊断。

### 本地备份

同步前工作区干净，原 `main` 为 `e38f073b`。已创建：

- 本地备份分支：`backup/pre-rc1-e38f073b`。
- 仓库外备份目录：`D:/Project/axonhub/backups/pre-rc1-e38f073b/`。
- 完整历史 `axonhub-pre-rc1.bundle`，已通过 `git bundle verify`，无前置提交依赖。
- `patches/` 中的两个独立提交补丁、`removed-changes.patch` 中的最终净变化，以及 `LOCAL_CHANGES.before-rc1.md` 原备忘录。
- 备份目录的 `README.md` 包含提交全名、SHA-256、恢复命令和同步结果；备份文件仅保存在本地，不随 `main` 推送。

### 升级与核对

- rc1 已包含请求设置快照、减少持久化读回、429 冷却与重试等上游改进；本次完整吸收，不宣称它们已解决此前现场的上传中断或慢读。
- 先前部署的 `AXONHUB_SERVER_REQUEST_DIAGNOSTICS=true` / `server.request_diagnostics` 不再启用那组诊断；下次修改部署配置时可移除。本文未改动部署端文件或环境变量。
- 静态核对被取消的诊断、兜底及定时清理代码无残留；对应业务文件与 rc1 一致，保留的超时文件逐项复核。
- 验证仅包含 Git 差异、合并结构、UTF-8、JSON 及源码检查；沿用现有超时回归用例，未编译、运行测试、安装依赖或部署。

---

## rc2 同步与版本显示

- 同步起点：`c2e483afc5e8b7eaa16313a72969c390593b3366`（本 fork 的 rc1）。
- 上游目标：`v1.0.0-rc2`，提交 `59bb6734e30d2c8b17ac35f7b1ad6d7b0ad98ac1`。
- 本地回退分支：`backup/pre-rc2-c2e483af`，固定在同步前的 rc1；原 `backup/pre-rc1-e38f073b` 和仓库外完整备份继续保留。
- 在现有历史上正常合并 rc2，合并无冲突，不重新纳入 `b68c57a3` / `e38f073b` 的请求补记及独立阶段诊断。
- 吸收上游桌面端、渠道模型别名预览、Responses/Gemini 工具结果兼容性修复和依赖更新；保留本 fork 的超时定制。
- 本次额外改动为 `internal/build/VERSION` 更新至 `v1.0.0-rc2`，以及本备忘录的版本维护规则。今后同步时同样更新版本文件。
- 验证采用静态源码、定制补丁一致性、版本文件、UTF-8/JSON 和 Git 差异检查；本地定制差异检查通过。完整合并检查报告上游两个打包 JavaScript 文件中的 3 处尾随空白，已确认它们与 rc2 原文一致，保持原样。未编译、运行测试、安装依赖或部署。

---

## rc3 同步记录

- 同步起点：`58ae5568eb6acd7578bfd93f592031945ec1cc92`（本 fork 的 rc2）。
- 上游目标：`v1.0.0-rc.3`，提交 `e70fb2a2676a0d9374a4665bc52accddd979a4d9`。注意标签含点号，不是 `v1.0.0-rc3`。
- 本地回退分支：`backup/pre-rc3-58ae5568`，固定在同步前的 rc2；更早的回退分支与 rc1 撤销备份继续保留。
- 在现有历史上正常合并，合并无冲突；保留超时定制，`internal/build/VERSION` 同步更新为 `v1.0.0-rc.3`。
- 吸收上游 DeepSeek 额度显示、Trace/Thread 身份缓存、HTTP/2 断流及 GOAWAY 重试、配额回退选路、桌面端和插件页面修复。
- 上游新加入的身份缓存属于 rc3 原有实现；不重新纳入此前取消的 `b68c57a3` / `e38f073b` 请求补记和独立阶段诊断。
- 静态核对超时定制补丁、版本读取路径、撤销范围、UTF-8/JSON 和 Git 差异；未编译、运行测试、安装依赖或部署。
