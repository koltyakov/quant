package config

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/koltyakov/quant/internal/logx"
	"gopkg.in/yaml.v3"
)

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportSSE   Transport = "sse"
	TransportHTTP  Transport = "http"
	stateDirMode             = 0750
)

type Config struct {
	WatchDir            string        `yaml:"dir"`
	DBPath              string        `yaml:"db"`
	Transport           Transport     `yaml:"transport"`
	ListenAddr          string        `yaml:"listen"`
	EmbedURL            string        `yaml:"embed_url"`
	EmbedModel          string        `yaml:"embed_model"`
	PDFOCRLang          string        `yaml:"pdf_ocr_lang"`
	PDFOCRTimeout       time.Duration `yaml:"pdf_ocr_timeout"`
	ChunkSize           int           `yaml:"chunk_size"`
	ChunkOverlap        float64       `yaml:"chunk_overlap"`
	IndexWorkers        int           `yaml:"index_workers"`
	MaxVectorCandidates int           `yaml:"max_vector_candidates"`
	WatchEventBuffer    int           `yaml:"watch_event_buffer"`
	ConfigFile          string        `yaml:"-"`
}

func Default() *Config {
	return &Config{
		Transport:           TransportStdio,
		ListenAddr:          ":8080",
		EmbedURL:            "http://localhost:11434",
		EmbedModel:          "nomic-embed-text",
		PDFOCRLang:          "eng",
		PDFOCRTimeout:       2 * time.Minute,
		ChunkSize:           512,
		ChunkOverlap:        0.15,
		IndexWorkers:        defaultIndexWorkers(),
		MaxVectorCandidates: 20000,
		WatchEventBuffer:    256,
	}
}

func (c *Config) Validate() error {
	info, err := os.Stat(c.WatchDir)
	if err != nil {
		return fmt.Errorf("cannot access watch dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--dir must be a directory")
	}
	if c.DBPath != "" {
		dbDir := filepath.Dir(c.DBPath)
		if err := checkDirWritable(dbDir); err != nil {
			return fmt.Errorf("database directory %s is not writable: %w", dbDir, err)
		}
	}
	if c.Transport != TransportStdio && c.Transport != TransportSSE && c.Transport != TransportHTTP {
		return fmt.Errorf("invalid transport %q; must be stdio, sse, or http", c.Transport)
	}
	if err := validateEmbedURL(c.EmbedURL); err != nil {
		return err
	}
	if c.ChunkSize < 64 || c.ChunkSize > 8192 {
		return fmt.Errorf("chunk_size must be between 64 and 8192")
	}
	if c.ChunkOverlap < 0 || c.ChunkOverlap >= 1 {
		return fmt.Errorf("chunk_overlap must be between 0 and 0.99")
	}
	if c.IndexWorkers < 1 || c.IndexWorkers > 64 {
		return fmt.Errorf("index_workers must be between 1 and 64")
	}
	if c.MaxVectorCandidates < 0 {
		return fmt.Errorf("max_vector_candidates must be >= 0")
	}
	if c.WatchEventBuffer < 1 || c.WatchEventBuffer > 4096 {
		return fmt.Errorf("watch_event_buffer must be between 1 and 4096")
	}
	return nil
}

func validateEmbedURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("embed_url must be a valid URL: %w", err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("embed_url must be an absolute URL with scheme and host")
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("embed_url scheme must be http or https")
	}
}

func checkDirWritable(dir string) error {
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".quant-writability-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	//nolint:gosec // Temp file is created in the checked directory only to verify local writability.
	_ = os.Remove(name)
	return nil
}

func Parse() (*Config, error) {
	return ParseArgs(os.Args[1:])
}

