package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration structure.
type Config struct {
	Mongo        MongoConfig        `yaml:"mongo"`
	Exporter     ExporterConfig     `yaml:"exporter"`
	Log          LogConfig          `yaml:"log"`
	ChangeStream ChangeStreamConfig `yaml:"change_stream"`
	Polling      PollingConfig      `yaml:"polling"`
}

// PollingConfig controls the background query-based metric collection.
type PollingConfig struct {
	// Enabled activates background aggregation queries for job/task status counts.
	Enabled bool `yaml:"enabled"`
	// Interval is how often the background queries run.
	Interval time.Duration `yaml:"interval"`
	// QueryTimeout is the deadline applied to each individual query.
	// Must be less than Interval.
	QueryTimeout time.Duration `yaml:"query_timeout"`
}

// ChangeStreamConfig controls the MongoDB change stream watcher.
type ChangeStreamConfig struct {
	// Enabled activates real-time status-change tracking via MongoDB change streams.
	// Requires MongoDB to be running as a replica set.
	Enabled bool `yaml:"enabled"`
	// InitialLoad populates the watcher's in-memory state map from a projection
	// query on startup so it can detect the previous status of documents that
	// existed before the change stream was opened. Only active (running/error)
	// documents are loaded to keep memory bounded.
	InitialLoad bool `yaml:"initial_load"`
	// InitialLoadTimeout is the deadline for the initial load queries.
	InitialLoadTimeout time.Duration `yaml:"initial_load_timeout"`
}

// MongoConfig holds all MongoDB connection parameters.
// If URI is set, it takes precedence over Host/Port/Username/Password/AuthSource.
type MongoConfig struct {
	URI        string   `yaml:"uri"`
	Host       string   `yaml:"host"`
	Port       int      `yaml:"port"`
	Username   string   `yaml:"username"`
	Password   string   `yaml:"password"`
	Database   string   `yaml:"database"`
	AuthSource string   `yaml:"auth_source"`
	TLS        MongoTLS `yaml:"tls"`
}

