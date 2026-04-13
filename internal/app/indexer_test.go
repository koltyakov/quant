package app

import (
	"errors"
	"testing"

	"github.com/koltyakov/quant/internal/embed"
	"github.com/koltyakov/quant/internal/extract"
)

func TestShouldRetryIndexError_DoesNotRetryOversizedExtractions(t *testing.T) {
	err := errors.Join(errors.New("extracting text"), extract.ErrFileTooLarge)

	if shouldRetryIndexError(err) {
		t.Fatal("expected oversized extraction errors to skip retries")
	}
	if !shouldQuarantineIndexError(err) {
		t.Fatal("expected oversized extraction errors to be quarantined")
	}
}

func TestShouldRetryIndexError_DoesNotRetryPermanentEmbedErrors(t *testing.T) {
	if shouldRetryIndexError(embed.ErrPermanent) {
		t.Fatal("expected permanent embed errors to skip retries")
	}
}
