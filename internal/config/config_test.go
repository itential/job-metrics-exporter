package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load empty path: %v", err)
	}
	if cfg.Mongo.Host != "localhost" {
		t.Errorf("Mongo.Host: got %q, want %q", cfg.Mongo.Host, "localhost")
	}
	if cfg.Mongo.Port != 27017 {
		t.Errorf("Mongo.Port: got %d, want 27017", cfg.Mongo.Port)
	}
	if cfg.Mongo.Database != "itential" {
		t.Errorf("Mongo.Database: got %q, want %q", cfg.Mongo.Database, "itential")
	}
	if cfg.Mongo.AuthSource != "admin" {
		t.Errorf("Mongo.AuthSource: got %q, want %q", cfg.Mongo.AuthSource, "admin")
	}
	if cfg.Exporter.ListenAddress != ":9477" {
		t.Errorf("Exporter.ListenAddress: got %q, want :9477", cfg.Exporter.ListenAddress)
	}
	if cfg.Exporter.MetricsPath != "/metrics" {
		t.Errorf("Exporter.MetricsPath: got %q, want /metrics", cfg.Exporter.MetricsPath)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level: got %q, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != "json" {
		t.Errorf("Log.Format: got %q, want json", cfg.Log.Format)
	}
	if cfg.ChangeStream.Enabled {
		t.Error("ChangeStream.Enabled: want false by default")
	}
	if cfg.Polling.Enabled {
		t.Error("Polling.Enabled: want false by default")
	}
}

func TestLoadYAML(t *testing.T) {
	yaml := strings.TrimSpace(`
mongo:
  host: "mongohost"
  port: 27018
  username: "user"
  password: "pass"
  database: "mydb"
  auth_source: "mydb"
log:
  level: "debug"
  format: "text"
`)
	f, err := os.CreateTemp("", "config*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mongo.Host != "mongohost" {
		t.Errorf("Mongo.Host: got %q, want mongohost", cfg.Mongo.Host)
	}
	if cfg.Mongo.Port != 27018 {
		t.Errorf("Mongo.Port: got %d, want 27018", cfg.Mongo.Port)
	}
	if cfg.Mongo.Username != "user" {
		t.Errorf("Mongo.Username: got %q, want user", cfg.Mongo.Username)
	}
	if cfg.Mongo.Password != "pass" {
		t.Errorf("Mongo.Password: got %q, want pass", cfg.Mongo.Password)
	}
	if cfg.Mongo.Database != "mydb" {
		t.Errorf("Mongo.Database: got %q, want mydb", cfg.Mongo.Database)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level: got %q, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format: got %q, want text", cfg.Log.Format)
	}
}

func TestLoadYAML_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_HOST", "envhost")
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_PORT", "27099")
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_USERNAME", "envuser")
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_PASSWORD", "envpass")
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_DATABASE", "envdb")
	t.Setenv("ITENTIAL_JOB_METRIC_LOG_LEVEL", "debug")
	t.Setenv("ITENTIAL_JOB_METRIC_LOG_FORMAT", "text")
	t.Setenv("ITENTIAL_JOB_METRIC_LISTEN_ADDRESS", ":8080")
	t.Setenv("ITENTIAL_JOB_METRIC_METRICS_PATH", "/prom")
	t.Setenv("ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED", "true")
	t.Setenv("ITENTIAL_JOB_METRIC_POLLING_ENABLED", "1")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mongo.Host != "envhost" {
		t.Errorf("Mongo.Host: got %q, want envhost", cfg.Mongo.Host)
	}
	if cfg.Mongo.Port != 27099 {
		t.Errorf("Mongo.Port: got %d, want 27099", cfg.Mongo.Port)
	}
	if cfg.Mongo.Username != "envuser" {
		t.Errorf("Mongo.Username: got %q, want envuser", cfg.Mongo.Username)
	}
	if cfg.Mongo.Password != "envpass" {
		t.Errorf("Mongo.Password: got %q, want envpass", cfg.Mongo.Password)
	}
	if cfg.Mongo.Database != "envdb" {
		t.Errorf("Mongo.Database: got %q, want envdb", cfg.Mongo.Database)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level: got %q, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format: got %q, want text", cfg.Log.Format)
	}
	if cfg.Exporter.ListenAddress != ":8080" {
		t.Errorf("ListenAddress: got %q, want :8080", cfg.Exporter.ListenAddress)
	}
	if cfg.Exporter.MetricsPath != "/prom" {
		t.Errorf("MetricsPath: got %q, want /prom", cfg.Exporter.MetricsPath)
	}
	if !cfg.ChangeStream.Enabled {
		t.Error("ChangeStream.Enabled: want true")
	}
	if !cfg.Polling.Enabled {
		t.Error("Polling.Enabled: want true")
	}
}

