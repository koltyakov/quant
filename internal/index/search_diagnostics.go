package index

import "context"

// RetrievalDiagnostics describes this request, not embedding backend health.
// Complete means the configured retrieval strategy ran, not exhaustive recall.
type RetrievalDiagnostics struct {
	Semantic      string   `json:"semantic"`
	BudgetSkipped bool     `json:"budget_skipped,omitempty"`
	Errors        []string `json:"errors,omitempty"`
	RerankError   string   `json:"rerank_error,omitempty"`
}

type retrievalDiagnosticsKey struct{}

// WithRetrievalDiagnostics attaches a request-local diagnostics sink. A backend
// that does not support diagnostics leaves Semantic as "unknown". Do not share
// the sink between concurrent requests.
func WithRetrievalDiagnostics(ctx context.Context) (context.Context, *RetrievalDiagnostics) {
	d := &RetrievalDiagnostics{Semantic: "unknown"}
	return context.WithValue(ctx, retrievalDiagnosticsKey{}, d), d
}

// RecordRetrievalDiagnostics copies remote diagnostics into the caller's sink.
// Missing diagnostics from an older backend leave the sink unchanged.
func RecordRetrievalDiagnostics(ctx context.Context, diagnostics *RetrievalDiagnostics) {
	if diagnostics == nil {
		return
	}
	if d, ok := ctx.Value(retrievalDiagnosticsKey{}).(*RetrievalDiagnostics); ok {
		*d = *diagnostics
		d.Errors = append([]string(nil), diagnostics.Errors...)
	}
}

func semanticStatus(ctx context.Context, status string, err error) {
	if d, ok := ctx.Value(retrievalDiagnosticsKey{}).(*RetrievalDiagnostics); ok {
		if status == "budget_skipped" {
			d.BudgetSkipped = true
		}
		if d.Semantic != "failed" {
			d.Semantic = status
		}
		if err != nil {
			d.Errors = append(d.Errors, err.Error())
		}
	}
}
