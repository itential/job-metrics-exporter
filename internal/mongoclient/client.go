package mongoclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"github.com/itential/job-metrics-exporter/internal/config"
)

// New creates and validates a MongoDB client from cfg.
// If cfg.URI is set, it is used directly and all other individual fields
// (Host, Port, Username, Password, AuthSource) are ignored — TLS settings
// still apply on top of the URI.
func New(cfg *config.MongoConfig, logger *slog.Logger) (*mongo.Client, error) {
	opts := options.Client()

	if cfg.URI != "" {
		logger.Debug("using MongoDB URI connection string")
		opts.ApplyURI(cfg.URI)
	} else {
		uri := fmt.Sprintf("mongodb://%s:%d", cfg.Host, cfg.Port)
		logger.Debug("building MongoDB connection", "host", cfg.Host, "port", cfg.Port)
		opts.ApplyURI(uri)
		if cfg.Username != "" {
			opts.SetAuth(options.Credential{
				Username:   cfg.Username,
				Password:   cfg.Password,
				AuthSource: cfg.AuthSource,
			})
		}
	}

	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("invalid MongoDB client options: %w", err)
	}

	// TLS is applied regardless of whether a URI or individual fields are used.
	if cfg.TLS.Enabled {
		tlsCfg, err := buildTLSConfig(&cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("building TLS config: %w", err)
		}
		opts.SetTLSConfig(tlsCfg)
		logger.Debug("MongoDB TLS enabled", "ca_file", cfg.TLS.CAFile, "insecure", cfg.TLS.InsecureSkipVerify)
	}

	opts.SetConnectTimeout(15 * time.Second)
	opts.SetServerSelectionTimeout(15 * time.Second)
	opts.SetSocketTimeout(60 * time.Second)

	// Use secondary preferred so metric queries don't load the primary.
	opts.SetReadPreference(readpref.SecondaryPreferred())

	client, err := mongo.Connect(context.Background(), opts)
	if err != nil {
		return nil, fmt.Errorf("creating MongoDB client: %w", err)
	}

	// Verify connectivity before returning.
	pingCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.SecondaryPreferred()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("pinging MongoDB: %w", err)
	}

	return client, nil
}

func buildTLSConfig(cfg *config.MongoTLS) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec
		MinVersion:         tls.VersionTLS12,
	}

	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file %q: %w", cfg.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from %q", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}

	return tlsCfg, nil
}
