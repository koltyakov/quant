package config

import (
	"flag"
	"fmt"
	"io"
	"math"
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
	WatchDir        string    `yaml:"dir"`
	DBPath          string    `yaml:"db"`
	Transport       Transport `yaml:"transport"`
	ListenAddr      string    `yaml:"listen"`
	EmbedURL        string    `yaml:"embed_url"`
	EmbedModel      string    `yaml:"embed_model"`
	EmbedProvider   string    `yaml:"embed_provider"`
	EmbedAPIKey     string    `yaml:"embed_api_key"`
	EmbedBatchSize  int       `yaml:"embed_batch_size"`
	PDFOCRLang      string    `yaml:"pdf_ocr_lang"`
	ChunkSize       int       `yaml:"chunk_size"`
	ChunkOverlap    float64   `yaml:"chunk_overlap"`
	IndexWorkers    int       `yaml:"index_workers"`
	IncludePatterns []string  `yaml:"include"`
	ExcludePatterns []string  `yaml:"exclude"`
	ConfigFile      string    `yaml:"-"`

	PDFOCRTimeout           time.Duration `yaml:"-"`
	MaxVectorCandidates     int           `yaml:"-"`
	MaxConcurrentTools      int           `yaml:"-"`
	KeywordWeight           float64       `yaml:"-"`
	VectorWeight            float64       `yaml:"-"`
	WatchEventBuffer        int           `yaml:"-"`
	HNSWM                   int           `yaml:"-"`
	HNSWEfSearch            int           `yaml:"-"`
	HNSWReoptimizeThreshold float64       `yaml:"-"`
	ProxyAddr               string        `yaml:"-"`
	NoLock                  bool          `yaml:"-"`
	RerankerType            string        `yaml:"-"`
	RerankerModel           string        `yaml:"-"`
	SummarizerEnabled       bool          `yaml:"-"`
	SummarizerModel         string        `yaml:"-"`

	pathMatcher *PathMatcher
}

