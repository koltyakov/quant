package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMustParseIntFlag(t *testing.T) {
	t.Parallel()
	t.Run("valid int", func(t *testing.T) {
		got := mustParseIntFlag("chunk-size", "256", 512)
		if got != 256 {
			t.Errorf("expected 256, got %d", got)
		}
	})
	t.Run("invalid int returns fallback", func(t *testing.T) {
		got := mustParseIntFlag("chunk-size", "abc", 512)
		if got != 512 {
			t.Errorf("expected fallback 512, got %d", got)
		}
	})
}

func TestMustParseFloatFlag(t *testing.T) {
	t.Parallel()
	t.Run("valid float", func(t *testing.T) {
		got := mustParseFloatFlag("chunk-overlap", "0.25", 0.15)
		if got != 0.25 {
			t.Errorf("expected 0.25, got %f", got)
		}
	})
	t.Run("invalid float returns fallback", func(t *testing.T) {
		got := mustParseFloatFlag("chunk-overlap", "notafloat", 0.15)
		if got != 0.15 {
			t.Errorf("expected fallback 0.15, got %f", got)
		}
	})
}

func TestMustParseIntEnv(t *testing.T) {
	t.Parallel()
	t.Run("valid int", func(t *testing.T) {
		got := mustParseIntEnv("QUANT_CHUNK_SIZE", "128", 512)
		if got != 128 {
			t.Errorf("expected 128, got %d", got)
		}
	})
	t.Run("invalid int returns fallback", func(t *testing.T) {
		got := mustParseIntEnv("QUANT_CHUNK_SIZE", "bad", 512)
		if got != 512 {
			t.Errorf("expected fallback 512, got %d", got)
		}
	})
}

func TestMustParseFloatEnv(t *testing.T) {
	t.Parallel()
	t.Run("valid float", func(t *testing.T) {
		got := mustParseFloatEnv("QUANT_CHUNK_OVERLAP", "0.5", 0.15)
		if got != 0.5 {
			t.Errorf("expected 0.5, got %f", got)
		}
	})
	t.Run("invalid float returns fallback", func(t *testing.T) {
		got := mustParseFloatEnv("QUANT_CHUNK_OVERLAP", "xxx", 0.15)
		if got != 0.15 {
			t.Errorf("expected fallback 0.15, got %f", got)
		}
	})
}

func TestDefaultDBPath(t *testing.T) {
	t.Parallel()
	got := defaultDBPath("/tmp/project")
	expected := filepath.Join("/tmp/project", ".index", "quant.db")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestResolveConfigPath(t *testing.T) {
	t.Parallel()
	t.Run("empty string returns empty", func(t *testing.T) {
		got := resolveConfigPath("/base", "")
		if got != "" {
			t.Errorf("expected empty, got %s", got)
		}
	})
	t.Run("absolute path unchanged", func(t *testing.T) {
		abs := filepath.Join("/", "absolute", "path")
		got := resolveConfigPath("/base", abs)
		if got != abs {
			t.Errorf("expected %s, got %s", abs, got)
		}
	})
	t.Run("relative path resolved", func(t *testing.T) {
		got := resolveConfigPath("/base/dir", "relative/path")
		expected := filepath.Join("/base/dir", "relative/path")
		if got != expected {
			t.Errorf("expected %s, got %s", expected, got)
		}
	})
}

func TestValidate_Errors(t *testing.T) {
	dir := t.TempDir()
	t.Run("not a directory", func(t *testing.T) {
		f, err := os.CreateTemp(dir, "notadir-*")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_ = f.Close()
		cfg := Default()
		cfg.WatchDir = f.Name()
		err = cfg.Validate()
		if err == nil {
			t.Fatal("expected error when watch dir is a file")
		}
	})
	t.Run("invalid transport", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.Transport = "ftp"
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for invalid transport")
		}
	})
	t.Run("chunk size too small", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkSize = 10
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for small chunk size")
		}
	})
	t.Run("chunk size too large", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkSize = 99999
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for large chunk size")
		}
	})
	t.Run("chunk overlap too large", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkOverlap = 1.0
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for chunk overlap >= 1")
		}
	})
	t.Run("chunk overlap negative", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkOverlap = -0.1
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for negative chunk overlap")
		}
	})
	t.Run("index workers too small", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.IndexWorkers = 0
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for zero index workers")
		}
	})
	t.Run("index workers too large", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.IndexWorkers = 100
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for large index workers")
		}
	})
	t.Run("embed batch size too small", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.EmbedBatchSize = 0
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for zero embed batch size")
		}
	})
	t.Run("embed batch size too large", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.EmbedBatchSize = 200
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for large embed batch size")
		}
	})
}

