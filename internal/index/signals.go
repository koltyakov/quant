package index

import (
	"math"
	"sort"
	"sync"
	"time"
)

// SearchSignal defines the interface for ranking signals in hybrid search.
// Implementations provide different ways to score search candidates.
type SearchSignal interface {
	// Name returns a human-readable name for this signal.
	Name() string

	// Score calculates this signal's contribution to the candidate's score.
	// The returned value should be in a consistent range (typically 0-1).
	Score(ctx *SignalContext, candidate *ScoredCandidate) float32

	// Weight returns the relative weight of this signal.
	// Higher weights increase this signal's influence on final ranking.
	Weight() float32
}

// SignalContext provides context for signal scoring.
type SignalContext struct {
	Query       string
	QueryTokens []string
	Now         time.Time
	HasKeyword  bool
	HasVector   bool
	Weights     QuerySignalWeights
}

// ScoredCandidate represents a search result being scored.
type ScoredCandidate = scoredCandidate

// SignalRegistry manages registered search signals.
type SignalRegistry struct {
	mu      sync.RWMutex
	signals []SearchSignal
}

// DefaultSignalRegistry is the global signal registry with built-in signals.
var DefaultSignalRegistry = NewSignalRegistry()

func init() {
	// Register built-in signals.
	DefaultSignalRegistry.Register(&KeywordSignal{})
	DefaultSignalRegistry.Register(&VectorSignal{})
	DefaultSignalRegistry.Register(&RecencySignal{HalfLife: recencyHalfLife})
	DefaultSignalRegistry.Register(&PathMatchSignal{})
}

// NewSignalRegistry creates a new empty signal registry.
func NewSignalRegistry() *SignalRegistry {
	return &SignalRegistry{
		signals: make([]SearchSignal, 0),
	}
}

// Register adds a signal to the registry.
func (r *SignalRegistry) Register(s SearchSignal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signals = append(r.signals, s)
}

// List returns all registered signals.
func (r *SignalRegistry) List() []SearchSignal {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]SearchSignal, len(r.signals))
	copy(result, r.signals)
	return result
}

// ApplySignals applies all registered signals to candidates and returns final scores.
func (r *SignalRegistry) ApplySignals(ctx *SignalContext, candidates []scoredCandidate) []scoredCandidate {
	r.mu.RLock()
	signals := make([]SearchSignal, len(r.signals))
	copy(signals, r.signals)
	r.mu.RUnlock()

	for i := range candidates {
		for _, signal := range signals {
			candidates[i].score += signal.Score(ctx, &candidates[i])
		}
	}
	return candidates
}

// KeywordSignal scores candidates based on keyword (FTS) rank.
type KeywordSignal struct {
	WeightOverride float32
}

func (s *KeywordSignal) Name() string { return "keyword" }

func (s *KeywordSignal) Weight() float32 {
	if s.WeightOverride > 0 {
		return s.WeightOverride
	}
	return 1.0
}

func (s *KeywordSignal) Score(ctx *SignalContext, candidate *ScoredCandidate) float32 {
	if candidate.keywordRank <= noKeywordRank {
		return 0
	}
	weight := ctx.Weights.Keyword
	if s.WeightOverride > 0 {
		weight = s.WeightOverride
	}
	return weight / float32(rrfK+candidate.keywordRank)
}

// VectorSignal scores candidates based on vector similarity rank.
type VectorSignal struct {
	WeightOverride float32
}

func (s *VectorSignal) Name() string { return "vector" }

func (s *VectorSignal) Weight() float32 {
	if s.WeightOverride > 0 {
		return s.WeightOverride
	}
	return 1.0
}

func (s *VectorSignal) Score(ctx *SignalContext, candidate *ScoredCandidate) float32 {
	if candidate.vectorRank <= 0 {
		return 0
	}
	weight := ctx.Weights.Vector
	if s.WeightOverride > 0 {
		weight = s.WeightOverride
	}
	return weight / float32(rrfK+candidate.vectorRank)
}

