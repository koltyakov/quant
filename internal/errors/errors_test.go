package errors

import (
	stderrors "errors"
	"testing"
)

type testWrappedError struct {
	message string
}

func (e *testWrappedError) Error() string {
	return e.message
}

func TestErrorHelpers(t *testing.T) {
	t.Parallel()

	base := New("base")
	if base == nil || base.Error() != "base" {
		t.Fatalf("New() = %v, want base error", base)
	}

	joined := Join(base, ErrSearchFailed)
	if !Is(joined, base) {
		t.Fatal("Is(joined, base) = false, want true")
	}
	if !Is(joined, ErrSearchFailed) {
		t.Fatal("Is(joined, ErrSearchFailed) = false, want true")
	}

	wrapped := stderrors.Join(base, &testWrappedError{message: "typed"})
	var target *testWrappedError
	if !As(wrapped, &target) {
		t.Fatal("As(wrapped, *testWrappedError) = false, want true")
	}
	if target == nil || target.message != "typed" {
		t.Fatalf("As target = %#v, want typed error", target)
	}
}
