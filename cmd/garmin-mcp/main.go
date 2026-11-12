// Command garmin-mcp is an MCP server (stdio) that gives an AI assistant
// read-only access to the user's own Garmin Connect data through the
// official Garmin Connect Personal API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sasha-sup/garmin-mcp/internal/cache"
	"github.com/sasha-sup/garmin-mcp/internal/config"
	"github.com/sasha-sup/garmin-mcp/internal/garmin"
	"github.com/sasha-sup/garmin-mcp/internal/tools"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = ""

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "garmin-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	envFile := flag.String("env-file", "", "read GARMIN_* variables from this file (existing environment wins)")
	check := flag.Bool("check", false, "verify the API key and print a one-line summary, then exit")
	clearCache := flag.Bool("clear-cache", false, "delete cached Garmin data, then exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildVersion())
		return nil
	}
	cfg, err := loadConfig(*envFile)
	if err != nil {
		return err
	}
	if *clearCache {
		return runClearCache(cfg)
	}
	// stdout carries the MCP protocol; logs must go to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	svc, err := newService(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *check {
		return runCheck(ctx, svc, cfg)
	}
	return serve(ctx, svc, cfg, log)
}

func loadConfig(envFile string) (config.Config, error) {
	if envFile != "" {
		warn, err := config.LoadEnvFile(envFile)
		if err != nil {
			return config.Config{}, err
		}
		if warn != "" {
			fmt.Fprintln(os.Stderr, "garmin-mcp: warning:", warn)
		}
	}
	return config.Load()
}

func runClearCache(cfg config.Config) error {
	if cfg.CacheDir == "" {
		fmt.Println("cache is disabled (GARMIN_CACHE_DIR=off): nothing to clear")
		return nil
	}
	ch, err := cache.New(cfg.CacheDir)
	if err != nil {
		return err
	}
	if err := ch.Clear(); err != nil {
		return err
	}
	fmt.Println("cache cleared:", cfg.CacheDir)
	return nil
}

func newService(cfg config.Config, log *slog.Logger) (*garmin.Service, error) {
	var ch *cache.Cache
	if cfg.CacheDir != "" {
		var err error
		if ch, err = cache.New(cfg.CacheDir); err != nil {
			return nil, err
		}
	}
	base := garmin.BaseURLGlobal
	if cfg.Region == "cn" {
		base = garmin.BaseURLChina
	}
	client, err := garmin.New(cfg.APIKey,
		garmin.WithBaseURL(base),
		garmin.WithLogger(log),
		garmin.WithUserAgent("garmin-mcp/"+buildVersion()),
	)
	if err != nil {
		return nil, err
	}
	return garmin.NewService(client, ch, cfg.Location, log), nil
}

func serve(ctx context.Context, svc *garmin.Service, cfg config.Config, log *slog.Logger) error {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "garmin-mcp",
		Title:   "Garmin Connect",
		Version: buildVersion(),
	}, &mcp.ServerOptions{Instructions: tools.Instructions, Logger: log})
	tools.Register(srv, svc, tools.Options{AllowSensitive: cfg.AllowSensitive})

	if cfg.APIKey == "" {
		log.Warn("GARMIN_API_KEY is not set: tools are listed but every call will fail until it is configured")
	}
	log.Info("garmin-mcp started", "version", buildVersion(), "cache", cfg.CacheDir, "tz", cfg.Location.String())
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func runCheck(ctx context.Context, svc *garmin.Service, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	exp, err := svc.Client().CheckAuth(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("auth ok: access token valid until %s\n", exp.Local().Format(time.DateTime))
	p, err := svc.Profile(ctx)
	if err != nil {
		return fmt.Errorf("token works but reading the profile failed: %w", err)
	}
	fmt.Printf("profile ok: Garmin time zone %s, server time zone %s (%s)\n",
		p.TimeZone, cfg.Location, time.Now().In(cfg.Location).Format("MST -07:00"))
	today := svc.Today()
	m, err := svc.DailySummaries(ctx, today.AddDays(-6), today)
	if err != nil {
		return fmt.Errorf("reading daily summaries failed: %w", err)
	}
	fmt.Printf("data ok: %d of the last 7 days have a daily summary\n", len(m))
	if cfg.CacheDir == "" {
		fmt.Println("cache: disabled")
	} else {
		fmt.Println("cache:", cfg.CacheDir)
	}
	return nil
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
