package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// WithTimeout applies a request-scoped deadline. A non-positive duration disables
// the middleware entirely, so llm_request_timeout/request_timeout set to 0 means
// "no server-imposed timeout" instead of an immediately-expired deadline.
func WithTimeout(ts time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ts <= 0 {
			c.Next()

			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), ts)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
