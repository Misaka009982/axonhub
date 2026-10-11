# 本地改动备忘录（上游同步指南）

> 本文件记录本 fork 相对上游 `looplj/axonhub` 的**有意变更**。
> 每次从上游同步（rebase / merge / 重新 clone）后，请按本文逐项核对并重新施加改动。
>
> 上游仓库：`https://github.com/looplj/axonhub`（默认分支 `unstable`）
> 本 fork：`https://github.com/Misaka009982/axonhub`
> 基线版本：`v1.0.0-beta11`（commit `b8bb3421`）

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

# 7. 确认改动清单
git status --short
```

第 1～3 项应无匹配；第 4 项应看到 `3600s` 默认值；第 5～6 项应有对应逻辑。不要把“无输出”一律当作失败。

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

---

## 分支说明

- 本 fork 上游分支为 `unstable`；本改动推送到新建的 `main` 分支。
- `main` 基线下沉在 `v1.0.0-beta11`（`b8bb3421`），**不含** beta11 之后的 4 个提交。
  后续如需对齐上游，可 `git rebase`/`merge origin/unstable`。
