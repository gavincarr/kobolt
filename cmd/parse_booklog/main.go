package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/gavincarr/kobolt"
	"github.com/gavincarr/kobolt/internal/env"
	helpcolours "github.com/gavincarr/kong-help-colours"
	"github.com/lmittmann/tint"
)

type CLI struct {
	Outfile string `short:"o" placeholder:"FILE" help:"Write JSON output to FILE instead of stdout"`
	Verbose int    `short:"v" type:"counter" help:"Verbose output: -v logs a parse summary (info), -vv also logs skipped lines (debug)"`

	Input string `arg:"" optional:"" help:"Input booklog path (default ~/Books)"`
}

const defaultInput = "~/Books"

func main() {
	env.Load()

	var cli CLI
	kong.Parse(&cli,
		kong.Name("parse_booklog"),
		kong.Description("Parse the fixed-column ~/Books reading log into structured JSON."),
		kong.Help(helpcolours.Help),
		kong.ShortHelp(helpcolours.ShortHelp),
	)

	// Silent by default: skipped lines surface as warnings. -v adds the parse
	// summary (info); -vv itemises skipped lines (debug).
	level := slog.LevelWarn
	switch {
	case cli.Verbose >= 2:
		level = slog.LevelDebug
	case cli.Verbose >= 1:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: level})))

	if err := run(cli); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(cli CLI) error {
	inPath := cli.Input
	if inPath == "" {
		inPath = defaultInput
	}
	inPath = expandHome(inPath)

	content, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}

	books, skipped := kobolt.ParseBooklog(string(content))

	if len(skipped) > 0 {
		slog.Warn("skipped unparseable lines", "count", len(skipped))
		for _, s := range skipped {
			slog.Debug("skipped line", "line", s.LineNo, "text", s.Text)
		}
	}
	slog.Info("parsed booklog", "books", len(books), "skipped", len(skipped), "path", inPath)

	out, err := json.MarshalIndent(books, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	out = append(out, '\n')

	if cli.Outfile == "" {
		if _, err := os.Stdout.Write(out); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		return nil
	}
	if err := atomicWrite(cli.Outfile, out); err != nil {
		return fmt.Errorf("write %s: %w", cli.Outfile, err)
	}
	return nil
}

// expandHome replaces a leading ~ or ~/ with the user's home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// atomicWrite writes data to a temp file in the same directory as path, then
// renames it over path, so a failure never truncates an existing output file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".booklog-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