func ParseArgs(args []string) (*Config, error) {
	flagSet, cfg := NewFlagSet("quant mcp")
	flagSet.SetOutput(io.Discard)

	if err := flagSet.Parse(args); err != nil {
		return nil, err
	}

	if flagSet.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flagSet.Args(), " "))
	}

	if cfg.ConfigFile != "" {
		if err := loadYAML(cfg, cfg.ConfigFile); err != nil {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	applyEnv(cfg)

	flagSet.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "dir":
			cfg.WatchDir = f.Value.String()
		case "db":
			cfg.DBPath = f.Value.String()
		case "transport":
			cfg.Transport = Transport(f.Value.String())
		case "listen":
			cfg.ListenAddr = f.Value.String()
		case "embed-url":
			cfg.EmbedURL = f.Value.String()
		case "embed-model":
			cfg.EmbedModel = f.Value.String()
		case "pdf-ocr-lang":
			cfg.PDFOCRLang = f.Value.String()
		case "pdf-ocr-timeout":
			cfg.PDFOCRTimeout = mustParseDurationFlag(f.Name, f.Value.String(), cfg.PDFOCRTimeout)
		case "chunk-size":
			cfg.ChunkSize = mustParseIntFlag(f.Name, f.Value.String(), cfg.ChunkSize)
		case "chunk-overlap":
			cfg.ChunkOverlap = mustParseFloatFlag(f.Name, f.Value.String(), cfg.ChunkOverlap)
		case "index-workers":
			cfg.IndexWorkers = mustParseIntFlag(f.Name, f.Value.String(), cfg.IndexWorkers)
		case "max-vector-candidates":
			cfg.MaxVectorCandidates = mustParseIntFlag(f.Name, f.Value.String(), cfg.MaxVectorCandidates)
		case "watch-event-buffer":
			cfg.WatchEventBuffer = mustParseIntFlag(f.Name, f.Value.String(), cfg.WatchEventBuffer)
		}
	})

	if cfg.WatchDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current directory: %w", err)
		}
		cfg.WatchDir = wd
	}

	watchDir, err := filepath.Abs(cfg.WatchDir)
	if err != nil {
		return nil, fmt.Errorf("resolving watch dir: %w", err)
	}
	cfg.WatchDir = filepath.Clean(watchDir)

	if cfg.DBPath == "" {
		cfg.DBPath = defaultDBPath(cfg.WatchDir)
	} else {
		dbPath, err := filepath.Abs(cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("resolving db path: %w", err)
		}
		cfg.DBPath = filepath.Clean(dbPath)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func NewFlagSet(name string) (*flag.FlagSet, *Config) {
	cfg := Default()
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)

	flagSet.StringVar(&cfg.WatchDir, "dir", "", "Directory to watch (default: current directory)")
	flagSet.StringVar(&cfg.DBPath, "db", "", "Path to SQLite database (default: <dir>/.index/quant.db)")
	flagSet.StringVar((*string)(&cfg.Transport), "transport", string(cfg.Transport), "MCP transport: stdio, sse, http")
	flagSet.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Listen address for SSE/HTTP transport")
	flagSet.StringVar(&cfg.EmbedURL, "embed-url", cfg.EmbedURL, "Embedding API URL")
	flagSet.StringVar(&cfg.EmbedModel, "embed-model", cfg.EmbedModel, "Embedding model")
	flagSet.StringVar(&cfg.PDFOCRLang, "pdf-ocr-lang", cfg.PDFOCRLang, "Tesseract language(s) for scanned PDF OCR, e.g. eng or rus+eng")
	flagSet.DurationVar(&cfg.PDFOCRTimeout, "pdf-ocr-timeout", cfg.PDFOCRTimeout, "Timeout for scanned PDF OCR fallback")
	flagSet.IntVar(&cfg.ChunkSize, "chunk-size", cfg.ChunkSize, "Chunk size in words")
	flagSet.Float64Var(&cfg.ChunkOverlap, "chunk-overlap", cfg.ChunkOverlap, "Chunk overlap fraction (0-1)")
	flagSet.IntVar(&cfg.IndexWorkers, "index-workers", cfg.IndexWorkers, "Number of parallel indexing workers")
	flagSet.IntVar(&cfg.MaxVectorCandidates, "max-vector-candidates", cfg.MaxVectorCandidates, "Maximum chunks eligible for brute-force vector fallback (0 disables it)")
	flagSet.IntVar(&cfg.WatchEventBuffer, "watch-event-buffer", cfg.WatchEventBuffer, "Watcher event channel buffer size")
	flagSet.StringVar(&cfg.ConfigFile, "config", "", "Path to YAML config file")

	return flagSet, cfg
}

func defaultDBPath(watchDir string) string {
	return filepath.Join(watchDir, ".index", "quant.db")
}