func TestEnvOverride_Duration(t *testing.T) {
	t.Setenv("ITENTIAL_JOB_METRIC_CACHE_TTL", "2m")
	t.Setenv("ITENTIAL_JOB_METRIC_QUERY_TIMEOUT", "90s")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Exporter.CacheTTL != 2*time.Minute {
		t.Errorf("CacheTTL: got %s, want 2m", cfg.Exporter.CacheTTL)
	}
	if cfg.Exporter.QueryTimeout != 90*time.Second {
		t.Errorf("QueryTimeout: got %s, want 90s", cfg.Exporter.QueryTimeout)
	}
}

func TestEnvOverride_InvalidPort(t *testing.T) {
	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_PORT", "notanumber")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Invalid port env var is silently ignored; default is preserved.
	if cfg.Mongo.Port != 27017 {
		t.Errorf("Port: got %d, want default 27017", cfg.Mongo.Port)
	}
}

func TestEnvOverride_TakePrecedenceOverYAML(t *testing.T) {
	yaml := "mongo:\n  host: yamlhost\n"
	f, _ := os.CreateTemp("", "config*.yaml")
	t.Cleanup(func() { os.Remove(f.Name()) })
	f.WriteString(yaml)
	f.Close()

	t.Setenv("ITENTIAL_JOB_METRIC_MONGO_HOST", "envhost")

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mongo.Host != "envhost" {
		t.Errorf("Mongo.Host: got %q, want envhost (env should override YAML)", cfg.Mongo.Host)
	}
}

func TestValidate_ExporterTLSMissingCert(t *testing.T) {
	cfg := defaults()
	cfg.Exporter.TLS.Enabled = true
	cfg.Exporter.TLS.KeyFile = "server.key"
	if err := validate(cfg); err == nil {
		t.Error("expected error for missing cert_file, got nil")
	}
}

func TestValidate_ExporterTLSMissingKey(t *testing.T) {
	cfg := defaults()
	cfg.Exporter.TLS.Enabled = true
	cfg.Exporter.TLS.CertFile = "server.crt"
	if err := validate(cfg); err == nil {
		t.Error("expected error for missing key_file, got nil")
	}
}

func TestValidate_QueryTimeoutExceedsCacheTTL(t *testing.T) {
	cfg := defaults()
	cfg.Exporter.QueryTimeout = 60 * time.Second
	cfg.Exporter.CacheTTL = 60 * time.Second
	if err := validate(cfg); err == nil {
		t.Error("expected error when query_timeout >= cache_ttl, got nil")
	}
}

func TestValidate_SlowQueryTimeoutExceedsSlowCacheTTL(t *testing.T) {
	cfg := defaults()
	cfg.Exporter.SlowQueryTimeout = 5 * time.Minute
	cfg.Exporter.SlowCacheTTL = 5 * time.Minute
	if err := validate(cfg); err == nil {
		t.Error("expected error when slow_query_timeout >= slow_cache_ttl, got nil")
	}
}

func TestValidate_PollingQueryTimeoutExceedsInterval(t *testing.T) {
	cfg := defaults()
	cfg.Polling.Enabled = true
	cfg.Polling.QueryTimeout = 60 * time.Second
	cfg.Polling.Interval = 60 * time.Second
	if err := validate(cfg); err == nil {
		t.Error("expected error when polling query_timeout >= interval, got nil")
	}
}

func TestValidate_PollingDisabledSkipsIntervalCheck(t *testing.T) {
	cfg := defaults()
	cfg.Polling.Enabled = false
	cfg.Polling.QueryTimeout = 60 * time.Second
	cfg.Polling.Interval = 60 * time.Second
	if err := validate(cfg); err != nil {
		t.Errorf("unexpected error with polling disabled: %v", err)
	}
}

func TestValidate_MongoTLSCAFileNotFound(t *testing.T) {
	cfg := defaults()
	cfg.Mongo.TLS.Enabled = true
	cfg.Mongo.TLS.CAFile = "/nonexistent/ca.pem"
	if err := validate(cfg); err == nil {
		t.Error("expected error for nonexistent CA file, got nil")
	}
}

func TestValidate_MongoTLSNoCAFile(t *testing.T) {
	cfg := defaults()
	cfg.Mongo.TLS.Enabled = true
	// CAFile is empty — no file check should occur.
	if err := validate(cfg); err != nil {
		t.Errorf("unexpected error with TLS enabled but no CA file: %v", err)
	}
}
