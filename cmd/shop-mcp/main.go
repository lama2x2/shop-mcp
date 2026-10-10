// Command shop-mcp is an MCP server (stdio) that gives an AI agent
// read-only access to the shop SQLite database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lama2x2/shop-mcp/internal/store"
	"github.com/lama2x2/shop-mcp/internal/tools"
)

// version is set with -ldflags "-X main.version=…".
var version = "dev"

const (
	envDBPath = "SHOP_DB_PATH"
	dbName    = "shop.db"
)

type config struct {
	dbPath       string
	queryTimeout time.Duration
	logLevel     slog.Level
	showVersion  bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shop-mcp: %v\n", err)
		return 2
	}
	if cfg.showVersion {
		_, _ = fmt.Fprintln(stderr, "shop-mcp", version)
		return 0
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.logLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	path, err := resolveDBPath(cfg.dbPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shop-mcp: %v\n", err)
		return 1
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := store.Open(openCtx, path)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shop-mcp: cannot open database at %q: %v\n", path, err)
		return 1
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Warn("close database", "error", err)
		}
	}()

	server := mcp.NewServer(
		&mcp.Implementation{Name: "shop-mcp", Version: version},
		&mcp.ServerOptions{Instructions: tools.Instructions},
	)
	tools.Register(server, tools.Deps{Store: st, Timeout: cfg.queryTimeout, Logger: logger})

	logger.Info("serving on stdio", "db", path, "version", version, "query_timeout", cfg.queryTimeout)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !isShutdown(ctx, err) {
		logger.Error("server stopped", "error", err)
		return 1
	}
	logger.Info("shutdown")
	return 0
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	var (
		cfg      config
		logLevel string
	)
	fs := flag.NewFlagSet("shop-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.dbPath, "db", "", "path to shop.db (default: $"+envDBPath+", ./"+dbName+", or "+dbName+" next to the executable)")
	fs.DurationVar(&cfg.queryTimeout, "query-timeout", tools.DefaultTimeout, "time limit for each tool call")
	fs.StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error (logs go to stderr)")
	fs.BoolVar(&cfg.showVersion, "version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if cfg.queryTimeout <= 0 {
		return config{}, fmt.Errorf("--query-timeout must be positive, got %s", cfg.queryTimeout)
	}
	if err := cfg.logLevel.UnmarshalText([]byte(logLevel)); err != nil {
		return config{}, fmt.Errorf("invalid --log-level %q: use debug, info, warn or error", logLevel)
	}
	return cfg, nil
}

// resolveDBPath applies the precedence --db > $SHOP_DB_PATH > ./shop.db >
// shop.db next to the executable, and checks that the file exists.
func resolveDBPath(flagPath string) (string, error) {
	var candidates []string
	switch {
	case flagPath != "":
		candidates = []string{flagPath}
	case os.Getenv(envDBPath) != "":
		candidates = []string{os.Getenv(envDBPath)}
	default:
		candidates = []string{dbName}
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), dbName))
		}
	}
	for _, c := range candidates {
		info, err := os.Stat(c) //nolint:gosec // operator-supplied path is the point of --db
		if err != nil || info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", c, err)
		}
		return abs, nil
	}
	return "", fmt.Errorf("database not found at %q (set --db or %s)", candidates[0], envDBPath)
}

// isShutdown reports whether err is the normal end of a session: a signal
// or the client closing stdin.
func isShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed)
}
