package logx

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestLogFunctionsDoNotPanic(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	Debug("debug msg")
	Info("info msg")
	Warn("warn msg")
	Error("error msg")

	output := buf.String()
	for _, msg := range []string{"info msg", "warn msg", "error msg"} {
		if !strings.Contains(output, msg) {
			t.Errorf("expected %q in output", msg)
		}
	}
}

func TestStdWriter(t *testing.T) {
	var sw stdWriter
	data := []byte("hello")
	n, err := sw.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Fatalf("expected %d bytes written, got %d", len(data), n)
	}
}
