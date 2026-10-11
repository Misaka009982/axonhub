package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/looplj/axonhub/internal/log"
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
