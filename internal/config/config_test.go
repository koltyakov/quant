package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Transport != TransportStdio {
		t.Errorf("expected transport stdio, got %s", cfg.Transport)
	}
	if cfg.ChunkSize != 512 {
		t.Errorf("expected chunk size 512, got %d", cfg.ChunkSize)
	}
	if cfg.ChunkOverlap != 0.15 {
		t.Errorf("expected chunk overlap 0.15, got %f", cfg.ChunkOverlap)
	}
	if cfg.EmbedModel != "nomic-embed-text" {
		t.Errorf("expected nomic-embed-text, got %s", cfg.EmbedModel)
	}
	if cfg.PDFOCRLang != "eng" {
		t.Errorf("expected PDF OCR lang eng, got %s", cfg.PDFOCRLang)
	}
	if cfg.IndexWorkers < 1 {
		t.Errorf("expected positive index worker count, got %d", cfg.IndexWorkers)
	}
	if cfg.MaxVectorCandidates != 20000 {
		t.Errorf("expected max vector candidates 20000, got %d", cfg.MaxVectorCandidates)
	}
	if cfg.WatchEventBuffer != 256 {
		t.Errorf("expected watch event buffer 256, got %d", cfg.WatchEventBuffer)
	}
}

func TestValidate_NoDir(t *testing.T) {
	cfg := Default()
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

func TestValidate_InvalidDir(t *testing.T) {
	cfg := Default()
	cfg.WatchDir = "/nonexistent/path"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}

func TestValidate_ValidDir(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_InvalidEmbedURL(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.EmbedURL = "not-a-url"

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid embed_url to be rejected")
	}
}

func TestApplyEnv(t *testing.T) {
	cfg := Default()

	t.Setenv("QUANT_EMBED_MODEL", "all-minilm")
	t.Setenv("QUANT_PDF_OCR_LANG", "rus+eng")
	t.Setenv("QUANT_CHUNK_SIZE", "256")
	t.Setenv("QUANT_INDEX_WORKERS", "6")
	t.Setenv("QUANT_MAX_VECTOR_CANDIDATES", "321")
	t.Setenv("QUANT_WATCH_EVENT_BUFFER", "123")

	applyEnv(cfg)

	if cfg.EmbedModel != "all-minilm" {
		t.Errorf("expected embed model all-minilm, got %s", cfg.EmbedModel)
	}
	if cfg.PDFOCRLang != "rus+eng" {
		t.Errorf("expected PDF OCR lang rus+eng, got %s", cfg.PDFOCRLang)
	}
	if cfg.ChunkSize != 256 {
		t.Errorf("expected chunk size 256, got %d", cfg.ChunkSize)
	}
	if cfg.IndexWorkers != 6 {
		t.Errorf("expected index workers 6, got %d", cfg.IndexWorkers)
	}
	if cfg.MaxVectorCandidates != 321 {
		t.Errorf("expected max vector candidates 321, got %d", cfg.MaxVectorCandidates)
	}
	if cfg.WatchEventBuffer != 123 {
		t.Errorf("expected watch event buffer 123, got %d", cfg.WatchEventBuffer)
	}
}

func TestDefaultIndexWorkers_ScalesWithCPU(t *testing.T) {
	got := defaultIndexWorkers()
	if got < 1 {
		t.Fatalf("expected at least 1 index worker, got %d", got)
	}
	if got > 8 {
		t.Fatalf("expected at most 8 index workers, got %d", got)
	}
}

func TestDefaultMaxConcurrentTools(t *testing.T) {
	got := defaultMaxConcurrentTools()
	if got < 1 {
		t.Fatalf("expected at least 1, got %d", got)
	}
	if got > 8 {
		t.Fatalf("expected at most 8, got %d", got)
	}
}

func TestDefaultMemoryLimit(t *testing.T) {
	got := DefaultMemoryLimit()
	if got < 0 {
		t.Fatalf("expected non-negative limit, got %d", got)
	}
	if got > 4<<30 {
		t.Fatalf("expected at most 4 GiB, got %d", got)
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("embed_model: all-minilm\npdf_ocr_lang: rus+eng\nchunk_size: 1024\nindex_workers: 5\nmax_vector_candidates: 123\nwatch_event_buffer: 321\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbedModel != "all-minilm" {
		t.Errorf("expected embed model all-minilm, got %s", cfg.EmbedModel)
	}
	if cfg.PDFOCRLang != "rus+eng" {
		t.Errorf("expected PDF OCR lang rus+eng, got %s", cfg.PDFOCRLang)
	}
	if cfg.ChunkSize != 1024 {
		t.Errorf("expected chunk size 1024, got %d", cfg.ChunkSize)
	}
	if cfg.IndexWorkers != 5 {
		t.Errorf("expected index workers 5, got %d", cfg.IndexWorkers)
	}
	if cfg.MaxVectorCandidates != 123 {
		t.Errorf("expected max vector candidates 123, got %d", cfg.MaxVectorCandidates)
	}
	if cfg.WatchEventBuffer != 321 {
		t.Errorf("expected watch event buffer 321, got %d", cfg.WatchEventBuffer)
	}
}

func TestLoadYAML_AllowsZeroChunkOverlap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("chunk_overlap: 0\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ChunkOverlap != 0 {
		t.Fatalf("expected chunk overlap 0, got %f", cfg.ChunkOverlap)
	}
}

func TestParseArgs_ResolvesConfigPathsRelativeToConfigFile(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "configs", "project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	cfgDir := filepath.Join(root, "configs")
	cfgPath := filepath.Join(cfgDir, "quant.yaml")
	if err := os.WriteFile(cfgPath, []byte("dir: ./project\ndb: ./.index/quant.db\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	otherDir := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatalf("unexpected mkdir error: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("unexpected getwd error: %v", err)
	}
	if err := os.Chdir(otherDir); err != nil {
		t.Fatalf("unexpected chdir error: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	cfg, err := ParseArgs([]string{"--config", cfgPath})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.WatchDir != projectDir {
		t.Fatalf("expected watch dir %q, got %q", projectDir, cfg.WatchDir)
	}
	wantDB := filepath.Join(cfgDir, ".index", "quant.db")
	if cfg.DBPath != wantDB {
		t.Fatalf("expected db path %q, got %q", wantDB, cfg.DBPath)
	}
}

func TestParseArgs_Help(t *testing.T) {
	_, err := ParseArgs([]string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
}

func TestParseArgs_RejectsUnexpectedPositionalArgs(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseArgs([]string{"--dir", dir, "extra"})
	if err == nil {
		t.Fatal("expected error for unexpected positional arguments")
	}
}

func TestParseArgs_AcceptsPDFOCRTimeoutFlag(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--pdf-ocr-timeout", "45s"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.PDFOCRTimeout.Seconds() != 45 {
		t.Fatalf("expected OCR timeout 45s, got %s", cfg.PDFOCRTimeout)
	}
}

func TestParseArgs_AcceptsMaxVectorCandidatesFlag(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--max-vector-candidates", "77"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.MaxVectorCandidates != 77 {
		t.Fatalf("expected max vector candidates 77, got %d", cfg.MaxVectorCandidates)
	}
}

func TestParseArgs_AcceptsWatchEventBufferFlag(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir, "--watch-event-buffer", "512"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.WatchEventBuffer != 512 {
		t.Fatalf("expected watch event buffer 512, got %d", cfg.WatchEventBuffer)
	}
}
