package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

type Logger struct {
	mu         sync.Mutex
	out        io.Writer
	level      Level
	timestamps bool
}

var std = &Logger{
	out:        os.Stderr,
	level:      LevelInfo,
	timestamps: false,
}

func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func SetLevel(l Level) { std.level = l }

func SetTimestamps(enabled bool) { std.timestamps = enabled }

func Debug(format string, args ...any) { std.log(LevelDebug, "DEBUG", format, args...) }

func Info(format string, args ...any) { std.log(LevelInfo, "INFO", format, args...) }

func Warn(format string, args ...any) { std.log(LevelWarn, "WARN", format, args...) }

func Error(format string, args ...any) { std.log(LevelError, "ERROR", format, args...) }

func (l *Logger) log(level Level, label, format string, args ...any) {
	if level < l.level {
		return
	}

	msg := fmt.Sprintf(format, args...)

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.timestamps {
		fmt.Fprintf(l.out, "%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339), label, msg)
	} else {
		fmt.Fprintf(l.out, "[%s] %s\n", label, msg)
	}
}
