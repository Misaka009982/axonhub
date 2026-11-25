package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/llm/httpclient"
)

// WithTimeout 设置请求总时限。非正值只禁用本层计时器，
// 保留父 context 的 deadline 和客户端取消，不代表关闭所有来源的超时。
func WithTimeout(ts time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ts <= 0 {
			c.Next()

			return
		}

		started := time.Now()
		timeoutCause := fmt.Errorf("server request timeout after %s: %w", ts, context.DeadlineExceeded)
		ctx, cancel := context.WithTimeoutCause(c.Request.Context(), ts, timeoutCause)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		// 只把本层计时器触发的超时记为服务端总时限，避免误判父级或客户端取消。
		if context.Cause(ctx) == timeoutCause {
			log.Warn(c.Request.Context(), "Server request timeout exceeded",
				log.String("source", "server_request_timeout"),
				log.String("route", c.FullPath()),
				log.Duration("timeout", ts),
				log.Duration("elapsed", time.Since(started)),
				log.Cause(timeoutCause),
			)
		}
	}
}

type llmTimeoutKey struct{}

type llmTimeoutPolicy struct {
	started                   time.Time
	request, stream           time.Duration
	requestCause, streamCause error
}

// WithLLMTimeout 在正文解析前保留较大的总预算，任一预算为 0 时先保持无限等待。
// 解析后再选择请求类型；本机总时限使用独有 cause，不误报父级或客户端取消。
func WithLLMTimeout(requestTimeout, streamTimeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		policy := llmTimeoutPolicy{
			started:      time.Now(),
			request:      requestTimeout,
			stream:       streamTimeout,
			requestCause: fmt.Errorf("server request timeout after %s: %w", requestTimeout, context.DeadlineExceeded),
			streamCause:  fmt.Errorf("server request timeout after %s: %w", streamTimeout, context.DeadlineExceeded),
		}
		timeout, timeoutCause := requestTimeout, policy.requestCause
		if c.Request.Method == http.MethodPost {
			if streamTimeout > requestTimeout {
				timeout, timeoutCause = streamTimeout, policy.streamCause
			}
			if requestTimeout <= 0 || streamTimeout <= 0 {
				timeout = 0
			}
		}
		ctx := context.WithValue(c.Request.Context(), llmTimeoutKey{}, policy)
		var cancel context.CancelFunc
		if timeout <= 0 {
			ctx, cancel = context.WithCancel(ctx)
		} else {
			ctx, cancel = context.WithDeadlineCause(ctx, policy.started.Add(timeout), timeoutCause)
		}
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		if timeout <= 0 {
			writer := &llmResponseWriter{ResponseWriter: c.Writer, timeout: llmWriteTimeout}
			c.Writer = writer
			writer.setDeadline(time.Time{})
			// net/http may flush buffered headers/body after Gin returns.
			defer func() { writer.setDeadline(time.Now().Add(writer.timeout)) }()
		}
		c.Next()

		cause := context.Cause(c.Request.Context())
		if cause == policy.requestCause || cause == policy.streamCause {
			timedOut, configKey := policy.request, "server.llm_request_timeout"
			if cause == policy.streamCause {
				timedOut, configKey = policy.stream, "server.llm_stream_request_timeout"
			}
			log.Warn(c.Request.Context(), "Server request timeout exceeded",
				log.String("source", "server_request_timeout"),
				log.String("config_key", configKey),
				log.String("route", c.FullPath()),
				log.Duration("timeout", timedOut),
				log.Duration("elapsed", time.Since(policy.started)),
				log.Cause(cause),
			)
		}
	}
}

// SelectLLMTimeout 从最初到达时刻计算所选总时限，包含正文读取和请求准备。
func SelectLLMTimeout(c *gin.Context, streaming bool) context.CancelFunc {
	policy, ok := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy)
	if !ok {
		return func() {}
	}
	timeout, timeoutCause := policy.request, policy.requestCause
	if streaming {
		timeout, timeoutCause = policy.stream, policy.streamCause
	}
	if timeout <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithDeadlineCause(c.Request.Context(), policy.started.Add(timeout), timeoutCause)
	c.Request = c.Request.WithContext(ctx)
	return cancel
}

// WithStreamIdleTimeout configures upstream reads without counting downstream keep-alives.
func WithStreamIdleTimeout(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := httpclient.WithStreamIdleTimeout(c.Request.Context(), timeout)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
