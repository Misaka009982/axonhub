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

	"github.com/looplj/axonhub/llm/httpclient"
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

func TestWithLLMTimeoutUnlimitedPreservesCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	router := gin.New()
	router.Use(WithLLMTimeout(0, 0))
	router.GET("/", func(c *gin.Context) {
		handlerContext := c.Request.Context()
		_, hasDeadline := handlerContext.Deadline()
		require.False(t, hasDeadline)
		require.NoError(t, handlerContext.Err())
		cancelParent()
		require.ErrorIs(t, handlerContext.Err(), context.Canceled)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(parent))
}

func TestWithLLMTimeoutFiniteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(WithLLMTimeout(10*time.Millisecond, 10*time.Millisecond))
	router.GET("/", func(c *gin.Context) {
		_, hasDeadline := c.Request.Context().Deadline()
		require.True(t, hasDeadline)
		<-c.Request.Context().Done()
		require.ErrorIs(t, c.Request.Context().Err(), context.DeadlineExceeded)
		policy := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy)
		require.Equal(t, policy.requestCause, context.Cause(c.Request.Context()))
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestWithLLMTimeoutCancelsContextAfterHandlerReturns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var handlerErr func() error
	router := gin.New()
	router.Use(WithLLMTimeout(0, 0))
	router.GET("/", func(c *gin.Context) { handlerErr = c.Request.Context().Err })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.ErrorIs(t, handlerErr(), context.Canceled)
}

func TestLLMTimeoutAndStreamIdleTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name        string
		total, idle time.Duration
		cause       error
	}{
		{"idle with unlimited total", 0, 10 * time.Millisecond, httpclient.ErrStreamIdleTimeout},
		{"total expires first", 10 * time.Millisecond, time.Second, context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(WithLLMTimeout(tt.total, tt.total), WithStreamIdleTimeout(tt.idle))
			router.GET("/", func(c *gin.Context) {
				upstreamContext, _, stop := httpclient.NewStreamIdleContext(c.Request.Context())
				defer stop()
				select {
				case <-upstreamContext.Done():
				case <-time.After(2 * time.Second):
					t.Fatal("upstream timeout did not fire")
				}
				require.ErrorIs(t, context.Cause(upstreamContext), tt.cause)
				if tt.total == 0 {
					require.NoError(t, c.Request.Context().Err(), "upstream idle must not cancel the downstream request")
				}
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		})
	}
}

func TestSelectLLMTimeoutIndependentBudgets(t *testing.T) {
	for _, tt := range []struct {
		name            string
		request, stream time.Duration
		streaming       bool
		expected        time.Duration
	}{
		{"nonstream", time.Second, 3 * time.Second, false, time.Second},
		{"stream", time.Second, 3 * time.Second, true, 3 * time.Second},
		{"shorter stream", 3 * time.Second, time.Second, true, time.Second},
		{"unlimited nonstream", 0, time.Second, false, 0},
		{"finite stream with unlimited nonstream", 0, time.Second, true, time.Second},
		{"unlimited stream", time.Second, 0, true, 0},
		{"finite nonstream with unlimited stream", time.Second, 0, false, time.Second},
		{"stream above historical ceiling", time.Second, 900 * time.Second, true, 900 * time.Second},
		{"nonstream above historical ceiling", 2 * time.Hour, time.Hour, false, 2 * time.Hour},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(WithLLMTimeout(tt.request, tt.stream))
			router.POST("/", func(c *gin.Context) {
				start := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy).started
				cancel := SelectLLMTimeout(c, tt.streaming)
				defer cancel()
				deadline, ok := c.Request.Context().Deadline()
				require.Equal(t, tt.expected != 0, ok)
				if ok {
					require.Equal(t, start.Add(tt.expected), deadline)
				}
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
		})
	}
}

func TestSelectLLMTimeoutIncludesRequestPreparation(t *testing.T) {
	router := gin.New()
	router.Use(WithLLMTimeout(time.Second, 2*time.Second))
	router.POST("/", func(c *gin.Context) {
		policy := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy)
		policy.started = time.Now().Add(-1500 * time.Millisecond)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), llmTimeoutKey{}, policy))
		cancel := SelectLLMTimeout(c, false)
		defer cancel()
		require.ErrorIs(t, c.Request.Context().Err(), context.DeadlineExceeded)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
}

func TestSelectLLMTimeoutRecordsSelectedDeadlineCause(t *testing.T) {
	for _, tt := range []struct {
		name      string
		streaming bool
	}{
		{"nonstream", false},
		{"stream", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(WithLLMTimeout(900*time.Second, 2*time.Hour))
			router.POST("/", func(c *gin.Context) {
				policy := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy)
				policy.started = time.Now().Add(-3 * time.Hour)
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), llmTimeoutKey{}, policy))
				cancel := SelectLLMTimeout(c, tt.streaming)
				defer cancel()
				expectedCause := policy.requestCause
				if tt.streaming {
					expectedCause = policy.streamCause
				}
				require.ErrorIs(t, c.Request.Context().Err(), context.DeadlineExceeded)
				require.Equal(t, expectedCause, context.Cause(c.Request.Context()))
				require.ErrorIs(t, expectedCause, context.DeadlineExceeded)
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
		})
	}
}

func TestSelectLLMTimeoutPreservesEarlierParentCause(t *testing.T) {
	for _, tt := range []struct {
		name      string
		streaming bool
	}{
		{"nonstream", false},
		{"stream", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parentCause := errors.New("parent deadline")
			parent, cancelParent := context.WithDeadlineCause(context.Background(), time.Now().Add(-time.Second), parentCause)
			defer cancelParent()
			router := gin.New()
			router.Use(WithLLMTimeout(900*time.Second, 2*time.Hour))
			router.POST("/", func(c *gin.Context) {
				policy := c.Request.Context().Value(llmTimeoutKey{}).(llmTimeoutPolicy)
				cancel := SelectLLMTimeout(c, tt.streaming)
				defer cancel()
				require.ErrorIs(t, c.Request.Context().Err(), context.DeadlineExceeded)
				require.Equal(t, parentCause, context.Cause(c.Request.Context()))
				require.NotEqual(t, policy.requestCause, context.Cause(c.Request.Context()))
				require.NotEqual(t, policy.streamCause, context.Cause(c.Request.Context()))
			})
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil).WithContext(parent))
		})
	}
}
