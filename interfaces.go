package r

import (
	"io"
	"time"
)

// LogLevel represents the severity of a log entry
type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
	FatalLevel
)

// Logger interface defines common logging methods
type Logger interface {
	WithField(key string, value interface{}) Logger
	WithFields(fields map[string]interface{}) Logger
	WithError(err error) Logger
	With(key string, value interface{}) Logger
	Configure(config LogConfig) error
	Log(method string, status int, latency time.Duration, ip, path string)
	Info(msg string, args ...interface{})
	Error(msg string, args ...interface{})
	Debug(msg string, args ...interface{})
	Warn(msg string, args ...interface{})
}

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
