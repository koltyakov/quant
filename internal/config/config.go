package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportSSE   Transport = "sse"
	TransportHTTP  Transport = "http"
)

type Config struct {
	WatchDir     string    `yaml:"dir"`
	DBPath       string    `yaml:"db"`
	Transport    Transport `yaml:"transport"`
	ListenAddr   string    `yaml:"listen"`
	EmbedURL     string    `yaml:"embed_url"`
	EmbedModel   string    `yaml:"embed_model"`
	ChunkSize    int       `yaml:"chunk_size"`
	ChunkOverlap float64   `yaml:"chunk_overlap"`
	ConfigFile   string    `yaml:"-"`
}

func Default() *Config {
	return &Config{
		Transport:    TransportStdio,
		ListenAddr:   ":8080",
		EmbedURL:     "http://localhost:11434",
		EmbedModel:   "nomic-embed-text",
		ChunkSize:    512,
		ChunkOverlap: 0.15,
	}
}

func (c *Config) Validate() error {
	if c.WatchDir == "" {
		return fmt.Errorf("--dir is required")
	}
	info, err := os.Stat(c.WatchDir)
	if err != nil {
		return fmt.Errorf("cannot access watch dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--dir must be a directory")
	}
	if c.Transport != TransportStdio && c.Transport != TransportSSE && c.Transport != TransportHTTP {
		return fmt.Errorf("invalid transport %q; must be stdio, sse, or http", c.Transport)
	}
	if c.ChunkSize < 64 || c.ChunkSize > 8192 {
		return fmt.Errorf("chunk_size must be between 64 and 8192")
	}
	if c.ChunkOverlap < 0 || c.ChunkOverlap >= 1 {
		return fmt.Errorf("chunk_overlap must be between 0 and 0.99")
	}
	return nil
}

func Parse() (*Config, error) {
	cfg := Default()

	flag.StringVar(&cfg.WatchDir, "dir", "", "Directory to watch (required)")
	flag.StringVar(&cfg.DBPath, "db", "", "Path to SQLite database (default: <dir>/.quant.db)")
	flag.StringVar((*string)(&cfg.Transport), "transport", string(cfg.Transport), "MCP transport: stdio, sse, http")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Listen address for SSE/HTTP transport")
	flag.StringVar(&cfg.EmbedURL, "embed-url", cfg.EmbedURL, "Embedding API URL")
	flag.StringVar(&cfg.EmbedModel, "embed-model", cfg.EmbedModel, "Embedding model")
	flag.IntVar(&cfg.ChunkSize, "chunk-size", cfg.ChunkSize, "Chunk size in words")
	flag.Float64Var(&cfg.ChunkOverlap, "chunk-overlap", cfg.ChunkOverlap, "Chunk overlap fraction (0-1)")
	flag.StringVar(&cfg.ConfigFile, "config", "", "Path to YAML config file")

	flag.Parse()

	if cfg.ConfigFile != "" {
		if err := loadYAML(cfg, cfg.ConfigFile); err != nil {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	applyEnv(cfg)

	flag.Visit(func(f *flag.Flag) {
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
		case "chunk-size":
			fmt.Sscanf(f.Value.String(), "%d", &cfg.ChunkSize)
		case "chunk-overlap":
			fmt.Sscanf(f.Value.String(), "%f", &cfg.ChunkOverlap)
		}
	})

	if cfg.DBPath == "" && cfg.WatchDir != "" {
		cfg.DBPath = filepath.Join(cfg.WatchDir, ".quant.db")
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadYAML(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	type fileConfig struct {
		WatchDir     string    `yaml:"dir"`
		DBPath       string    `yaml:"db"`
		Transport    Transport `yaml:"transport"`
		ListenAddr   string    `yaml:"listen"`
		EmbedURL     string    `yaml:"embed_url"`
		EmbedModel   string    `yaml:"embed_model"`
		ChunkSize    int       `yaml:"chunk_size"`
		ChunkOverlap float64   `yaml:"chunk_overlap"`
	}

	var parsed fileConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}

	if parsed.WatchDir != "" {
		cfg.WatchDir = parsed.WatchDir
	}
	if parsed.DBPath != "" {
		cfg.DBPath = parsed.DBPath
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
	if parsed.ChunkSize != 0 {
		cfg.ChunkSize = parsed.ChunkSize
	}
	if parsed.ChunkOverlap != 0 {
		cfg.ChunkOverlap = parsed.ChunkOverlap
	}

	return nil
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
	if v := os.Getenv("QUANT_CHUNK_SIZE"); v != "" {
		fmt.Sscanf(v, "%d", &cfg.ChunkSize)
	}
	if v := os.Getenv("QUANT_CHUNK_OVERLAP"); v != "" {
		fmt.Sscanf(v, "%f", &cfg.ChunkOverlap)
	}
}
