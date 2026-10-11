package server

import (
	"testing"
	"time"
)

func TestConfigWriteTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		requestTimeout time.Duration
		llmTimeout     time.Duration
		want           time.Duration
	}{
		{name: "both positive", requestTimeout: 30 * time.Second, llmTimeout: time.Hour, want: time.Hour},
		{name: "ordinary request is longer", requestTimeout: 2 * time.Hour, llmTimeout: time.Hour, want: 2 * time.Hour},
		{name: "above historical ceiling", requestTimeout: 30 * time.Second, llmTimeout: 900 * time.Second, want: 900 * time.Second},
		{name: "LLM timeout disabled", requestTimeout: 30 * time.Second, llmTimeout: 0, want: 0},
		{name: "negative LLM timeout", requestTimeout: 30 * time.Second, llmTimeout: -time.Second, want: 0},
		{name: "ordinary timeout disabled", requestTimeout: 0, llmTimeout: time.Hour, want: 0},
		{name: "negative ordinary timeout", requestTimeout: -time.Second, llmTimeout: time.Hour, want: 0},
		{name: "both disabled", requestTimeout: 0, llmTimeout: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				RequestTimeout:    tt.requestTimeout,
				LLMRequestTimeout: tt.llmTimeout,
			}

			if got := cfg.writeTimeout(); got != tt.want {
				t.Fatalf("全局写超时 = %s，期望 %s", got, tt.want)
			}
		})
	}
}