func loadYAML(cfg *Config, path string) error {
	//nolint:gosec // Configuration file path is explicitly provided by the user.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(path)

	type fileConfig struct {
		WatchDir            string    `yaml:"dir"`
		DBPath              string    `yaml:"db"`
		Transport           Transport `yaml:"transport"`
		ListenAddr          string    `yaml:"listen"`
		EmbedURL            string    `yaml:"embed_url"`
		EmbedModel          string    `yaml:"embed_model"`
		PDFOCRLang          string    `yaml:"pdf_ocr_lang"`
		PDFOCRTimeout       *string   `yaml:"pdf_ocr_timeout"`
		ChunkSize           *int      `yaml:"chunk_size"`
		ChunkOverlap        *float64  `yaml:"chunk_overlap"`
		IndexWorkers        *int      `yaml:"index_workers"`
		MaxVectorCandidates *int      `yaml:"max_vector_candidates"`
		WatchEventBuffer    *int      `yaml:"watch_event_buffer"`
	}

	var parsed fileConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}

	if parsed.WatchDir != "" {
		cfg.WatchDir = resolveConfigPath(baseDir, parsed.WatchDir)
	}
	if parsed.DBPath != "" {
		cfg.DBPath = resolveConfigPath(baseDir, parsed.DBPath)
	}
	if parsed.Transport != "" {
		cfg.Transport = parsed.Transport
	}
	if parsed.ListenAddr != "" {
		cfg.ListenAddr = parsed.ListenAddr
	}
	if parsed.EmbedURL != "" {
		cfg.EmbedURL = parsed.EmbedURL
	}
	if parsed.EmbedModel != "" {
		cfg.EmbedModel = parsed.EmbedModel
	}
	if parsed.PDFOCRLang != "" {
		cfg.PDFOCRLang = parsed.PDFOCRLang
	}
	if parsed.PDFOCRTimeout != nil {
		d, err := time.ParseDuration(*parsed.PDFOCRTimeout)
		if err != nil {
			logx.Warn("ignoring invalid pdf_ocr_timeout value", "value", *parsed.PDFOCRTimeout, "err", err)
		} else {
			cfg.PDFOCRTimeout = d
		}
	}
	if parsed.ChunkSize != nil {
		cfg.ChunkSize = *parsed.ChunkSize
	}
	if parsed.ChunkOverlap != nil {
		cfg.ChunkOverlap = *parsed.ChunkOverlap
	}
	if parsed.IndexWorkers != nil {
		cfg.IndexWorkers = *parsed.IndexWorkers
	}
	if parsed.MaxVectorCandidates != nil {
		cfg.MaxVectorCandidates = *parsed.MaxVectorCandidates
	}
	if parsed.WatchEventBuffer != nil {
		cfg.WatchEventBuffer = *parsed.WatchEventBuffer
	}

	return nil
}

func resolveConfigPath(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("QUANT_DIR"); v != "" {
		cfg.WatchDir = v
	}
	if v := os.Getenv("QUANT_DB"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("QUANT_TRANSPORT"); v != "" {
		cfg.Transport = Transport(strings.ToLower(v))
	}
	if v := os.Getenv("QUANT_LISTEN"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("QUANT_EMBED_URL"); v != "" {
		cfg.EmbedURL = v
	}
	if v := os.Getenv("QUANT_EMBED_MODEL"); v != "" {
		cfg.EmbedModel = v
	}
	if v := os.Getenv("QUANT_PDF_OCR_LANG"); v != "" {
		cfg.PDFOCRLang = v
	}
	if v := os.Getenv("QUANT_PDF_OCR_TIMEOUT"); v != "" {
		cfg.PDFOCRTimeout = mustParseDurationEnv("QUANT_PDF_OCR_TIMEOUT", v, cfg.PDFOCRTimeout)
	}
	if v := os.Getenv("QUANT_CHUNK_SIZE"); v != "" {
		cfg.ChunkSize = mustParseIntEnv("QUANT_CHUNK_SIZE", v, cfg.ChunkSize)
	}
	if v := os.Getenv("QUANT_CHUNK_OVERLAP"); v != "" {
		cfg.ChunkOverlap = mustParseFloatEnv("QUANT_CHUNK_OVERLAP", v, cfg.ChunkOverlap)
	}
	if v := os.Getenv("QUANT_INDEX_WORKERS"); v != "" {
		cfg.IndexWorkers = mustParseIntEnv("QUANT_INDEX_WORKERS", v, cfg.IndexWorkers)
	}
	if v := os.Getenv("QUANT_MAX_VECTOR_CANDIDATES"); v != "" {
		cfg.MaxVectorCandidates = mustParseIntEnv("QUANT_MAX_VECTOR_CANDIDATES", v, cfg.MaxVectorCandidates)
	}
	if v := os.Getenv("QUANT_WATCH_EVENT_BUFFER"); v != "" {
		cfg.WatchEventBuffer = mustParseIntEnv("QUANT_WATCH_EVENT_BUFFER", v, cfg.WatchEventBuffer)
	}
}

func defaultIndexWorkers() int {
	workers := runtime.GOMAXPROCS(0)
	if workers < 2 {
		return 2
	}
	if workers > 8 {
		return 8
	}
	return workers
}

func mustParseIntFlag(name, value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		logx.Warn("ignoring invalid flag value", "flag", "--"+name, "value", value, "err", err)
		return fallback
	}
	return parsed
}

func mustParseFloatFlag(name, value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		logx.Warn("ignoring invalid flag value", "flag", "--"+name, "value", value, "err", err)
		return fallback
	}
	return parsed
}

func mustParseDurationFlag(name, value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		logx.Warn("ignoring invalid flag value", "flag", "--"+name, "value", value, "err", err)
		return fallback
	}
	return parsed
}

func mustParseIntEnv(name, value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		logx.Warn("ignoring invalid env value", "name", name, "value", value, "err", err)
		return fallback
	}
	return parsed
}

func mustParseFloatEnv(name, value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		logx.Warn("ignoring invalid env value", "name", name, "value", value, "err", err)
		return fallback
	}
	return parsed
}

func mustParseDurationEnv(name, value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		logx.Warn("ignoring invalid env value", "name", name, "value", value, "err", err)
		return fallback
	}
	return parsed
}
