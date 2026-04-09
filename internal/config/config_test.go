package config

import (
	"os"
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

func TestApplyEnv(t *testing.T) {
	cfg := Default()

	t.Setenv("QUANT_EMBED_MODEL", "all-minilm")
	t.Setenv("QUANT_PDF_OCR_LANG", "rus+eng")
	t.Setenv("QUANT_CHUNK_SIZE", "256")
	t.Setenv("QUANT_INDEX_WORKERS", "6")

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
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("embed_model: all-minilm\npdf_ocr_lang: rus+eng\nchunk_size: 1024\nindex_workers: 5\n"), 0644); err != nil {
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
}
