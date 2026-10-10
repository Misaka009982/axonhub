# 本地改动备忘录（上游同步指南）

> 本文件记录本 fork 相对上游 `looplj/axonhub` 的**有意变更**。
> 每次从上游同步（rebase / merge / 重新 clone）后，请按本文逐项核对并重新施加改动。
>
> 上游仓库：`https://github.com/looplj/axonhub`（默认分支 `unstable`）
> 本 fork：`https://github.com/Misaka009982/axonhub`
> 基线版本：`v1.0.0-beta11`（commit `b8bb3421`）

---

## 变更总览

**主题：让「流式首字超时 / 非流式传输超时」可自由配置，并让 0 真正表示"关闭"。**

### 问题背景

用户反馈：设置里两项超时都填 `0`（文档说 0 = 关闭），但请求仍在 **600s** 被中断。

排查结论：600s 有**两个独立来源**，只改设置没用。

| 来源 | 位置 | 作用 |
|------|------|------|
| ① 设置上限钳制 | `maxRetryResponseTimeoutSeconds = 600` | 把用户填的值强行截到 600 |
| ② **真正的"凶手"** | `server.llm_request_timeout` 默认 `600s` | 整个 `/v1/*` 路由的 context 超时 |

来源 ② 触发时日志为 `Stream deadline exceeded`，错误是 `context.DeadlineExceeded`
（见 `internal/server/api/chat.go` 的 `writeSSEStreamEnd`），**不是**首字超时错误。

另外 `context.WithTimeout(ctx, 0)` 会立即超时，所以旧代码里
`llm_request_timeout` 填 `0` = **立刻失败**，无法表示"关闭"。

### 修改后的预期行为

- 两项设置填 `0` → **真正关闭**该超时（不再有 600s 兜底）。
- 填 `> 600` → **按填写值生效**，不再被截断。
- `server.llm_request_timeout` 默认放宽到 `3600s`，且支持填 `0` = 禁用服务端 LLM 请求超时。
- 两者关系：设置项是"业务级"超时，`llm_request_timeout` 是"请求级"兜底天花板。

---

## 需要重新施加的改动（共 6 个文件）

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

        ctx, cancel := context.WithTimeout(c.Request.Context(), ts)
        // ...原逻辑不变
    }
}
```

### 3. `internal/server/server.go`

`Run()` 里的 `WriteTimeout` 抽成变量并补注释（值本身等价，只是显式化 + 说明）：

```go
// A non-positive WriteTimeout means "no write deadline" in net/http, which is
// what we want when the LLM request timeout is explicitly disabled (0).
writeTimeout := max(srv.Config.RequestTimeout, srv.Config.LLMRequestTimeout)
srv.server = &http.Server{
    Addr:         addr,
    Handler:      srv.Engine,
    ReadTimeout:  srv.Config.ReadTimeout,
    WriteTimeout: writeTimeout,
}
```

### 4. `conf/conf.go`

`setDefaults` 中：

```go
- v.SetDefault("server.llm_request_timeout", "600s")
+ v.SetDefault("server.llm_request_timeout", "3600s")
```

### 5. `config.example.yml`

```yaml
- llm_request_timeout: "600s"   # LLM request timeout duration (env: ...)
+ llm_request_timeout: "3600s"  # LLM request timeout duration; set to 0 to disable (env: ...)
```

### 6. `frontend/src/features/system/components/retry-settings.tsx`

删除两个 `Input` 上的 `max='600'`（`stream-first-event-timeout` 与 `non-stream-response-timeout`），
保留 `min='0'`。

### 7. i18n 文案（2 个文件）

`frontend/src/locales/en/system.json` 与 `frontend/src/locales/zh-CN/system.json`：

- `system.retry.streamFirstEventTimeoutSeconds.description`
- `system.retry.nonStreamResponseTimeoutSeconds.description`

把 `(0-600s)` 去掉，改成"无上限，仍受 server.llm_request_timeout（默认 3600s）约束"。
（历史版本可能有 `zh.json`，当前为 `zh-CN` + `en`。）

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

# 6. 确认改动清单
git status --short
```

若某项无输出/输出为旧值，说明上游同步覆盖了改动，照上面「需要重新施加的改动」重做即可。

---

## 注意事项 / 风险

- **`middleware.WithTimeout` 是公共函数**，`request_timeout`（默认 30s）也走它。
  改成 `<= 0` 不超时后，**若把 `request_timeout` 设为 0，管理 API 也会失去超时保护**。
  默认值未变（仍 30s），只在用户显式填 0 时行为改变。
- `llm_request_timeout` 从 600s 提到 3600s 会**同时拉长** `http.Server.WriteTimeout`
  （取 `max(request_timeout, llm_request_timeout)`），即所有路由的写超时上限变长。
- `deploy/nginx.conf` 里 `location /v1/` 仍有 `proxy_read_timeout 600s`。
  **若通过该 nginx 反代访问，超过 600s 仍会被 nginx 掐断**，需自行调大。
- 上游若给 `RetryPolicy` 加了新的钳制逻辑，注意别把这次的放开又覆盖回去。

---

## 分支说明

- 本 fork 上游分支为 `unstable`；本改动推送到新建的 `main` 分支。
- `main` 基线下沉在 `v1.0.0-beta11`（`b8bb3421`），**不含** beta11 之后的 4 个提交。
  后续如需对齐上游，可 `git rebase`/`merge origin/unstable`。
