package conf

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

func TestLoaderReloadUsesFreshViper(t *testing.T) {
	configFile := writeTestConfig(t, `
log:
  level: info
`)
	loader := &Loader{configFile: configFile}

	initial, err := loader.Reload()
	if err != nil {
		t.Fatalf("initial Reload() error = %v", err)
	}
	if initial.Log.Level != zapcore.InfoLevel {
		t.Fatalf("initial log level = %s, want %s", initial.Log.Level, zapcore.InfoLevel)
	}

	if err := os.WriteFile(configFile, []byte(`
log:
  level: debug
`), 0o600); err != nil {
		t.Fatalf("rewrite test config: %v", err)
	}

	reloaded, err := loader.Reload()
	if err != nil {
		t.Fatalf("reloaded Reload() error = %v", err)
	}
	if reloaded.Log.Level != zapcore.DebugLevel {
		t.Fatalf("reloaded log level = %s, want %s", reloaded.Log.Level, zapcore.DebugLevel)
	}
}

func TestSSEKeepAliveConfig(t *testing.T) {
	configFile := writeTestConfig(t, `
server:
  sse_keep_alive:
    enabled: true
    interval: 30s
`)

	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.APIServer.SSEKeepAlive.Enabled {
		t.Fatal("SSE keep-alive should be enabled")
	}
	if cfg.APIServer.SSEKeepAlive.Interval != 30*time.Second {
		t.Fatalf("SSE keep-alive interval = %s, want 30s", cfg.APIServer.SSEKeepAlive.Interval)
	}
}

func TestSSEKeepAliveDefaultsToDisabled(t *testing.T) {
	configFile := writeTestConfig(t, "")

	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.APIServer.SSEKeepAlive.Enabled {
		t.Fatal("SSE keep-alive should default to disabled")
	}
	if cfg.APIServer.SSEKeepAlive.Interval != 15*time.Second {
		t.Fatalf("SSE keep-alive interval = %s, want 15s", cfg.APIServer.SSEKeepAlive.Interval)
	}
}

func TestMetricsExporterConfigFromEnvironment(t *testing.T) {
	t.Setenv("AXONHUB_METRICS_ENABLED", "true")
	t.Setenv("AXONHUB_METRICS_EXPORTER_TYPE", "otlphttp")
	t.Setenv("AXONHUB_METRICS_EXPORTER_ENDPOINT", "alloy.alloy.svc.cluster.local:4318")
	t.Setenv("AXONHUB_METRICS_EXPORTER_INSECURE", "true")

	configFile := writeTestConfig(t, "")
	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.Metrics.Enabled {
		t.Fatal("metrics should be enabled")
	}
	if cfg.Metrics.Exporter.Type != "otlphttp" {
		t.Fatalf("metrics exporter type = %q, want %q", cfg.Metrics.Exporter.Type, "otlphttp")
	}
	if cfg.Metrics.Exporter.Endpoint != "alloy.alloy.svc.cluster.local:4318" {
		t.Fatalf(
			"metrics exporter endpoint = %q, want %q",
			cfg.Metrics.Exporter.Endpoint,
			"alloy.alloy.svc.cluster.local:4318",
		)
	}
	if !cfg.Metrics.Exporter.Insecure {
		t.Fatal("metrics exporter should use an insecure connection")
	}
}

func TestTraceExtractionDefaultsToEnabled(t *testing.T) {
	configFile := writeTestConfig(t, "")

	cfg, _, err := loadConfig(configFile)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	trace := cfg.APIServer.Trace
	if !trace.ClaudeCodeTraceEnabled {
		t.Error("Claude Code trace extraction should default to enabled")
	}
	if !trace.CodexTraceEnabled {
		t.Error("Codex trace extraction should default to enabled")
	}
	if !trace.OpenCodeTraceEnabled {
		t.Error("OpenCode trace extraction should default to enabled")
	}
}

func TestLLMRequestTimeoutConfig(t *testing.T) {
	t.Setenv("AXONHUB_SERVER_REQUEST_TIMEOUT", "")

	tests := []struct {
		name   string
		config string
		env    string
		want   time.Duration
	}{
		{name: "default", want: time.Hour},
		{name: "explicit legacy value", config: "server:\n  llm_request_timeout: 600s\n", want: 600 * time.Second},
		{name: "above historical ceiling", config: "server:\n  llm_request_timeout: 900s\n", want: 900 * time.Second},
		{name: "disabled in config", config: "server:\n  llm_request_timeout: 0s\n", want: 0},
		{name: "environment disables config timeout", config: "server:\n  llm_request_timeout: 600s\n", env: "0", want: 0},
		{name: "environment extends config timeout", config: "server:\n  llm_request_timeout: 600s\n", env: "7200s", want: 2 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AXONHUB_SERVER_LLM_REQUEST_TIMEOUT", tt.env)
			cfg, _, err := loadConfig(writeTestConfig(t, tt.config))
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if cfg.APIServer.LLMRequestTimeout != tt.want {
				t.Fatalf("LLM总时限 = %s，期望 %s", cfg.APIServer.LLMRequestTimeout, tt.want)
			}
			if cfg.APIServer.RequestTimeout != 30*time.Second {
				t.Fatalf("管理请求超时 = %s，期望 30s", cfg.APIServer.RequestTimeout)
			}
		})
	}
}

func writeTestConfig(t *testing.T, contents string) string {
	t.Helper()

	configFile := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(configFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return configFile
}
