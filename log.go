package r

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"time"
)

type LogEntry struct {
	Level      LogLevel       `json:"level"`
	Message    string         `json:"message"`
	Time       time.Time      `json:"time"`
	Fields     map[string]any `json:"fields,omitempty"`
	Method     string         `json:"method,omitempty"`
	Status     int            `json:"status,omitempty"`
	Latency    time.Duration  `json:"latency,omitempty"`
	IP         string         `json:"ip,omitempty"`
	Path       string         `json:"path,omitempty"`
	StackTrace string         `json:"stack_trace,omitempty"`
}

// structuredLogger implements the Logger interface with enhanced features
type structuredLogger struct {
	output     io.Writer
	level      LogLevel
	fields     map[string]any
	fieldsMu   sync.RWMutex
	bufferPool sync.Pool
}

func NewStructuredLogger(output io.Writer, level LogLevel) Logger {
	return &structuredLogger{
		output: output,
		level:  level,
		fields: make(map[string]any),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return new(LogEntry)
			},
		},
	}
}

func NewDefaultLogger() Logger {
	return &structuredLogger{
		output: io.Discard,
		level:  InfoLevel,
		fields: make(map[string]any),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return new(LogEntry)
			},
		},
	}
}

func (l *structuredLogger) Log(method string, status int, latency time.Duration, ip, path string) {
	entry := l.bufferPool.Get().(*LogEntry)
	defer l.bufferPool.Put(entry)

	*entry = LogEntry{
		Level:   InfoLevel,
		Time:    time.Now(),
		Method:  method,
		Status:  status,
		Latency: latency,
		IP:      ip,
		Path:    path,
		Fields:  l.getFields(),
	}

	l.writeEntry(entry)
}

func (l *structuredLogger) Info(msg string, args ...interface{}) {
	entry := l.bufferPool.Get().(*LogEntry)
	defer l.bufferPool.Put(entry)

	*entry = LogEntry{
		Level:   InfoLevel,
		Message: msg, // Keep raw message
		Time:    time.Now(),
		Fields:  l.getFields(),
	}

	// Efficiently append structured log arguments
	if len(args) > 0 {
		for i := 0; i < len(args); i += 2 {
			if i+1 < len(args) {
				entry.Fields[fmt.Sprint(args[i])] = args[i+1]
			}
		}
	}

	l.writeEntry(entry)
}

func (l *structuredLogger) Error(msg string, args ...interface{}) {
	entry := l.bufferPool.Get().(*LogEntry)
	defer l.bufferPool.Put(entry)

	*entry = LogEntry{
		Level:      ErrorLevel,
		Message:    fmt.Sprintf(msg, args...),
		Time:       time.Now(),
		Fields:     l.getFields(),
		StackTrace: string(debug.Stack()),
	}

	l.writeEntry(entry)
}

func (l *structuredLogger) Debug(msg string, args ...interface{}) {
	if l.level > DebugLevel {
		return
	}

	entry := l.bufferPool.Get().(*LogEntry)
	defer l.bufferPool.Put(entry)

	*entry = LogEntry{
		Level:   DebugLevel,
		Message: fmt.Sprintf(msg, args...),
		Time:    time.Now(),
		Fields:  l.getFields(),
	}

	l.writeEntry(entry)
}

func (l *structuredLogger) Warn(msg string, args ...interface{}) {
	entry := l.bufferPool.Get().(*LogEntry)
	defer l.bufferPool.Put(entry)

	*entry = LogEntry{
		Level:   WarnLevel,
		Message: fmt.Sprintf(msg, args...),
		Time:    time.Now(),
		Fields:  l.getFields(),
	}

	l.writeEntry(entry)
}

func (l *structuredLogger) WithField(key string, value interface{}) Logger {
	newLogger := &structuredLogger{
		output:     l.output,
		level:      l.level,
		fields:     l.cloneFields(),
		bufferPool: l.bufferPool,
	}
	newLogger.fields[key] = value
	return newLogger
}

func (l *structuredLogger) WithFields(fields map[string]interface{}) Logger {
	newLogger := &structuredLogger{
		output:     l.output,
		level:      l.level,
		fields:     l.cloneFields(),
		bufferPool: l.bufferPool,
	}
	for k, v := range fields {
		newLogger.fields[k] = v
	}
	return newLogger
}

func (l *structuredLogger) WithError(err error) Logger {
	return l.WithField("error", err.Error())
}

func (l *structuredLogger) With(key string, value interface{}) Logger {
	return l.WithField(key, value)
}

func (l *structuredLogger) Configure(config LogConfig) error {
	l.output = config.Output
	l.level = config.Level
	return nil
}

// Helper methods
func (l *structuredLogger) cloneFields() map[string]interface{} {
	l.fieldsMu.RLock()
	defer l.fieldsMu.RUnlock()

	newFields := make(map[string]interface{}, len(l.fields))
	for k, v := range l.fields {
		newFields[k] = v
	}
	return newFields
}

func (l *structuredLogger) getFields() map[string]interface{} {
	l.fieldsMu.RLock()
	defer l.fieldsMu.RUnlock()
	return l.fields
}

func (l *structuredLogger) writeEntry(entry *LogEntry) {
	if l.output == nil {
		return
	}

	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Printf("Error marshaling log entry: %v\n", err)
		return
	}

	data = append(data, '\n')
	_, err = l.output.Write(data)
	if err != nil {
		fmt.Printf("Error writing log entry: %v\n", err)
	}
}
