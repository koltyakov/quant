package config

import (
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/koltyakov/quant/internal/logx"
	"gopkg.in/yaml.v3"
)

// Transport identifies the MCP server transport protocol.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportSSE   Transport = "sse"
	TransportHTTP  Transport = "http"
	stateDirMode             = 0750
)

// Config holds all runtime configuration for the quant server.
type Config struct {
	WatchDir        string    `yaml:"dir"`
	DBPath          string    `yaml:"db"`
	Transport       Transport `yaml:"transport"`
	ListenAddr      string    `yaml:"listen"`
	MCPToken        string    `yaml:"mcp_token"`
	EmbedURL        string    `yaml:"embed_url"`
	EmbedModel      string    `yaml:"embed_model"`
	EmbedProvider   string    `yaml:"embed_provider"`
	EmbedAPIKey     string    `yaml:"embed_api_key"`
	LLMURL          string    `yaml:"llm_url"`
	LLMModel        string    `yaml:"llm_model"`
	LLMProvider     string    `yaml:"llm_provider"`
	LLMAPIKey       string    `yaml:"llm_api_key"`
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
	AllowInsecureModelHTTP  bool          `yaml:"allow_insecure_model_http"`

	pathMatcher     *PathMatcher
	pathMatcherOnce sync.Once
}

// Default returns a Config populated with sensible defaults.
func Default() *Config {
	return &Config{
		Transport:      TransportStdio,
		ListenAddr:     "127.0.0.1:8080",
		EmbedURL:       "http://localhost:11434",
		EmbedModel:     "nomic-embed-text",
		LLMURL:         "http://localhost:11434",
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

// Validate checks that all configuration values are within acceptable ranges and that required paths exist.
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
	if c.Transport == TransportSSE || c.Transport == TransportHTTP {
		if err := validateMCPListenSecurity(c.ListenAddr, c.MCPToken); err != nil {
			return err
		}
	}
	if err := validateModelURL("embed_url", c.EmbedURL, c.AllowInsecureModelHTTP); err != nil {
		return err
	}
	if err := validateEmbedProvider(c.EmbedProvider); err != nil {
		return err
	}
	if err := validateLLMProvider(c.LLMProvider); err != nil {
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
	if c.PDFOCRTimeout <= 0 {
		return fmt.Errorf("pdf_ocr_timeout must be greater than 0")
	}
	if c.MaxVectorCandidates < -1 {
		return fmt.Errorf("max_vector_candidates must be -1, 0, or greater")
	}
	if c.MaxConcurrentTools < 0 {
		return fmt.Errorf("max_concurrent_tools must be 0 or greater")
	}
	switch c.RerankerType {
	case "", "cross-encoder":
	default:
		return fmt.Errorf("invalid reranker %q; must be \"cross-encoder\"", c.RerankerType)
	}
	if c.RerankerType == "cross-encoder" && c.EffectiveRerankerModel() == "" {
		return fmt.Errorf("reranker cross-encoder requires --reranker-model or --llm-model")
	}
	if c.SummarizerEnabled && c.EffectiveSummarizerModel() == "" {
		return fmt.Errorf("summarizer requires --summarizer-model or --llm-model")
	}
	if c.RerankerType != "" || c.SummarizerEnabled {
		if err := validateModelURL("llm_url", c.LLMURL, c.AllowInsecureModelHTTP); err != nil {
			return err
		}
	}
	return nil
}

// PathMatcher returns the configured path matcher for include/exclude patterns.
// If no patterns are configured, returns nil (all paths included).
func (c *Config) PathMatcher() *PathMatcher {
	c.pathMatcherOnce.Do(func() {
		if len(c.IncludePatterns) == 0 && len(c.ExcludePatterns) == 0 {
			return
		}
		c.pathMatcher = &PathMatcher{
			IncludePatterns: c.IncludePatterns,
			ExcludePatterns: c.ExcludePatterns,
		}
	})
	return c.pathMatcher
}

func (c *Config) EffectiveRerankerModel() string {
	if c.RerankerModel != "" {
		return c.RerankerModel
	}
	return c.LLMModel
}

func (c *Config) EffectiveSummarizerModel() string {
	if c.SummarizerModel != "" {
		return c.SummarizerModel
	}
	return c.LLMModel
}

func validateHTTPURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL: %w", name, err)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL with scheme and host", name)
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%s scheme must be http or https", name)
	}
}