// MongoTLS holds TLS settings for the MongoDB connection.
// Mutual TLS (client cert) is not currently supported but can be added here.
type MongoTLS struct {
	Enabled            bool   `yaml:"enabled"`
	CAFile             string `yaml:"ca_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// ExporterConfig holds HTTP server configuration.
type ExporterConfig struct {
	ListenAddress string      `yaml:"listen_address"`
	MetricsPath   string      `yaml:"metrics_path"`
	TLS           ExporterTLS `yaml:"tls"`
	// Polling fields below are unused while query-based collection is disabled.
	CacheTTL         time.Duration `yaml:"cache_ttl"`
	QueryTimeout     time.Duration `yaml:"query_timeout"`
	SlowCacheTTL     time.Duration `yaml:"slow_cache_ttl"`
	SlowQueryTimeout time.Duration `yaml:"slow_query_timeout"`
}

// ExporterTLS holds TLS settings for the exporter's own HTTP endpoint.
type ExporterTLS struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// LogConfig controls log level and output format.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, text
}

// Load reads config from a YAML file (if path is non-empty) and then applies
// any environment variable overrides. Environment variables always win.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %q: %w", path, err)
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Mongo: MongoConfig{
			Host:       "localhost",
			Port:       27017,
			Database:   "itential",
			AuthSource: "admin",
		},
		Exporter: ExporterConfig{
			ListenAddress:    ":9477",
			MetricsPath:      "/metrics",
			CacheTTL:         60 * time.Second,
			QueryTimeout:     55 * time.Second,
			SlowCacheTTL:     5 * time.Minute,
			SlowQueryTimeout: 4 * time.Minute,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		ChangeStream: ChangeStreamConfig{
			Enabled:            false,
			InitialLoad:        true,
			InitialLoadTimeout: 2 * time.Minute,
		},
		Polling: PollingConfig{
			Enabled:      false,
			Interval:     60 * time.Second,
			QueryTimeout: 55 * time.Second,
		},
	}
}

// applyEnv overlays environment variable values onto cfg.
// Precedence: env vars > config file > defaults.
//
// The config file is entirely optional; the application can be configured
// exclusively via environment variables (useful for container deployments).
//
// Environment variables:
//
//	ITENTIAL_JOB_METRIC_MONGO_URI                       Full MongoDB connection string
//	ITENTIAL_JOB_METRIC_MONGO_HOST                      MongoDB host
//	ITENTIAL_JOB_METRIC_MONGO_PORT                      MongoDB port (integer)
//	ITENTIAL_JOB_METRIC_MONGO_USERNAME                  MongoDB username
//	ITENTIAL_JOB_METRIC_MONGO_PASSWORD                  MongoDB password
//	ITENTIAL_JOB_METRIC_MONGO_DATABASE                  MongoDB database name
//	ITENTIAL_JOB_METRIC_MONGO_AUTH_SOURCE               MongoDB auth source database
//	ITENTIAL_JOB_METRIC_MONGO_TLS_ENABLED               Enable TLS for MongoDB (true/false)
//	ITENTIAL_JOB_METRIC_MONGO_TLS_CA_FILE               Path to CA certificate file
//	ITENTIAL_JOB_METRIC_MONGO_TLS_INSECURE              Skip TLS verification (true/false)
//	ITENTIAL_JOB_METRIC_LISTEN_ADDRESS                  Exporter listen address (e.g. :9477)
//	ITENTIAL_JOB_METRIC_METRICS_PATH                    Metrics endpoint path (e.g. /metrics)
//	ITENTIAL_JOB_METRIC_TLS_ENABLED                     Enable TLS for exporter endpoint (true/false)
//	ITENTIAL_JOB_METRIC_TLS_CERT_FILE                   Path to TLS certificate file
//	ITENTIAL_JOB_METRIC_TLS_KEY_FILE                    Path to TLS key file
//	ITENTIAL_JOB_METRIC_CACHE_TTL                       Cache TTL duration (e.g. 30s)
//	ITENTIAL_JOB_METRIC_QUERY_TIMEOUT                   MongoDB query timeout (e.g. 25s)
//	ITENTIAL_JOB_METRIC_SLOW_CACHE_TTL                  Refresh interval for slow queries (e.g. 5m)
//	ITENTIAL_JOB_METRIC_SLOW_QUERY_TIMEOUT              Bootstrap query timeout for change stream startup (e.g. 4m)
//	ITENTIAL_JOB_METRIC_LOG_LEVEL                       Log level: debug, info, warn, error
//	ITENTIAL_JOB_METRIC_LOG_FORMAT                      Log format: json, text
//	ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED           Enable MongoDB change stream watcher (true/false)
//	ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD      Load active doc states on startup (true/false)
//	ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD_TIMEOUT  Deadline for the initial load queries (e.g. 2m)
func applyEnv(cfg *Config) {
	setStr := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	setBool := func(dst *bool, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = strings.EqualFold(v, "true") || v == "1"
		}
	}
	setDur := func(dst *time.Duration, key string) {
		if v := os.Getenv(key); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}

	setStr(&cfg.Mongo.URI, "ITENTIAL_JOB_METRIC_MONGO_URI")
	setStr(&cfg.Mongo.Host, "ITENTIAL_JOB_METRIC_MONGO_HOST")
	if v := os.Getenv("ITENTIAL_JOB_METRIC_MONGO_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Mongo.Port = p
		}
	}
	setStr(&cfg.Mongo.Username, "ITENTIAL_JOB_METRIC_MONGO_USERNAME")
	setStr(&cfg.Mongo.Password, "ITENTIAL_JOB_METRIC_MONGO_PASSWORD")
	setStr(&cfg.Mongo.Database, "ITENTIAL_JOB_METRIC_MONGO_DATABASE")
	setStr(&cfg.Mongo.AuthSource, "ITENTIAL_JOB_METRIC_MONGO_AUTH_SOURCE")
	setBool(&cfg.Mongo.TLS.Enabled, "ITENTIAL_JOB_METRIC_MONGO_TLS_ENABLED")
	setStr(&cfg.Mongo.TLS.CAFile, "ITENTIAL_JOB_METRIC_MONGO_TLS_CA_FILE")
	setBool(&cfg.Mongo.TLS.InsecureSkipVerify, "ITENTIAL_JOB_METRIC_MONGO_TLS_INSECURE")
	setStr(&cfg.Exporter.ListenAddress, "ITENTIAL_JOB_METRIC_LISTEN_ADDRESS")
	setStr(&cfg.Exporter.MetricsPath, "ITENTIAL_JOB_METRIC_METRICS_PATH")
	setBool(&cfg.Exporter.TLS.Enabled, "ITENTIAL_JOB_METRIC_TLS_ENABLED")
	setStr(&cfg.Exporter.TLS.CertFile, "ITENTIAL_JOB_METRIC_TLS_CERT_FILE")
	setStr(&cfg.Exporter.TLS.KeyFile, "ITENTIAL_JOB_METRIC_TLS_KEY_FILE")
	setDur(&cfg.Exporter.CacheTTL, "ITENTIAL_JOB_METRIC_CACHE_TTL")
	setDur(&cfg.Exporter.QueryTimeout, "ITENTIAL_JOB_METRIC_QUERY_TIMEOUT")
	setDur(&cfg.Exporter.SlowCacheTTL, "ITENTIAL_JOB_METRIC_SLOW_CACHE_TTL")
	setDur(&cfg.Exporter.SlowQueryTimeout, "ITENTIAL_JOB_METRIC_SLOW_QUERY_TIMEOUT")
	setStr(&cfg.Log.Level, "ITENTIAL_JOB_METRIC_LOG_LEVEL")
	setStr(&cfg.Log.Format, "ITENTIAL_JOB_METRIC_LOG_FORMAT")
	setBool(&cfg.ChangeStream.Enabled, "ITENTIAL_JOB_METRIC_CHANGE_STREAM_ENABLED")
	setBool(&cfg.ChangeStream.InitialLoad, "ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD")
	setDur(&cfg.ChangeStream.InitialLoadTimeout, "ITENTIAL_JOB_METRIC_CHANGE_STREAM_INITIAL_LOAD_TIMEOUT")
	setBool(&cfg.Polling.Enabled, "ITENTIAL_JOB_METRIC_POLLING_ENABLED")
	setDur(&cfg.Polling.Interval, "ITENTIAL_JOB_METRIC_POLLING_INTERVAL")
	setDur(&cfg.Polling.QueryTimeout, "ITENTIAL_JOB_METRIC_POLLING_QUERY_TIMEOUT")
}

func validate(cfg *Config) error {
	if cfg.Exporter.TLS.Enabled {
		if cfg.Exporter.TLS.CertFile == "" {
			return fmt.Errorf("exporter.tls.cert_file is required when TLS is enabled")
		}
		if cfg.Exporter.TLS.KeyFile == "" {
			return fmt.Errorf("exporter.tls.key_file is required when TLS is enabled")
		}
	}
	if cfg.Mongo.TLS.Enabled && cfg.Mongo.TLS.CAFile != "" {
		if _, err := os.Stat(cfg.Mongo.TLS.CAFile); err != nil {
			return fmt.Errorf("mongo TLS CA file %q not found: %w", cfg.Mongo.TLS.CAFile, err)
		}
	}
	if cfg.Exporter.QueryTimeout >= cfg.Exporter.CacheTTL {
		return fmt.Errorf(
			"query_timeout (%s) must be less than cache_ttl (%s)",
			cfg.Exporter.QueryTimeout, cfg.Exporter.CacheTTL,
		)
	}
	if cfg.Exporter.SlowQueryTimeout >= cfg.Exporter.SlowCacheTTL {
		return fmt.Errorf(
			"slow_query_timeout (%s) must be less than slow_cache_ttl (%s)",
			cfg.Exporter.SlowQueryTimeout, cfg.Exporter.SlowCacheTTL,
		)
	}
	if cfg.Polling.Enabled && cfg.Polling.QueryTimeout >= cfg.Polling.Interval {
		return fmt.Errorf(
			"polling.query_timeout (%s) must be less than polling.interval (%s)",
			cfg.Polling.QueryTimeout, cfg.Polling.Interval,
		)
	}
	return nil
}
