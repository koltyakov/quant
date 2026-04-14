package chunk

type SemanticChunker struct {
	SimilarityThreshold float64
	MinChunkSize        int
	MaxChunkSize        int
}

func (s *SemanticChunker) Name() string { return "semantic" }

func (s *SemanticChunker) Supports(path string) bool { return false }

func (s *SemanticChunker) Priority() int { return -10 }

func (s *SemanticChunker) Split(text string, chunkSize int, overlapFraction float64) []Chunk {
	return Split(text, chunkSize, overlapFraction)
}

type SemanticBoundary struct {
	Position int
	Score    float64
}

func FindSemanticBoundaries(paragraphs []string, getSimilarity func(a, b string) float64, threshold float64) []SemanticBoundary {
	if len(paragraphs) <= 1 {
		return nil
	}

	var boundaries []SemanticBoundary
	for i := 1; i < len(paragraphs); i++ {
		sim := getSimilarity(paragraphs[i-1], paragraphs[i])
		if sim < threshold {
			pos := 0
			for j := 0; j < i; j++ {
				pos += len(paragraphs[j]) + 1
			}
			boundaries = append(boundaries, SemanticBoundary{Position: pos, Score: sim})
		}
	}
	return boundaries
}