func Default() *Config {
	return &Config{
		Transport:      TransportStdio,
		ListenAddr:     ":8080",
		EmbedURL:       "http://localhost:11434",
		EmbedModel:     "nomic-embed-text",
		EmbedBatchSize: 16,
		PDFOCRLang:     "eng",
		PDFOCRTimeout:  2 * time.Minute,
		ChunkSize:      512,
		ChunkOverlap:   0.15,
		IndexWorkers:   defaultIndexWorkers(),

		MaxVectorCandidates:     defaultMaxVectorCandidates(),
		MaxConcurrentTools:      defaultMaxConcurrentTools(),
		KeywordWeight:           0,
		VectorWeight:            0,
		WatchEventBuffer:        256,
		HNSWM:                   defaultHNSWM(),
		HNSWEfSearch:            defaultHNSWEfSearch(),
		HNSWReoptimizeThreshold: 0.2,
		RerankerType:            "",
		RerankerModel:           "",
		SummarizerEnabled:       false,
		SummarizerModel:         "",
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
	if c.EmbedBatchSize < 1 || c.EmbedBatchSize > 128 {
		return fmt.Errorf("embed_batch_size must be between 1 and 128")
	}
	return nil
}

// PathMatcher returns the configured path matcher for include/exclude patterns.
// If no patterns are configured, returns nil (all paths included).
func (c *Config) PathMatcher() *PathMatcher {
	if c.pathMatcher != nil {
		return c.pathMatcher
	}
	if len(c.IncludePatterns) == 0 && len(c.ExcludePatterns) == 0 {
		return nil
	}
	c.pathMatcher = &PathMatcher{
		IncludePatterns: c.IncludePatterns,
		ExcludePatterns: c.ExcludePatterns,
	}
	return c.pathMatcher
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
		case "embed-provider":
			cfg.EmbedProvider = f.Value.String()
		case "embed-api-key":
			cfg.EmbedAPIKey = f.Value.String()
		case "pdf-ocr-lang":
			cfg.PDFOCRLang = f.Value.String()
		case "chunk-size":
			cfg.ChunkSize = mustParseIntFlag(f.Name, f.Value.String(), cfg.ChunkSize)
		case "chunk-overlap":
			cfg.ChunkOverlap = mustParseFloatFlag(f.Name, f.Value.String(), cfg.ChunkOverlap)
		case "index-workers":
			cfg.IndexWorkers = mustParseIntFlag(f.Name, f.Value.String(), cfg.IndexWorkers)
		case "embed-batch-size":
			cfg.EmbedBatchSize = mustParseIntFlag(f.Name, f.Value.String(), cfg.EmbedBatchSize)
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
	flagSet.StringVar(&cfg.EmbedProvider, "embed-provider", cfg.EmbedProvider, "Embedding backend: ollama or openai (auto-detected from URL when not set)")
	flagSet.StringVar(&cfg.EmbedAPIKey, "embed-api-key", cfg.EmbedAPIKey, "API key for the embedding backend (OpenAI-compatible providers)")
	flagSet.StringVar(&cfg.PDFOCRLang, "pdf-ocr-lang", cfg.PDFOCRLang, "Tesseract language(s) for scanned PDF OCR, e.g. eng or rus+eng")
	flagSet.IntVar(&cfg.ChunkSize, "chunk-size", cfg.ChunkSize, "Chunk size in words")
	flagSet.Float64Var(&cfg.ChunkOverlap, "chunk-overlap", cfg.ChunkOverlap, "Chunk overlap fraction (0-1)")
	flagSet.IntVar(&cfg.IndexWorkers, "index-workers", cfg.IndexWorkers, "Number of parallel indexing workers")
	flagSet.IntVar(&cfg.EmbedBatchSize, "embed-batch-size", cfg.EmbedBatchSize, "Number of chunks to embed per batch")
	flagSet.StringVar(&cfg.RerankerType, "reranker", cfg.RerankerType, "Reranker type: cross-encoder (requires --reranker-model)")
	flagSet.StringVar(&cfg.RerankerModel, "reranker-model", cfg.RerankerModel, "Model for cross-encoder reranking (e.g. llama3.2)")
	flagSet.BoolVar(&cfg.SummarizerEnabled, "summarizer", cfg.SummarizerEnabled, "Enable LLM-powered chunk summarization at index time")
	flagSet.StringVar(&cfg.SummarizerModel, "summarizer-model", cfg.SummarizerModel, "Model for chunk summarization (default: same as embed model)")
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
		WatchDir        string    `yaml:"dir"`
		DBPath          string    `yaml:"db"`
		Transport       Transport `yaml:"transport"`
		ListenAddr      string    `yaml:"listen"`
		EmbedURL        string    `yaml:"embed_url"`
		EmbedModel      string    `yaml:"embed_model"`
		EmbedProvider   string    `yaml:"embed_provider"`
		EmbedAPIKey     string    `yaml:"embed_api_key"`
		EmbedBatchSize  *int      `yaml:"embed_batch_size"`
		PDFOCRLang      string    `yaml:"pdf_ocr_lang"`
		ChunkSize       *int      `yaml:"chunk_size"`
		ChunkOverlap    *float64  `yaml:"chunk_overlap"`
		IndexWorkers    *int      `yaml:"index_workers"`
		IncludePatterns []string  `yaml:"include"`
		ExcludePatterns []string  `yaml:"exclude"`
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
	if parsed.EmbedProvider != "" {
		cfg.EmbedProvider = parsed.EmbedProvider
	}
	if parsed.EmbedAPIKey != "" {
		cfg.EmbedAPIKey = parsed.EmbedAPIKey
	}
	if parsed.PDFOCRLang != "" {
		cfg.PDFOCRLang = parsed.PDFOCRLang
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
	if parsed.EmbedBatchSize != nil {
		cfg.EmbedBatchSize = *parsed.EmbedBatchSize
	}
	if len(parsed.IncludePatterns) > 0 {
		cfg.IncludePatterns = parsed.IncludePatterns
	}
	if len(parsed.ExcludePatterns) > 0 {
		cfg.ExcludePatterns = parsed.ExcludePatterns
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
	if v := os.Getenv("QUANT_EMBED_PROVIDER"); v != "" {
		cfg.EmbedProvider = v
	}
	if v := os.Getenv("QUANT_EMBED_API_KEY"); v != "" {
		cfg.EmbedAPIKey = v
	}
	if v := os.Getenv("QUANT_PDF_OCR_LANG"); v != "" {
		cfg.PDFOCRLang = v
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
	if v := os.Getenv("QUANT_EMBED_BATCH_SIZE"); v != "" {
		cfg.EmbedBatchSize = mustParseIntEnv("QUANT_EMBED_BATCH_SIZE", v, cfg.EmbedBatchSize)
	}
	if v := os.Getenv("QUANT_RERANKER"); v != "" {
		cfg.RerankerType = v
	}
	if v := os.Getenv("QUANT_RERANKER_MODEL"); v != "" {
		cfg.RerankerModel = v
	}
	if v := os.Getenv("QUANT_SUMMARIZER"); v != "" {
		cfg.SummarizerEnabled = v == "true" || v == "1" || v == "yes"
	}
	if v := os.Getenv("QUANT_SUMMARIZER_MODEL"); v != "" {
		cfg.SummarizerModel = v
	}
}

func defaultIndexWorkers() int {
	cpus := runtime.GOMAXPROCS(0)
	if cpus <= 1 {
		return 1
	}
	if cpus <= 4 {
		return 2
	}
	return min(cpus/2, 8)
}

func defaultMaxConcurrentTools() int {
	cpus := runtime.GOMAXPROCS(0)
	if cpus <= 2 {
		return 2
	}
	return min(cpus/2, 8)
}

func defaultMaxVectorCandidates() int {
	return 20000
}

func defaultHNSWM() int {
	return 16
}

func defaultHNSWEfSearch() int {
	return 100
}

// DefaultMemoryLimit returns a suggested Go runtime memory soft limit
// based on the total physical memory of the system. Returns 0 if the
// system memory cannot be determined.
func DefaultMemoryLimit() int64 {
	const (
		minLimit int64 = 512 << 20
		maxLimit int64 = 4 << 30
		fraction       = 4
	)
	total := totalMemory()
	if total == 0 {
		return 0
	}
	if total > math.MaxInt64 {
		total = math.MaxInt64
	}
	limit := int64(total) / fraction
	return max(min(limit, maxLimit), minLimit)
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
