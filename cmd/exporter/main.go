package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/itential/job-metrics-exporter/internal/collector"
	"github.com/itential/job-metrics-exporter/internal/config"
	"github.com/itential/job-metrics-exporter/internal/mongoclient"
	"github.com/itential/job-metrics-exporter/internal/queries"
	"github.com/itential/job-metrics-exporter/internal/watcher"
)

// Set via -ldflags at build time: -X main.version=v1.0.0
var version = "dev"

func main() {
	var (
		configFile    = flag.String("config", "", "Path to YAML config file")
		database      = flag.String("database", "", "Override MongoDB database name")
		listenAddress = flag.String("listen-address", "", "Override listen address (e.g. :9477)")
		showVersion   = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("itential-job-metrics-exporter %s\n", version)
		os.Exit(0)
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	// CLI flags take highest precedence
	if *database != "" {
		cfg.Mongo.Database = *database
	}
	if *listenAddress != "" {
		cfg.Exporter.ListenAddress = *listenAddress
	}

	logger := buildLogger(cfg.Log)
	logger.Info("starting itential-job-metrics-exporter",
		"version", version,
		"database", cfg.Mongo.Database,
		"listen", cfg.Exporter.ListenAddress,
		"change_stream", cfg.ChangeStream.Enabled,
	)

	mongoClient, err := mongoclient.New(&cfg.Mongo, logger)
	if err != nil {
		logger.Error("failed to connect to MongoDB", "err", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mongoClient.Disconnect(ctx); err != nil {
			logger.Warn("error disconnecting from MongoDB", "err", err)
		}
	}()
	logger.Info("connected to MongoDB", "database", cfg.Mongo.Database)

	db := mongoClient.Database(cfg.Mongo.Database)

	coll := collector.New(version, func(ctx context.Context) error {
		return mongoClient.Ping(ctx, nil)
	}, logger)

	reg := prometheus.NewRegistry()
	reg.MustRegister(coll)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cfg.ChangeStream.Enabled {
		// Bootstrap: run JobsByStatus + TasksByStatus once to seed the initial
		// gauge values before the change stream opens. The change stream then
		// maintains those counts via ±1 deltas — no recurring full scans needed.
		runner := queries.NewRunner(db, cfg.Queries.TaskStatusIndex)
		logger.Info("bootstrapping status gauges (one-time poll)")
		bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, cfg.Exporter.SlowQueryTimeout)
		pollOnce(bootstrapCtx, runner, coll, cfg.Exporter.SlowQueryTimeout, logger)
		bootstrapCancel()

		w := watcher.New(db, coll, coll, cfg.ChangeStream, logger)
		go w.Start(ctx)
	} else if cfg.Polling.Enabled {
		// Change stream is off: fall back to periodic polling for status gauges.
		runner := queries.NewRunner(db, cfg.Queries.TaskStatusIndex)
		logger.Info("starting background poller",
			"interval", cfg.Polling.Interval,
			"query_timeout", cfg.Polling.QueryTimeout,
			"read_preference", "secondaryPreferred",
		)
		go runPoller(ctx, runner, coll, cfg.Polling, logger)
	}

	mux := http.NewServeMux()
	mux.Handle(cfg.Exporter.MetricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		Registry:          reg,
		EnableOpenMetrics: true,
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Itential Job Metric Exporter</title></head>
<body><h1>Itential Job Metric Exporter</h1>
<p>Version: %s</p>
<p><a href="%s">Metrics</a></p>
<p><a href="/healthz">Health</a></p>
</body></html>`, version, cfg.Exporter.MetricsPath)
	})

	srv := &http.Server{
		Addr:         cfg.Exporter.ListenAddress,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-quit
		logger.Info("received shutdown signal", "signal", sig)
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("server shutdown error", "err", err)
		}
	}()

	if cfg.Exporter.TLS.Enabled {
		logger.Info("listening with TLS", "address", cfg.Exporter.ListenAddress)
		if err := srv.ListenAndServeTLS(
			cfg.Exporter.TLS.CertFile,
			cfg.Exporter.TLS.KeyFile,
		); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	} else {
		logger.Info("listening", "address", cfg.Exporter.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}

	logger.Info("exporter stopped")
}

func runPoller(ctx context.Context, runner *queries.Runner, coll *collector.Collector, cfg config.PollingConfig, logger *slog.Logger) {
	pollOnce(ctx, runner, coll, cfg.QueryTimeout, logger)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			pollOnce(ctx, runner, coll, cfg.QueryTimeout, logger)
		case <-ctx.Done():
			return
		}
	}
}

func pollOnce(ctx context.Context, runner *queries.Runner, coll *collector.Collector, timeout time.Duration, logger *slog.Logger) {
	jobCtx, jobCancel := context.WithTimeout(ctx, timeout)
	defer jobCancel()

	start := time.Now()
	jobCounts, err := runner.JobsByStatus(jobCtx)
	jobDur := time.Since(start)
	if err != nil {
		logger.Warn("job status query failed", "err", err, "duration_ms", jobDur.Milliseconds())
		coll.RecordPollError("jobs")
	} else {
		m := make(map[string]int64, len(jobCounts))
		for _, sc := range jobCounts {
			m[sc.Status] = sc.Count
		}
		coll.SetJobStatusCounts(m)
		logger.Debug("job status counts updated", "statuses", len(m), "duration_ms", jobDur.Milliseconds())
	}

	taskCtx, taskCancel := context.WithTimeout(ctx, timeout)
	defer taskCancel()

	start = time.Now()
	taskCounts, err := runner.TasksByStatus(taskCtx)
	taskDur := time.Since(start)
	if err != nil {
		logger.Warn("task status query failed", "err", err, "duration_ms", taskDur.Milliseconds())
		coll.RecordPollError("tasks")
	} else {
		m := make(map[string]int64, len(taskCounts))
		for _, sc := range taskCounts {
			m[sc.Status] = sc.Count
		}
		coll.SetTaskStatusCounts(m)
		logger.Debug("task status counts updated", "statuses", len(m), "duration_ms", taskDur.Milliseconds())
	}
}

func buildLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
