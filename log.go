package r

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"time"
)

var (
	bytesPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}
)

// Add a pool for JSON encoding buffers at the top of log.go
var jsonBufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

var (
	mapPool = sync.Pool{
		New: func() interface{} {
			return make(map[string]interface{}, 10) // Preallocate with a reasonable size
		},
	}
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

// Initialize the pools (add these global variables at the top of log.go)
var (
	jsonEncoderPool = sync.Pool{New: func() interface{} { return json.NewEncoder(&bytes.Buffer{}) }}
)

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
		Fields:  l.cloneFields(),
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

	// Return the map to the pool after usage
	mapPool.Put(entry.Fields)
	entry.Fields = nil
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

	// Obtain a map from the pool
	newFields := mapPool.Get().(map[string]interface{})

	// Clear the map before reuse
	for k := range newFields {
		delete(newFields, k)
	}

	// Clone existing fields into the new map
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

	// Get a buffer from the pool
	buf := bytesPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bytesPool.Put(buf)

	// Use a pre-allocated JSON encoder buffer
	jsonBuf := jsonBufferPool.Get().(*bytes.Buffer)
	jsonBuf.Reset()
	defer jsonBufferPool.Put(jsonBuf)

	// Encode JSON efficiently
	encoder := json.NewEncoder(jsonBuf)
	if err := encoder.Encode(entry); err != nil {
		fmt.Printf("Error encoding log entry: %v\n", err)
		return
	}

	// Write encoded data to output
	_, err := l.output.Write(jsonBuf.Bytes())
	if err != nil {
		fmt.Printf("Error writing log entry: %v\n", err)
	}
}
