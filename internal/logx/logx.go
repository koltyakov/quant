package logx

import (
	"log"
	"log/slog"
)

type stdWriter struct{}

func (stdWriter) Write(p []byte) (int, error) {
	return log.Writer().Write(p)
}

var logger = slog.New(slog.NewTextHandler(stdWriter{}, &slog.HandlerOptions{}))

func Debug(msg string, args ...any) {
	logger.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	logger.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	logger.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	logger.Error(msg, args...)
}
