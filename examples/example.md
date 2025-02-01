# Examples

This document provides examples of how to use the high-performance web framework. We'll demonstrate various features using curl, wscat, and other tools.

## Table of Contents
- [HTTP Endpoints](#http-endpoints)
- [WebSocket Examples](#websocket-examples)
- [Middleware Examples](#middleware-examples)
- [Rate Limiting Examples](#rate-limiting-examples)
- [Error Handling Examples](#error-handling-examples)

## HTTP Endpoints

### Basic GET Request
```bash
# Test the ping endpoint
curl http://localhost:8080/ping
# Expected response: pong

# Test the status endpoint
curl http://localhost:8080/api/v1/status
# Expected response: {"status":"operational","metrics":{...}}
```

### POST Request with JSON Data
```bash
# Send data to the API endpoint
curl -X POST http://localhost:8080/api/data \
  -H "Content-Type: application/json" \
  -d '{"message": "Hello, World!"}'

# Expected response:
# {
#   "status": "success",
#   "data": "Hello, World!"
# }
```

### Testing Error Endpoints
```bash
# Test error handling
curl http://localhost:8080/error
# Expected response: HTTP 400 with error message
```

## WebSocket Examples

### Using wscat
First, install wscat:
```bash
npm install -g wscat
```

Connect to the WebSocket endpoint:
```bash
# Connect to the chat endpoint
wscat -c ws://localhost:8080/ws/chat

# Once connected, you can send messages:
> {"message": "Hello, everyone!"}

# You should receive the echo back:
< {"message": "Hello, everyone!"}
```

### Using Browser WebSocket
```javascript
// JavaScript code for browser
const ws = new WebSocket('ws://localhost:8080/ws/chat');

ws.onopen = () => {
    console.log('Connected to WebSocket');
    ws.send(JSON.stringify({
        message: 'Hello from browser!'
    }));
};

ws.onmessage = (event) => {
    console.log('Received:', event.data);
};

ws.onclose = () => {
    console.log('Disconnected from WebSocket');
};
```

## Middleware Examples

### Testing CORS
```bash
# Test CORS preflight request
curl -X OPTIONS http://localhost:8080/api/protected \
  -H "Origin: http://example.com" \
  -H "Access-Control-Request-Method: POST" \
  -v

# Make a cross-origin request
curl -X POST http://localhost:8080/api/protected \
  -H "Origin: http://example.com" \
  -H "Content-Type: application/json" \
  -d '{"data": "test"}' \
  -v
```

### Testing Request ID Middleware
```bash
# Make a request and observe the X-Request-ID header
curl -v http://localhost:8080/hello

# Make a request with your own Request ID
curl -v http://localhost:8080/hello \
  -H "X-Request-ID: custom-id-123"
```

## Rate Limiting Examples

### Testing Rate Limits
```bash
# Make multiple requests quickly to trigger rate limiting
for i in {1..20}; do 
    curl -v http://localhost:8080/api/protected
    sleep 0.1
done

# Observe rate limit headers in the response:
# X-RateLimit-Limit: 10
# X-RateLimit-Remaining: 9
# X-RateLimit-Reset: 1612345678
```

### Testing Different Rate Limit Groups
```bash
# Test API endpoint (stricter rate limit)
curl -v http://localhost:8080/api/protected

# Test regular endpoint (more lenient rate limit)
curl -v http://localhost:8080/hello
```

## Error Handling Examples

### Testing Different Error Scenarios
```bash
# Test 404 Not Found
curl -v http://localhost:8080/nonexistent

# Test method not allowed
curl -X POST http://localhost:8080/ping

# Test validation error
curl -X POST http://localhost:8080/api/data \
  -H "Content-Type: application/json" \
  -d '{"invalid": "data"}'
```

## Load Testing Example

Using [hey](https://github.com/rakyll/hey) for load testing:

```bash
# Install hey
go install github.com/rakyll/hey@latest

# Run a load test (200 requests, 10 concurrent)
hey -n 200 -c 10 http://localhost:8080/ping

# Run a WebSocket load test (100 connections, 5 concurrent)
hey -n 100 -c 5 -ws ws://localhost:8080/ws/chat
```

## Complete Application Example

Here's a complete example that demonstrates multiple features:

```go
package main

import (
    "github.com/baditaflorin/l"
    "github.com/baditaflorin/r"
    "os"
    "time"
)

func main() {
    // Configure logger
    logger, err := l.NewStandardFactory().CreateLogger(l.Config{
        Output:     os.Stdout,
        JsonFormat: true,
    })
    if err != nil {
        panic(err)
    }
    defer logger.Close()

    // Create router
    router := r.NewRouter()

    // Create middleware provider
    middlewareProvider := r.NewMiddlewareProvider(router.(*r.RouterImpl))

    // Add middleware
    router.Use(
        middlewareProvider.RequestID(),
        middlewareProvider.Logger("%s | %3d | %13v | %15s | %s"),
        middlewareProvider.Security(),
        middlewareProvider.RateLimit(100, time.Minute),
        middlewareProvider.CORS([]string{"*"}),
    )

    // Add routes
    router.GET("/hello", func(ctx r.Context) {
        ctx.JSON(200, map[string]interface{}{
            "message": "Hello World!",
            "request_id": ctx.RequestID(),
        })
    })

    // Add API group with stricter rate limit
    api := router.Group("/api")
    api.Use(
        middlewareProvider.RateLimit(10, time.Minute),
    )

    api.GET("/protected", func(ctx r.Context) {
        ctx.JSON(200, map[string]interface{}{
            "message": "Protected endpoint!",
            "request_id": ctx.RequestID(),
        })
    })

    // Add WebSocket handler
    chatHandler := &ChatHandler{logger: logger}
    router.WS("/ws/chat", chatHandler)

    // Create and start server
    server := r.NewServer(r.Config{
        Handler: router,
        ReadTimeout: 15 * time.Second,
        WriteTimeout: 15 * time.Second,
    })

    logger.Info("Starting server on :8080")
    if err := server.Start(":8080"); err != nil {
        logger.Error("Server failed", "error", err)
        panic(err)
    }
}

type ChatHandler struct {
    logger l.Logger
}

func (h *ChatHandler) OnConnect(conn r.WSConnection) {
    h.logger.Info("Client connected",
        "client_id", conn.ID(),
        "remote_addr", conn.RemoteAddr(),
    )
}

func (h *ChatHandler) OnMessage(conn r.WSConnection, msg []byte) {
    h.logger.Debug("Received message",
        "client_id", conn.ID(),
        "message_size", len(msg),
    )
    // Echo the message back
    if err := conn.WriteMessage(1, msg); err != nil {
        h.logger.Error("Failed to write message",
            "error", err,
            "client_id", conn.ID(),
        )
    }
}

func (h *ChatHandler) OnClose(conn r.WSConnection) {
    h.logger.Info("Client disconnected",
        "client_id", conn.ID(),
    )
}
```

To test the complete application:

1. Start the server:
```bash
go run main.go
```

2. Test HTTP endpoints:
```bash
# Test hello endpoint
curl http://localhost:8080/hello

# Test protected endpoint
curl http://localhost:8080/api/protected
```

3. Test WebSocket:
```bash
# Connect with wscat
wscat -c ws://localhost:8080/ws/chat

# Send messages
> {"message": "Hello"}
```

4. Monitor logs in stdout for JSON-formatted logs showing all requests and WebSocket activity.

## Best Practices

1. Always use proper error handling:
```go
if err := ctx.JSON(200, data); err != nil {
    ctx.AbortWithError(500, err)
    return
}
```

2. Set appropriate timeouts:
```go
server := r.NewServer(r.Config{
    ReadTimeout: 15 * time.Second,
    WriteTimeout: 15 * time.Second,
    IdleTimeout: 60 * time.Second,
})
```

3. Use structured logging:
```go
logger.Info("Processing request",
    "request_id", ctx.RequestID(),
    "method", ctx.Method(),
    "path", ctx.Path(),
)
```

4. Implement graceful shutdown:
```go
c := make(chan os.Signal, 1)
signal.Notify(c, os.Interrupt)
go func() {
    <-c
    server.Stop()
}()
```