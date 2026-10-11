package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWithTimeoutDisabledPreservesParent(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()

			router := gin.New()
			router.GET("/test", WithTimeout(timeout), func(c *gin.Context) {
				ctx := c.Request.Context()
				require.Equal(t, parent, ctx)
				_, hasDeadline := ctx.Deadline()
				require.False(t, hasDeadline)
				require.NoError(t, ctx.Err())

				cancel()
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(parent))
			require.Equal(t, http.StatusNoContent, recorder.Code)
		})
	}
}

func TestWithTimeoutHonorsValuesAbove600Seconds(t *testing.T) {
	const timeout = 900 * time.Second
	var deadline time.Time
	var hasDeadline bool

	router := gin.New()
	router.GET("/test", WithTimeout(timeout), func(c *gin.Context) {
		deadline, hasDeadline = c.Request.Context().Deadline()
		require.NoError(t, c.Request.Context().Err())
		c.Status(http.StatusNoContent)
	})

	before := time.Now()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
	after := time.Now()

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.True(t, hasDeadline)
	require.False(t, deadline.Before(before.Add(timeout)))
	require.False(t, deadline.After(after.Add(timeout)))
}

func TestWithTimeoutPreservesEarlierParentDeadline(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			parentCause := errors.New("parent deadline")
			parentDeadline := time.Now().Add(-time.Second)
			parent, cancel := context.WithDeadlineCause(context.Background(), parentDeadline, parentCause)
			defer cancel()

			router := gin.New()
			router.GET("/test", WithTimeout(timeout), func(c *gin.Context) {
				ctx := c.Request.Context()
				deadline, hasDeadline := ctx.Deadline()
				require.True(t, hasDeadline)
				require.Equal(t, parentDeadline, deadline)
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
				require.ErrorIs(t, context.Cause(ctx), parentCause)
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(parent))
			require.Equal(t, http.StatusNoContent, recorder.Code)
		})
	}
}

func TestWithTimeoutRecordsServerDeadlineCause(t *testing.T) {
	var requestErr, cause error

	router := gin.New()
	router.GET("/test", WithTimeout(time.Millisecond), func(c *gin.Context) {
		ctx := c.Request.Context()
		select {
		case <-ctx.Done():
			requestErr = ctx.Err()
			cause = context.Cause(ctx)
		case <-time.After(time.Second):
			t.Fatal("服务端请求超时未触发")
		}
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.ErrorIs(t, requestErr, context.DeadlineExceeded)
	require.ErrorIs(t, cause, context.DeadlineExceeded)
	require.Contains(t, cause.Error(), "server request timeout")
}