func validateModelURL(name, raw string, allowInsecure bool) error {
	if err := validateHTTPURL(name, raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) && !allowInsecure {
		return fmt.Errorf("%s must use https for non-loopback hosts (set --allow-insecure-model-http to override)", name)
	}
	return nil
}

func validateMCPListenSecurity(addr, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen must be a host:port address: %w", err)
	}
	if token != "" {
		if strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\t ") {
			return fmt.Errorf("mcp_token must not contain whitespace")
		}
		return nil
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("non-loopback MCP listen address %q requires --mcp-token or QUANT_MCP_TOKEN", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(host), "."))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateEmbedURL(raw string) error {
	return validateHTTPURL("embed_url", raw)
}

func validateEmbedProvider(provider string) error {
	switch provider {
	case "", "ollama", "openai":
		return nil
	default:
		return fmt.Errorf("invalid embed_provider %q; must be \"ollama\" or \"openai\"", provider)
	}
}

func validateLLMProvider(provider string) error {
	switch provider {
	case "", "ollama", "openai":
		return nil
	default:
		return fmt.Errorf("invalid llm_provider %q; must be \"ollama\" or \"openai\"", provider)
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

	cliSet := make(map[string]bool)
	flagSet.Visit(func(f *flag.Flag) {
		cliSet[f.Name] = true
	})

	if cfg.ConfigFile != "" {
		if err := loadYAML(cfg, cfg.ConfigFile, cliSet); err != nil {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	applyEnv(cfg, cliSet)

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
	flagSet.StringVar(&cfg.MCPToken, "mcp-token", cfg.MCPToken, "Bearer token required by SSE/HTTP MCP transports")
	flagSet.StringVar(&cfg.EmbedURL, "embed-url", cfg.EmbedURL, "Embedding API URL")
	flagSet.StringVar(&cfg.EmbedModel, "embed-model", cfg.EmbedModel, "Embedding model")
	flagSet.StringVar(&cfg.EmbedProvider, "embed-provider", cfg.EmbedProvider, "Embedding backend: ollama or openai (auto-detected from URL when not set)")
	flagSet.StringVar(&cfg.EmbedAPIKey, "embed-api-key", cfg.EmbedAPIKey, "API key for the embedding backend (OpenAI-compatible providers)")
	flagSet.DurationVar(&cfg.PDFOCRTimeout, "pdf-ocr-timeout", cfg.PDFOCRTimeout, "OCR timeout per PDF file")
	flagSet.IntVar(&cfg.MaxVectorCandidates, "max-vector-candidates", cfg.MaxVectorCandidates, "Maximum chunks for brute-force vector fallback (-1 unlimited, 0 disabled)")
	flagSet.IntVar(&cfg.MaxConcurrentTools, "max-concurrent-tools", cfg.MaxConcurrentTools, "Maximum concurrent MCP tool calls (0 uses default)")
	flagSet.StringVar(&cfg.LLMURL, "llm-url", cfg.LLMURL, "LLM API URL for reranking and summarization")
	flagSet.StringVar(&cfg.LLMModel, "llm-model", cfg.LLMModel, "Default LLM model for reranking and summarization")
	flagSet.StringVar(&cfg.LLMProvider, "llm-provider", cfg.LLMProvider, "LLM backend: ollama or openai (auto-detected from URL when not set)")
	flagSet.StringVar(&cfg.LLMAPIKey, "llm-api-key", cfg.LLMAPIKey, "API key for the LLM backend (OpenAI-compatible providers)")
	flagSet.StringVar(&cfg.PDFOCRLang, "pdf-ocr-lang", cfg.PDFOCRLang, "Tesseract language(s) for scanned PDF OCR, e.g. eng or rus+eng")
	flagSet.IntVar(&cfg.ChunkSize, "chunk-size", cfg.ChunkSize, "Chunk size in words")
	flagSet.Float64Var(&cfg.ChunkOverlap, "chunk-overlap", cfg.ChunkOverlap, "Chunk overlap fraction (0-1)")
	flagSet.IntVar(&cfg.IndexWorkers, "index-workers", cfg.IndexWorkers, "Number of parallel indexing workers")
	flagSet.IntVar(&cfg.EmbedBatchSize, "embed-batch-size", cfg.EmbedBatchSize, "Number of chunks to embed per batch")
	flagSet.StringVar(&cfg.RerankerType, "reranker", cfg.RerankerType, "Reranker type: cross-encoder (requires --reranker-model or --llm-model)")
	flagSet.StringVar(&cfg.RerankerModel, "reranker-model", cfg.RerankerModel, "Model for cross-encoder reranking (e.g. llama3.2)")
	flagSet.BoolVar(&cfg.SummarizerEnabled, "summarizer", cfg.SummarizerEnabled, "Enable LLM-powered chunk summarization at index time")
	flagSet.StringVar(&cfg.SummarizerModel, "summarizer-model", cfg.SummarizerModel, "Model for chunk summarization (default: same as llm model)")
	flagSet.BoolVar(&cfg.AllowInsecureModelHTTP, "allow-insecure-model-http", cfg.AllowInsecureModelHTTP, "Allow plaintext HTTP model endpoints on non-loopback hosts")
	flagSet.StringVar(&cfg.ConfigFile, "config", "", "Path to YAML config file")

	return flagSet, cfg
}

func defaultDBPath(watchDir string) string {
	return filepath.Join(watchDir, ".index", "quant.db")
}

func loadYAML(cfg *Config, path string, cliSet map[string]bool) error {
	//nolint:gosec // Configuration file path is explicitly provided by the user.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(path)

	type fileConfig struct {
		WatchDir               string    `yaml:"dir"`
		DBPath                 string    `yaml:"db"`
		Transport              Transport `yaml:"transport"`
		ListenAddr             string    `yaml:"listen"`
		MCPToken               string    `yaml:"mcp_token"`
		EmbedURL               string    `yaml:"embed_url"`
		EmbedModel             string    `yaml:"embed_model"`
		EmbedProvider          string    `yaml:"embed_provider"`
		EmbedAPIKey            string    `yaml:"embed_api_key"`
		LLMURL                 string    `yaml:"llm_url"`
		LLMModel               string    `yaml:"llm_model"`
		LLMProvider            string    `yaml:"llm_provider"`
		LLMAPIKey              string    `yaml:"llm_api_key"`
		EmbedBatchSize         *int      `yaml:"embed_batch_size"`
		PDFOCRLang             string    `yaml:"pdf_ocr_lang"`
		ChunkSize              *int      `yaml:"chunk_size"`
		ChunkOverlap           *float64  `yaml:"chunk_overlap"`
		IndexWorkers           *int      `yaml:"index_workers"`
		IncludePatterns        []string  `yaml:"include"`
		ExcludePatterns        []string  `yaml:"exclude"`
		RerankerType           string    `yaml:"reranker"`
		RerankerModel          string    `yaml:"reranker_model"`
		Summarizer             *bool     `yaml:"summarizer"`
		SummarizerModel        string    `yaml:"summarizer_model"`
		AllowInsecureModelHTTP *bool     `yaml:"allow_insecure_model_http"`
	}

	var parsed fileConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}

	if parsed.WatchDir != "" && !cliSet["dir"] {
		cfg.WatchDir = resolveConfigPath(baseDir, parsed.WatchDir)
	}
	if parsed.DBPath != "" && !cliSet["db"] {
		cfg.DBPath = resolveConfigPath(baseDir, parsed.DBPath)
	}
	if parsed.Transport != "" && !cliSet["transport"] {
		cfg.Transport = parsed.Transport
	}
	if parsed.ListenAddr != "" && !cliSet["listen"] {
		cfg.ListenAddr = parsed.ListenAddr
	}
	if parsed.MCPToken != "" && !cliSet["mcp-token"] {
		cfg.MCPToken = parsed.MCPToken
	}
	if parsed.EmbedURL != "" && !cliSet["embed-url"] {
		cfg.EmbedURL = parsed.EmbedURL
	}
	if parsed.EmbedModel != "" && !cliSet["embed-model"] {
		cfg.EmbedModel = parsed.EmbedModel
	}
	if parsed.EmbedProvider != "" && !cliSet["embed-provider"] {
		cfg.EmbedProvider = parsed.EmbedProvider
	}
	if parsed.EmbedAPIKey != "" && !cliSet["embed-api-key"] {
		cfg.EmbedAPIKey = parsed.EmbedAPIKey
	}
	if parsed.LLMURL != "" && !cliSet["llm-url"] {
		cfg.LLMURL = parsed.LLMURL
	}
	if parsed.LLMModel != "" && !cliSet["llm-model"] {
		cfg.LLMModel = parsed.LLMModel
	}
	if parsed.LLMProvider != "" && !cliSet["llm-provider"] {
		cfg.LLMProvider = parsed.LLMProvider
	}
	if parsed.LLMAPIKey != "" && !cliSet["llm-api-key"] {
		cfg.LLMAPIKey = parsed.LLMAPIKey
	}
	if parsed.PDFOCRLang != "" && !cliSet["pdf-ocr-lang"] {
		cfg.PDFOCRLang = parsed.PDFOCRLang
	}
	if parsed.ChunkSize != nil && !cliSet["chunk-size"] {
		cfg.ChunkSize = *parsed.ChunkSize
	}
	if parsed.ChunkOverlap != nil && !cliSet["chunk-overlap"] {
		cfg.ChunkOverlap = *parsed.ChunkOverlap
	}
	if parsed.IndexWorkers != nil && !cliSet["index-workers"] {
		cfg.IndexWorkers = *parsed.IndexWorkers
	}
	if parsed.EmbedBatchSize != nil && !cliSet["embed-batch-size"] {
		cfg.EmbedBatchSize = *parsed.EmbedBatchSize
	}
	if len(parsed.IncludePatterns) > 0 {
		cfg.IncludePatterns = parsed.IncludePatterns
	}
	if len(parsed.ExcludePatterns) > 0 {
		cfg.ExcludePatterns = parsed.ExcludePatterns
	}
	if parsed.RerankerType != "" && !cliSet["reranker"] {
		cfg.RerankerType = parsed.RerankerType
	}
	if parsed.RerankerModel != "" && !cliSet["reranker-model"] {
		cfg.RerankerModel = parsed.RerankerModel
	}
	if parsed.Summarizer != nil && !cliSet["summarizer"] {
		cfg.SummarizerEnabled = *parsed.Summarizer
	}
	if parsed.SummarizerModel != "" && !cliSet["summarizer-model"] {
		cfg.SummarizerModel = parsed.SummarizerModel
	}
	if parsed.AllowInsecureModelHTTP != nil && !cliSet["allow-insecure-model-http"] {
		cfg.AllowInsecureModelHTTP = *parsed.AllowInsecureModelHTTP
	}

	return nil
}

func resolveConfigPath(baseDir, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

// applyEnv applies QUANT_* environment variables to the config.
// Environment variables override YAML config values but never override
// flags explicitly passed on the command line (precedence: CLI > env > file).
func applyEnv(cfg *Config, cliSet map[string]bool) {
	set := func(flagName string) bool { return cliSet[flagName] }
	if v := os.Getenv("QUANT_DIR"); v != "" && !set("dir") {
		cfg.WatchDir = v
	}
	if v := os.Getenv("QUANT_DB"); v != "" && !set("db") {
		cfg.DBPath = v
	}
	if v := os.Getenv("QUANT_TRANSPORT"); v != "" && !set("transport") {
		cfg.Transport = Transport(strings.ToLower(v))
	}
	if v := os.Getenv("QUANT_LISTEN"); v != "" && !set("listen") {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("QUANT_MCP_TOKEN"); v != "" && !set("mcp-token") {
		cfg.MCPToken = v
	}
	if v := os.Getenv("QUANT_EMBED_URL"); v != "" && !set("embed-url") {
		cfg.EmbedURL = v
	}
	if v := os.Getenv("QUANT_EMBED_MODEL"); v != "" && !set("embed-model") {
		cfg.EmbedModel = v
	}
	if v := os.Getenv("QUANT_EMBED_PROVIDER"); v != "" && !set("embed-provider") {
		cfg.EmbedProvider = v
	}
	if v := os.Getenv("QUANT_EMBED_API_KEY"); v != "" && !set("embed-api-key") {
		cfg.EmbedAPIKey = v
	}
	if v := os.Getenv("QUANT_LLM_URL"); v != "" && !set("llm-url") {
		cfg.LLMURL = v
	}
	if v := os.Getenv("QUANT_LLM_MODEL"); v != "" && !set("llm-model") {
		cfg.LLMModel = v
	}
	if v := os.Getenv("QUANT_LLM_PROVIDER"); v != "" && !set("llm-provider") {
		cfg.LLMProvider = v
	}
	if v := os.Getenv("QUANT_LLM_API_KEY"); v != "" && !set("llm-api-key") {
		cfg.LLMAPIKey = v
	}
	if v := os.Getenv("QUANT_PDF_OCR_LANG"); v != "" && !set("pdf-ocr-lang") {
		cfg.PDFOCRLang = v
	}
	if v := os.Getenv("QUANT_CHUNK_SIZE"); v != "" && !set("chunk-size") {
		cfg.ChunkSize = mustParseIntEnv("QUANT_CHUNK_SIZE", v, cfg.ChunkSize)
	}
	if v := os.Getenv("QUANT_CHUNK_OVERLAP"); v != "" && !set("chunk-overlap") {
		cfg.ChunkOverlap = mustParseFloatEnv("QUANT_CHUNK_OVERLAP", v, cfg.ChunkOverlap)
	}
	if v := os.Getenv("QUANT_INDEX_WORKERS"); v != "" && !set("index-workers") {
		cfg.IndexWorkers = mustParseIntEnv("QUANT_INDEX_WORKERS", v, cfg.IndexWorkers)
	}
	if v := os.Getenv("QUANT_EMBED_BATCH_SIZE"); v != "" && !set("embed-batch-size") {
		cfg.EmbedBatchSize = mustParseIntEnv("QUANT_EMBED_BATCH_SIZE", v, cfg.EmbedBatchSize)
	}
	if v := os.Getenv("QUANT_RERANKER"); v != "" && !set("reranker") {
		cfg.RerankerType = v
	}
	if v := os.Getenv("QUANT_RERANKER_MODEL"); v != "" && !set("reranker-model") {
		cfg.RerankerModel = v
	}
	if v := os.Getenv("QUANT_SUMMARIZER"); v != "" && !set("summarizer") {
		cfg.SummarizerEnabled = v == "true" || v == "1" || v == "yes"
	}
	if v := os.Getenv("QUANT_SUMMARIZER_MODEL"); v != "" && !set("summarizer-model") {
		cfg.SummarizerModel = v
	}
	if v := os.Getenv("QUANT_ALLOW_INSECURE_MODEL_HTTP"); v != "" && !set("allow-insecure-model-http") {
		cfg.AllowInsecureModelHTTP = v == "true" || v == "1" || v == "yes"
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