func TestValidateEmbedURL(t *testing.T) {
	t.Parallel()
	t.Run("valid http URL", func(t *testing.T) {
		err := validateEmbedURL("http://localhost:11434")
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
	t.Run("valid https URL", func(t *testing.T) {
		err := validateEmbedURL("https://api.example.com")
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
	t.Run("missing scheme", func(t *testing.T) {
		err := validateEmbedURL("localhost:11434")
		if err == nil {
			t.Fatal("expected error for URL without scheme")
		}
	})
	t.Run("invalid scheme ftp", func(t *testing.T) {
		err := validateEmbedURL("ftp://host/path")
		if err == nil {
			t.Fatal("expected error for ftp scheme")
		}
	})
	t.Run("empty string", func(t *testing.T) {
		err := validateEmbedURL("")
		if err == nil {
			t.Fatal("expected error for empty embed URL")
		}
	})
	t.Run("relative path", func(t *testing.T) {
		err := validateEmbedURL("/just/a/path")
		if err == nil {
			t.Fatal("expected error for relative path")
		}
	})
}

func TestCheckDirWritable(t *testing.T) {
	t.Run("writable dir", func(t *testing.T) {
		dir := t.TempDir()
		err := checkDirWritable(dir)
		if err != nil {
			t.Errorf("expected writable dir, got %v", err)
		}
	})
	t.Run("creates dir if missing", func(t *testing.T) {
		parent := t.TempDir()
		sub := filepath.Join(parent, "subdir")
		err := checkDirWritable(sub)
		if err != nil {
			t.Errorf("expected mkdir+write, got %v", err)
		}
	})
	t.Run("permission denied", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("running as root, skip permission test")
		}
		dir := t.TempDir()
		readOnly := filepath.Join(dir, "readonly")
		if err := os.MkdirAll(readOnly, 0555); err != nil {
			t.Fatalf("unexpected mkdir error: %v", err)
		}
		err := checkDirWritable(readOnly)
		if err == nil {
			t.Error("expected error for read-only dir")
		}
	})
}

func TestConfigPathMatcher(t *testing.T) {
	t.Run("nil when no patterns", func(t *testing.T) {
		cfg := Default()
		if m := cfg.PathMatcher(); m != nil {
			t.Errorf("expected nil matcher, got %v", m)
		}
	})
	t.Run("returns matcher when include patterns set", func(t *testing.T) {
		cfg := Default()
		cfg.IncludePatterns = []string{"*.go"}
		m := cfg.PathMatcher()
		if m == nil {
			t.Fatal("expected non-nil matcher")
		}
		if !m.ShouldIndex("main.go") {
			t.Error("expected main.go to be included")
		}
	})
	t.Run("returns matcher when exclude patterns set", func(t *testing.T) {
		cfg := Default()
		cfg.ExcludePatterns = []string{"*.log"}
		m := cfg.PathMatcher()
		if m == nil {
			t.Fatal("expected non-nil matcher")
		}
		if m.ShouldIndex("server.log") {
			t.Error("expected server.log to be excluded")
		}
	})
	t.Run("caches matcher", func(t *testing.T) {
		cfg := Default()
		cfg.IncludePatterns = []string{"*.go"}
		m1 := cfg.PathMatcher()
		m2 := cfg.PathMatcher()
		if m1 != m2 {
			t.Error("expected same matcher instance")
		}
	})
}

func TestParseArgs_BasicFlags(t *testing.T) {
	dir := t.TempDir()
	t.Run("dir flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.WatchDir != filepath.Clean(dir) {
			t.Errorf("expected %s, got %s", filepath.Clean(dir), cfg.WatchDir)
		}
	})
	t.Run("transport flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--transport", "sse"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Transport != TransportSSE {
			t.Errorf("expected sse, got %s", cfg.Transport)
		}
	})
	t.Run("embed-url flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--embed-url", "http://localhost:1234"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.EmbedURL != "http://localhost:1234" {
			t.Errorf("expected http://localhost:1234, got %s", cfg.EmbedURL)
		}
	})
	t.Run("chunk-size flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--chunk-size", "1024"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ChunkSize != 1024 {
			t.Errorf("expected 1024, got %d", cfg.ChunkSize)
		}
	})
	t.Run("embed-model flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--embed-model", "custom-model"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.EmbedModel != "custom-model" {
			t.Errorf("expected custom-model, got %s", cfg.EmbedModel)
		}
	})
	t.Run("pdf-ocr-lang flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--pdf-ocr-lang", "deu"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.PDFOCRLang != "deu" {
			t.Errorf("expected deu, got %s", cfg.PDFOCRLang)
		}
	})
	t.Run("listen flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--listen", ":9090"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.ListenAddr != ":9090" {
			t.Errorf("expected :9090, got %s", cfg.ListenAddr)
		}
	})
	t.Run("db flag", func(t *testing.T) {
		dbPath := filepath.Join(dir, "custom.db")
		cfg, err := ParseArgs([]string{"--dir", dir, "--db", dbPath})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DBPath != filepath.Clean(dbPath) {
			t.Errorf("expected %s, got %s", filepath.Clean(dbPath), cfg.DBPath)
		}
	})
	t.Run("embed-batch-size flag", func(t *testing.T) {
		cfg, err := ParseArgs([]string{"--dir", dir, "--embed-batch-size", "32"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.EmbedBatchSize != 32 {
			t.Errorf("expected 32, got %d", cfg.EmbedBatchSize)
		}
	})
	t.Run("invalid flag", func(t *testing.T) {
		_, err := ParseArgs([]string{"--dir", dir, "--unknown-flag"})
		if err == nil {
			t.Fatal("expected error for unknown flag")
		}
	})
	t.Run("invalid transport", func(t *testing.T) {
		_, err := ParseArgs([]string{"--dir", dir, "--transport", "ftp"})
		if err == nil {
			t.Fatal("expected error for invalid transport")
		}
	})
}

func TestParseArgs_DefaultDir(t *testing.T) {
	cfg, err := ParseArgs([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wd, _ := os.Getwd()
	if cfg.WatchDir != filepath.Clean(wd) {
		t.Errorf("expected current dir %s, got %s", filepath.Clean(wd), cfg.WatchDir)
	}
}

func TestParseArgs_DefaultDBPath(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(filepath.Clean(dir), ".index", "quant.db")
	if cfg.DBPath != expected {
		t.Errorf("expected %s, got %s", expected, cfg.DBPath)
	}
}

func TestApplyEnv_AllVars(t *testing.T) {
	cfg := Default()
	dir := t.TempDir()

	t.Setenv("QUANT_DIR", dir)
	t.Setenv("QUANT_DB", filepath.Join(dir, "test.db"))
	t.Setenv("QUANT_TRANSPORT", "SSE")
	t.Setenv("QUANT_LISTEN", ":9999")
	t.Setenv("QUANT_EMBED_URL", "http://example.com")
	t.Setenv("QUANT_EMBED_MODEL", "test-model")
	t.Setenv("QUANT_EMBED_PROVIDER", "openai")
	t.Setenv("QUANT_EMBED_API_KEY", "key123")
	t.Setenv("QUANT_PDF_OCR_LANG", "fra")
	t.Setenv("QUANT_CHUNK_SIZE", "256")
	t.Setenv("QUANT_CHUNK_OVERLAP", "0.5")
	t.Setenv("QUANT_INDEX_WORKERS", "4")
	t.Setenv("QUANT_EMBED_BATCH_SIZE", "64")

	applyEnv(cfg)

	if cfg.WatchDir != dir {
		t.Errorf("expected %s, got %s", dir, cfg.WatchDir)
	}
	if cfg.Transport != TransportSSE {
		t.Errorf("expected sse, got %s", cfg.Transport)
	}
	if cfg.ListenAddr != ":9999" {
		t.Errorf("expected :9999, got %s", cfg.ListenAddr)
	}
	if cfg.EmbedURL != "http://example.com" {
		t.Errorf("expected http://example.com, got %s", cfg.EmbedURL)
	}
	if cfg.EmbedModel != "test-model" {
		t.Errorf("expected test-model, got %s", cfg.EmbedModel)
	}
	if cfg.EmbedProvider != "openai" {
		t.Errorf("expected openai, got %s", cfg.EmbedProvider)
	}
	if cfg.EmbedAPIKey != "key123" {
		t.Errorf("expected key123, got %s", cfg.EmbedAPIKey)
	}
	if cfg.PDFOCRLang != "fra" {
		t.Errorf("expected fra, got %s", cfg.PDFOCRLang)
	}
	if cfg.ChunkSize != 256 {
		t.Errorf("expected 256, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 0.5 {
		t.Errorf("expected 0.5, got %f", cfg.ChunkOverlap)
	}
	if cfg.IndexWorkers != 4 {
		t.Errorf("expected 4, got %d", cfg.IndexWorkers)
	}
	if cfg.EmbedBatchSize != 64 {
		t.Errorf("expected 64, got %d", cfg.EmbedBatchSize)
	}
}

func TestApplyEnv_InvalidValuesUseDefaults(t *testing.T) {
	cfg := Default()

	t.Setenv("QUANT_CHUNK_SIZE", "abc")
	t.Setenv("QUANT_CHUNK_OVERLAP", "xyz")
	t.Setenv("QUANT_INDEX_WORKERS", "bad")
	t.Setenv("QUANT_EMBED_BATCH_SIZE", "nope")

	wantChunkSize := cfg.ChunkSize
	wantChunkOverlap := cfg.ChunkOverlap

	applyEnv(cfg)

	if cfg.ChunkSize != wantChunkSize {
		t.Errorf("expected default chunk size to be preserved, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != wantChunkOverlap {
		t.Errorf("expected default chunk overlap to be preserved, got %f", cfg.ChunkOverlap)
	}
}

func TestLoadYAML_AllFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `dir: /tmp/myproject
db: /tmp/myproject/data.db
transport: http
listen: ":3000"
embed_url: https://api.openai.com
embed_model: text-embedding-ada-002
embed_batch_size: 32
pdf_ocr_lang: jpn
chunk_size: 2048
chunk_overlap: 0.25
index_workers: 4
include:
  - "*.go"
  - "*.ts"
exclude:
  - "vendor/**"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.WatchDir != "/tmp/myproject" {
		t.Errorf("expected /tmp/myproject, got %s", cfg.WatchDir)
	}
	if cfg.DBPath != "/tmp/myproject/data.db" {
		t.Errorf("expected /tmp/myproject/data.db, got %s", cfg.DBPath)
	}
	if cfg.Transport != TransportHTTP {
		t.Errorf("expected http, got %s", cfg.Transport)
	}
	if cfg.ListenAddr != ":3000" {
		t.Errorf("expected :3000, got %s", cfg.ListenAddr)
	}
	if cfg.EmbedURL != "https://api.openai.com" {
		t.Errorf("expected https://api.openai.com, got %s", cfg.EmbedURL)
	}
	if cfg.EmbedModel != "text-embedding-ada-002" {
		t.Errorf("expected text-embedding-ada-002, got %s", cfg.EmbedModel)
	}
	if cfg.EmbedBatchSize != 32 {
		t.Errorf("expected 32, got %d", cfg.EmbedBatchSize)
	}
	if cfg.PDFOCRLang != "jpn" {
		t.Errorf("expected jpn, got %s", cfg.PDFOCRLang)
	}
	if cfg.ChunkSize != 2048 {
		t.Errorf("expected 2048, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 0.25 {
		t.Errorf("expected 0.25, got %f", cfg.ChunkOverlap)
	}
	if cfg.IndexWorkers != 4 {
		t.Errorf("expected 4, got %d", cfg.IndexWorkers)
	}
	if len(cfg.IncludePatterns) != 2 {
		t.Errorf("expected 2 include patterns, got %d", len(cfg.IncludePatterns))
	}
	if len(cfg.ExcludePatterns) != 1 {
		t.Errorf("expected 1 exclude pattern, got %d", len(cfg.ExcludePatterns))
	}
}

func TestLoadYAML_RelativePaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `dir: ./subdir
db: ./data/quant.db
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDir := filepath.Join(dir, "subdir")
	expectedDB := filepath.Join(dir, "data", "quant.db")
	if cfg.WatchDir != expectedDir {
		t.Errorf("expected %s, got %s", expectedDir, cfg.WatchDir)
	}
	if cfg.DBPath != expectedDB {
		t.Errorf("expected %s, got %s", expectedDB, cfg.DBPath)
	}
}

func TestLoadYAML_NonexistentFile(t *testing.T) {
	cfg := Default()
	err := loadYAML(cfg, "/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadYAML_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("invalid: [yaml: content"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseArgs_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := "transport: http\nlisten: \":4000\"\nchunk_size: 2048\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg, err := ParseArgs([]string{"--dir", dir, "--config", cfgPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportHTTP {
		t.Errorf("expected http, got %s", cfg.Transport)
	}
	if cfg.ListenAddr != ":4000" {
		t.Errorf("expected :4000, got %s", cfg.ListenAddr)
	}
	if cfg.ChunkSize != 2048 {
		t.Errorf("expected 2048, got %d", cfg.ChunkSize)
	}
}

func TestParseArgs_ConfigFileNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseArgs([]string{"--dir", dir, "--config", "/nonexistent/config.yaml"})
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestNewFlagSet(t *testing.T) {
	flagSet, cfg := NewFlagSet("test")
	if flagSet == nil {
		t.Fatal("expected non-nil flagset")
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Transport != TransportStdio {
		t.Errorf("expected stdio transport, got %s", cfg.Transport)
	}
}

func TestTransport_Constants(t *testing.T) {
	t.Parallel()
	if TransportStdio != "stdio" {
		t.Errorf("expected stdio, got %s", TransportStdio)
	}
	if TransportSSE != "sse" {
		t.Errorf("expected sse, got %s", TransportSSE)
	}
	if TransportHTTP != "http" {
		t.Errorf("expected http, got %s", TransportHTTP)
	}
}

func TestDefault_Values(t *testing.T) {
	cfg := Default()
	if cfg.ListenAddr != ":8080" {
		t.Errorf("expected :8080, got %s", cfg.ListenAddr)
	}
	if cfg.EmbedURL != "http://localhost:11434" {
		t.Errorf("expected http://localhost:11434, got %s", cfg.EmbedURL)
	}
	if cfg.EmbedBatchSize != 16 {
		t.Errorf("expected 16, got %d", cfg.EmbedBatchSize)
	}
	if cfg.PDFOCRTimeout != 2*time.Minute {
		t.Errorf("expected 2m, got %s", cfg.PDFOCRTimeout)
	}
	if cfg.HNSWM != 16 {
		t.Errorf("expected 16, got %d", cfg.HNSWM)
	}
	if cfg.HNSWEfSearch != 100 {
		t.Errorf("expected 100, got %d", cfg.HNSWEfSearch)
	}
	if cfg.HNSWReoptimizeThreshold != 0.2 {
		t.Errorf("expected 0.2, got %f", cfg.HNSWReoptimizeThreshold)
	}
	if cfg.NoLock != false {
		t.Errorf("expected false, got %v", cfg.NoLock)
	}
}

func TestDefaultIndexWorkers_Bounds(t *testing.T) {
	t.Parallel()
	got := defaultIndexWorkers()
	if got < 1 || got > 8 {
		t.Errorf("defaultIndexWorkers = %d, want in [1,8]", got)
	}
}

func TestDefaultMaxVectorCandidates(t *testing.T) {
	t.Parallel()
	if defaultMaxVectorCandidates() != 20000 {
		t.Errorf("expected 20000, got %d", defaultMaxVectorCandidates())
	}
}

func TestDefaultHNSWM(t *testing.T) {
	t.Parallel()
	if defaultHNSWM() != 16 {
		t.Errorf("expected 16, got %d", defaultHNSWM())
	}
}

func TestDefaultHNSWEfSearch(t *testing.T) {
	t.Parallel()
	if defaultHNSWEfSearch() != 100 {
		t.Errorf("expected 100, got %d", defaultHNSWEfSearch())
	}
}

func TestValidate_ValidTransports(t *testing.T) {
	dir := t.TempDir()
	for _, transport := range []Transport{TransportStdio, TransportSSE, TransportHTTP} {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.Transport = transport
		if err := cfg.Validate(); err != nil {
			t.Errorf("expected valid for transport %s, got %v", transport, err)
		}
	}
}

func TestValidate_ValidEmbedURL(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.EmbedURL = "https://api.example.com/v1"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_BoundaryChunkOverlap(t *testing.T) {
	dir := t.TempDir()
	t.Run("zero overlap is valid", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkOverlap = 0
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("0.99 overlap is valid", func(t *testing.T) {
		cfg := Default()
		cfg.WatchDir = dir
		cfg.ChunkOverlap = 0.99
		if err := cfg.Validate(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestValidate_DBPathWritable(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.DBPath = filepath.Join(dir, "quant.db")
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseArgs_RerankerFlags(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--reranker", "cross-encoder", "--reranker-model", "llama3.2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RerankerType != "cross-encoder" {
		t.Errorf("expected cross-encoder, got %s", cfg.RerankerType)
	}
	if cfg.RerankerModel != "llama3.2" {
		t.Errorf("expected llama3.2, got %s", cfg.RerankerModel)
	}
}

func TestParseArgs_SummarizerFlags(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--summarizer", "--summarizer-model", "gpt-4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.SummarizerEnabled {
		t.Error("expected summarizer to be enabled")
	}
	if cfg.SummarizerModel != "gpt-4" {
		t.Errorf("expected gpt-4, got %s", cfg.SummarizerModel)
	}
}

func TestParseArgs_EnvOverrides(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUANT_EMBED_MODEL", "env-model")
	cfg, err := ParseArgs([]string{"--dir", dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbedModel != "env-model" {
		t.Errorf("expected env-model, got %s", cfg.EmbedModel)
	}
}

func TestParseArgs_EnvAppliedAfterFlags(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUANT_EMBED_MODEL", "env-model")
	cfg, err := ParseArgs([]string{"--dir", dir, "--embed-model", "flag-model"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbedModel != "env-model" {
		t.Errorf("expected env-model (env applied after flags), got %s", cfg.EmbedModel)
	}
}

func TestParseArgs_HTTPTransport(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--transport", "http"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Transport != TransportHTTP {
		t.Errorf("expected http, got %s", cfg.Transport)
	}
}

func TestParse_Error(t *testing.T) {
	t.Setenv("QUANT_DIR", "/nonexistent/path/for/parse/test")
	_, err := Parse()
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestLoadYAML_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(""), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	defaultChunkSize := cfg.ChunkSize
	err := loadYAML(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ChunkSize != defaultChunkSize {
		t.Errorf("expected defaults preserved, got %d", cfg.ChunkSize)
	}
}

func TestMatchPattern_Simple(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"exact match", "file.go", "file.go", true},
		{"no match", "*.go", "file.ts", false},
		{"glob star", "*.go", "main.go", true},
		{"component match dotfile", ".git", ".git", true},
		{"component match subdir", ".git", "src/.git", true},
		{"no match different ext", "*.log", "main.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPattern(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestMatchDoubleStarPattern(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"dir star star matches sub", "node_modules/**", "node_modules/lodash/index.js", true},
		{"dir star star no match", "node_modules/**", "src/index.js", false},
		{"star star file matches any dir", "**/file.go", "src/pkg/file.go", true},
		{"star star dir star star matches deep", "**/vendor/**", "pkg/vendor/lib/file.go", true},
		{"star star at end matches all", "src/**", "src/a/b/c.go", true},
		{"star star at end no prefix match", "src/**", "other/a.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchDoubleStarPattern(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchDoubleStarPattern(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestMatchPartsRecursive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		patternParts []string
		pathParts    []string
		want         bool
	}{
		{"exact match", []string{"src", "main.go"}, []string{"src", "main.go"}, true},
		{"pattern longer", []string{"src", "main.go", "extra"}, []string{"src", "main.go"}, false},
		{"path longer", []string{"src"}, []string{"src", "extra"}, false},
		{"double star at end matches all suffixes", []string{"**"}, []string{"a", "b", "c"}, true},
		{"double star in middle", []string{"src", "**", "main.go"}, []string{"src", "pkg", "main.go"}, true},
		{"double star no match", []string{"src", "**", "main.go"}, []string{"other", "pkg", "main.go"}, false},
		{"empty both", []string{}, []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPartsRecursive(tt.patternParts, tt.pathParts)
			if got != tt.want {
				t.Errorf("matchPartsRecursive(%v, %v) = %v, want %v", tt.patternParts, tt.pathParts, got, tt.want)
			}
		})
	}
}

func TestPathMatcher_Merge_Nil(t *testing.T) {
	t.Parallel()
	m := &PathMatcher{
		IncludePatterns: []string{"*.go"},
		ExcludePatterns: []string{"vendor/**"},
	}
	originalInclude := len(m.IncludePatterns)
	originalExclude := len(m.ExcludePatterns)
	m.Merge(nil)
	if len(m.IncludePatterns) != originalInclude {
		t.Errorf("Merge(nil) should not modify IncludePatterns")
	}
	if len(m.ExcludePatterns) != originalExclude {
		t.Errorf("Merge(nil) should not modify ExcludePatterns")
	}
}

func TestValidate_NonexistentDir(t *testing.T) {
	cfg := Default()
	cfg.WatchDir = "/nonexistent/path/that/does/not/exist"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestParseArgs_ChunkOverlapFlag(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--chunk-overlap", "0.5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ChunkOverlap != 0.5 {
		t.Errorf("expected 0.5, got %f", cfg.ChunkOverlap)
	}
}

func TestParseArgs_IndexWorkersFlag(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--index-workers", "4"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IndexWorkers != 4 {
		t.Errorf("expected 4, got %d", cfg.IndexWorkers)
	}
}
