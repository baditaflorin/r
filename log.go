package r

import (
	"fmt"
	"io"
	"time"
)

// LogConfig defines configuration options for logging
type LogConfig struct {
	Output      io.Writer
	FilePath    string
	JsonFormat  bool
	AsyncWrite  bool
	BufferSize  int
	MaxFileSize int
	MaxBackups  int
	AddSource   bool
	Metrics     bool
	Level       LogLevel
}

// LogLevel represents the severity of a log entry
type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
)

func (l LogLevel) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	case FatalLevel:
		return "fatal"
	default:
		return "unknown"
	}
}

// Logger interface defines common logging methods
type Logger interface {
	WithField(key string, value interface{}) Logger
	WithFields(fields map[string]interface{}) Logger
	WithError(err error) Logger
	With(key string, value interface{}) Logger // Add this method
	Configure(config LogConfig) error
	Log(method string, status int, latency time.Duration, ip, path string)
	Info(msg string, args ...interface{})
	Error(msg string, args ...interface{})
	Debug(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
}

// defaultLogger is a basic implementation of the Logger interface
type defaultLogger struct{}

func NewDefaultLogger() Logger {
	return &defaultLogger{}
}

func (l *defaultLogger) WithField(key string, value interface{}) Logger {
	return l
}

func (l *defaultLogger) WithFields(fields map[string]interface{}) Logger {
	return l
}

func (l *defaultLogger) WithError(err error) Logger {
	return l
}

func (l *defaultLogger) Configure(config LogConfig) error {
	return nil
}

func (l *defaultLogger) Log(method string, status int, latency time.Duration, ip, path string) {
	fmt.Printf("%s | %3d | %13v | %15s | %s\n", method, status, latency, ip, path)
}

func (l *defaultLogger) Info(msg string, args ...interface{}) {
	fmt.Printf("INFO: "+msg+"\n", args...)
}

func (l *defaultLogger) Error(msg string, args ...interface{}) {
	fmt.Printf("ERROR: "+msg+"\n", args...)
}

func (l *defaultLogger) Debug(msg string, args ...interface{}) {
	fmt.Printf("DEBUG: "+msg+"\n", args...)
}

func (l *defaultLogger) Warn(msg string, args ...interface{}) {
	fmt.Printf("WARN: "+msg+"\n", args...)
}

func (l *defaultLogger) With(key string, value interface{}) Logger {
	return l.WithField(key, value)
}