// RecencySignal boosts recently modified documents.
type RecencySignal struct {
	HalfLife       time.Duration
	WeightOverride float32
}

func (s *RecencySignal) Name() string { return "recency" }

func (s *RecencySignal) Weight() float32 {
	if s.WeightOverride > 0 {
		return s.WeightOverride
	}
	return recencyBoostWeight
}

func (s *RecencySignal) Score(ctx *SignalContext, candidate *ScoredCandidate) float32 {
	if candidate.modifiedAt.IsZero() {
		return 0
	}
	halfLife := s.HalfLife
	if halfLife == 0 {
		halfLife = recencyHalfLife
	}
	weight := s.Weight()
	age := ctx.Now.Sub(candidate.modifiedAt)
	if age < 0 {
		age = 0
	}
	decay := float32(fastExp(-0.693 * float64(age) / float64(halfLife)))
	return weight * decay / float32(rrfK+1)
}

// fastExp is a fast approximation of math.Exp for small negative values.
func fastExp(x float64) float64 {
	return math.Exp(x)
}

// PathMatchSignal boosts results where the document path contains query tokens.
type PathMatchSignal struct {
	WeightOverride float32
}

func (s *PathMatchSignal) Name() string { return "path_match" }

func (s *PathMatchSignal) Weight() float32 {
	if s.WeightOverride > 0 {
		return s.WeightOverride
	}
	return 1.0
}

func (s *PathMatchSignal) Score(ctx *SignalContext, candidate *ScoredCandidate) float32 {
	if len(ctx.QueryTokens) == 0 {
		return 0
	}
	lowerPath := toLower(candidate.result.DocumentPath)
	for _, tok := range ctx.QueryTokens {
		if containsString(lowerPath, tok) {
			return s.Weight() / float32(rrfK+1)
		}
	}
	return 0
}

// FileTypeSignal boosts specific file types (e.g., code files over documentation).
type FileTypeSignal struct {
	Extensions map[string]float32 // Extension -> boost factor
	Default    float32
}

func (s *FileTypeSignal) Name() string { return "file_type" }

func (s *FileTypeSignal) Weight() float32 { return 1.0 }

func (s *FileTypeSignal) Score(ctx *SignalContext, candidate *ScoredCandidate) float32 {
	path := candidate.result.DocumentPath
	ext := fileExtension(path)
	if boost, ok := s.Extensions[ext]; ok {
		return boost / float32(rrfK+1)
	}
	return s.Default / float32(rrfK+1)
}

// Helper functions that avoid import cycles
func toLower(s string) string {
	// Implemented inline to avoid import
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}

func containsString(s, substr string) bool {
	return len(substr) <= len(s) && searchString(s, substr) >= 0
}

func searchString(s, substr string) int {
	n := len(substr)
	if n == 0 {
		return 0
	}
	for i := 0; i <= len(s)-n; i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
}

func fileExtension(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/'; i-- {
		if path[i] == '.' {
			return toLower(path[i:])
		}
	}
	return ""
}

// RegisterSignal adds a custom signal to the default registry.
func RegisterSignal(s SearchSignal) {
	DefaultSignalRegistry.Register(s)
}

// CreateSignalContext creates a SignalContext for ranking operations.
func CreateSignalContext(query string, weights QuerySignalWeights, hasKeyword, hasVector bool) *SignalContext {
	return &SignalContext{
		Query:       query,
		QueryTokens: pathQueryTokens(query),
		Now:         time.Now(),
		HasKeyword:  hasKeyword,
		HasVector:   hasVector,
		Weights:     weights,
	}
}

// ApplySignalsToRanking applies all registered signals to candidates.
// This is the main entry point for signal-based ranking.
func ApplySignalsToRanking(candidates []scoredCandidate, ctx *SignalContext) []scoredCandidate {
	return DefaultSignalRegistry.ApplySignals(ctx, candidates)
}

// SortByScore sorts candidates by score in descending order.
func SortByScore(candidates []scoredCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
}
