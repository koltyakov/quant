package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if cfg.LLMURL != "http://localhost:11434" {
		t.Errorf("expected default llm URL, got %s", cfg.LLMURL)
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
	t.Setenv("QUANT_LLM_URL", "https://llm.example.com")
	t.Setenv("QUANT_LLM_MODEL", "gpt-4.1-mini")
	t.Setenv("QUANT_LLM_PROVIDER", "openai")
	t.Setenv("QUANT_LLM_API_KEY", "llm-secret")
	t.Setenv("QUANT_PDF_OCR_LANG", "rus+eng")
	t.Setenv("QUANT_CHUNK_SIZE", "256")
	t.Setenv("QUANT_INDEX_WORKERS", "6")

	applyEnv(cfg)

	if cfg.EmbedModel != "all-minilm" {
		t.Errorf("expected embed model all-minilm, got %s", cfg.EmbedModel)
	}
	if cfg.LLMURL != "https://llm.example.com" {
		t.Errorf("expected llm URL https://llm.example.com, got %s", cfg.LLMURL)
	}
	if cfg.LLMModel != "gpt-4.1-mini" {
		t.Errorf("expected llm model gpt-4.1-mini, got %s", cfg.LLMModel)
	}
	if cfg.LLMProvider != "openai" {
		t.Errorf("expected llm provider openai, got %s", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "llm-secret" {
		t.Errorf("expected llm api key llm-secret, got %s", cfg.LLMAPIKey)
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
	if err := os.WriteFile(cfgPath, []byte("embed_model: all-minilm\npdf_ocr_lang: rus+eng\nchunk_size: 1024\nindex_workers: 5\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath, nil)
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

func TestLoadYAML_AllowsZeroChunkOverlap(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("chunk_overlap: 0\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	err := loadYAML(cfg, cfgPath, nil)
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

func TestApplyEnv_RerankerSummarizer(t *testing.T) {
	cfg := Default()

	t.Setenv("QUANT_RERANKER", "cross-encoder")
	t.Setenv("QUANT_RERANKER_MODEL", "llama3.2")
	t.Setenv("QUANT_SUMMARIZER", "true")
	t.Setenv("QUANT_SUMMARIZER_MODEL", "llama3.2")

	applyEnv(cfg)

	if cfg.RerankerType != "cross-encoder" {
		t.Errorf("expected reranker cross-encoder, got %s", cfg.RerankerType)
	}
	if cfg.RerankerModel != "llama3.2" {
		t.Errorf("expected reranker model llama3.2, got %s", cfg.RerankerModel)
	}
	if !cfg.SummarizerEnabled {
		t.Errorf("expected summarizer enabled")
	}
	if cfg.SummarizerModel != "llama3.2" {
		t.Errorf("expected summarizer model llama3.2, got %s", cfg.SummarizerModel)
	}
}

func TestApplyEnv_SummarizerBoolVariants(t *testing.T) {
	for _, v := range []string{"true", "1", "yes"} {
		cfg := Default()
		t.Setenv("QUANT_SUMMARIZER", v)
		applyEnv(cfg)
		if !cfg.SummarizerEnabled {
			t.Errorf("QUANT_SUMMARIZER=%q should enable summarizer", v)
		}
	}
	cfg := Default()
	t.Setenv("QUANT_SUMMARIZER", "false")
	applyEnv(cfg)
	if cfg.SummarizerEnabled {
		t.Errorf("QUANT_SUMMARIZER=false should not enable summarizer")
	}
}

func TestParseArgs_EmbedProviderAndAPIKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QUANT_EMBED_PROVIDER", "openai")
	t.Setenv("QUANT_EMBED_API_KEY", "env-key")

	cfg, err := ParseArgs([]string{"--dir", dir})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	// Env vars take precedence (applied after flags, per documented precedence).
	if cfg.EmbedProvider != "openai" {
		t.Errorf("expected embed_provider openai from env, got %s", cfg.EmbedProvider)
	}
	if cfg.EmbedAPIKey != "env-key" {
		t.Errorf("expected embed_api_key env-key from env, got %s", cfg.EmbedAPIKey)
	}
}

func TestLoadYAML_EmbedProviderAndAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	if err := os.WriteFile(cfgPath, []byte("embed_provider: openai\nembed_api_key: sk-test\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	if err := loadYAML(cfg, cfgPath, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.EmbedProvider != "openai" {
		t.Errorf("expected embed_provider openai, got %s", cfg.EmbedProvider)
	}
	if cfg.EmbedAPIKey != "sk-test" {
		t.Errorf("expected embed_api_key sk-test, got %s", cfg.EmbedAPIKey)
	}
}

func TestLoadYAML_LLMConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := dir + "/config.yaml"
	content := "llm_url: https://llm.example.com/v1\nllm_model: gpt-4.1-mini\nllm_provider: openai\nllm_api_key: llm-test\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	if err := loadYAML(cfg, cfgPath, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLMURL != "https://llm.example.com/v1" {
		t.Errorf("expected llm_url from YAML, got %s", cfg.LLMURL)
	}
	if cfg.LLMModel != "gpt-4.1-mini" {
		t.Errorf("expected llm_model from YAML, got %s", cfg.LLMModel)
	}
	if cfg.LLMProvider != "openai" {
		t.Errorf("expected llm_provider from YAML, got %s", cfg.LLMProvider)
	}
	if cfg.LLMAPIKey != "llm-test" {
		t.Errorf("expected llm_api_key from YAML, got %s", cfg.LLMAPIKey)
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
	cfg, err := ParseArgs([]string{"--dir", dir})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.PDFOCRTimeout != 2*time.Minute {
		t.Fatalf("expected OCR timeout 2m (internal default), got %s", cfg.PDFOCRTimeout)
	}
}

func TestParseArgs_InternalDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := ParseArgs([]string{"--dir", dir})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.MaxVectorCandidates != 20000 {
		t.Fatalf("expected max vector candidates 20000, got %d", cfg.MaxVectorCandidates)
	}
	if cfg.WatchEventBuffer != 256 {
		t.Fatalf("expected watch event buffer 256, got %d", cfg.WatchEventBuffer)
	}
}

func TestParseArgs_CLIBeatsYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("embed_model: yaml-model\nllm_model: yaml-llm\nchunk_size: 1024\n"), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg, err := ParseArgs([]string{"--dir", dir, "--config", cfgPath, "--embed-model", "cli-model", "--llm-model", "cli-llm"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if cfg.EmbedModel != "cli-model" {
		t.Errorf("CLI flag should beat YAML; got %s", cfg.EmbedModel)
	}
	if cfg.ChunkSize != 1024 {
		t.Errorf("YAML should apply when no CLI flag is set; got %d", cfg.ChunkSize)
	}
	if cfg.LLMModel != "cli-llm" {
		t.Errorf("CLI llm-model should beat YAML; got %s", cfg.LLMModel)
	}
}

func TestLoadYAML_RerankerAndSummarizer(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := "reranker: cross-encoder\nreranker_model: llama3.2\nsummarizer: true\nsummarizer_model: mistral\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}

	cfg := Default()
	if err := loadYAML(cfg, cfgPath, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RerankerType != "cross-encoder" {
		t.Errorf("expected reranker cross-encoder, got %s", cfg.RerankerType)
	}
	if cfg.RerankerModel != "llama3.2" {
		t.Errorf("expected reranker model llama3.2, got %s", cfg.RerankerModel)
	}
	if !cfg.SummarizerEnabled {
		t.Errorf("expected summarizer enabled")
	}
	if cfg.SummarizerModel != "mistral" {
		t.Errorf("expected summarizer model mistral, got %s", cfg.SummarizerModel)
	}
}

func TestValidate_RerankerTypeInvalid(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.RerankerType = "unknown-reranker"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid reranker type")
	}
}

func TestValidate_RerankerCrossEncoderWithoutModel(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.RerankerType = "cross-encoder"
	cfg.RerankerModel = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for cross-encoder reranker without model")
	}
}

func TestValidate_RerankerAllowsSharedLLMModel(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.RerankerType = "cross-encoder"
	cfg.LLMModel = "shared-llm-model"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected shared llm model to satisfy reranker validation, got %v", err)
	}
}

func TestValidate_SummarizerWithoutModel(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.SummarizerEnabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for summarizer without model")
	}
}

func TestValidate_SummarizerAllowsSharedLLMModel(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.SummarizerEnabled = true
	cfg.LLMModel = "shared-llm-model"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected shared llm model to satisfy summarizer validation, got %v", err)
	}
}

func TestValidate_InvalidLLMProvider(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.WatchDir = dir
	cfg.LLMProvider = "custom"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid llm provider")
	}
}
