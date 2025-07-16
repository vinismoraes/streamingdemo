# Go Streaming Client for FastAPI Server

A robust, production-ready Go client for handling Server-Sent Events (SSE) streaming from a FastAPI server. Features circuit breaker pattern, comprehensive error handling, retry logic, and response data collection.

## 🚀 Features

- **Circuit Breaker Pattern**: Prevents cascading failures with automatic recovery
- **Response Data Collection**: Captures and returns all streamed events
- **Exponential Backoff**: Intelligent retry logic with jitter
- **Real-time Processing**: Events displayed as they arrive
- **Comprehensive Metrics**: Success rates, retry counts, performance tracking
- **Graceful Shutdown**: Signal handling and context cancellation
- **Connection Pooling**: Efficient HTTP connection management
- **Error Classification**: Different handling for different error types

## 📁 Project Structure

```
streamingdemo/
├── streaming_client.go    # Main streaming client (single file)
├── go.mod                # Go module dependencies
└── README.md             # This file
```

## 🛠️ Prerequisites

- Go 1.21 or later
- FastAPI server running on `http://localhost:8000` with `/trigger_invocation` endpoint

## ⚡ Quick Start

### 1. Install Dependencies
```bash
go mod tidy
```

### 2. Run the Client
```bash
go run streaming_client.go
```

### 3. Expected Output
```
Starting advanced stream to http://localhost:8000
Press Ctrl+C to stop
Event 1: Event 1
Event 2: Event 2
Event 3: Event 3
Event 4: Event 4
Message: Stream completed

=== Streaming Metrics ===
Duration: 26.79s
Total Events: 1
Successful: 1
Failed: 0
Retries: 0
Success Rate: 100.0%
=======================
Stream completed successfully

=== Collected Response Data ===
Event 1: Event 1
Event 2: Event 2
Event 3: Event 3
Event 4: Event 4
Message 1: Stream completed
Total responses collected: 5
==============================
```

## 🔧 Configuration

The client uses sensible defaults but can be customized:

```go
config := Config{
    BaseURL:    "http://localhost:8000",
    Timeout:    30 * time.Second,
    MaxRetries: 3,
    CircuitBreaker: CircuitBreakerConfig{
        FailureThreshold: 3,
        Timeout:          30 * time.Second,
    },
    HTTP: HTTPConfig{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}
```

## 📊 Response Data Collection

The client now **collects and returns all response data**:

```go
responses, err := service.StreamWithRetry(ctx, requestBody, config.MaxRetries)
if err != nil {
    // Handle error
}

// Access collected data
for i, response := range responses {
    if response.Count > 0 {
        fmt.Printf("Event %d: %s\n", response.Count, response.Data)
    } else {
        fmt.Printf("Message %d: %s\n", i+1, response.Data)
    }
}
```

## 🛡️ Error Handling

### Network Errors
- Connection timeouts
- DNS resolution failures
- Network unreachability

### Server Errors
- HTTP status errors (4xx, 5xx)
- Unexpected content types
- Malformed responses

### Stream Errors
- Server-sent error messages
- Stream cancellation
- Stream timeouts
- Unexpected stream closure

### Application Errors
- JSON parsing errors
- Context cancellation
- Signal interrupts (Ctrl+C)

## 🔄 Retry Logic

### Circuit Breaker States
1. **Closed**: Normal operation, requests pass through
2. **Open**: Requests blocked due to high failure rate
3. **Half-Open**: Limited requests allowed to test recovery

### Backoff Strategy
- **Exponential backoff**: `attempt²` seconds
- **Jitter**: `attempt * 500ms` to prevent thundering herd
- **Smart retry**: Skips retries for certain error types

## 📈 Metrics and Monitoring

The client provides detailed metrics:

- **Total Events**: Number of events processed
- **Successful Events**: Successfully processed events
- **Failed Events**: Failed event processing
- **Retry Count**: Number of retry attempts
- **Duration**: Total streaming time
- **Success Rate**: Percentage of successful events

## 🎯 Usage Examples

### Basic Usage
```go
config := DefaultConfig()
service := NewStreamService(config)
defer service.client.GracefulShutdown()

ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()

requestBody := BodyRequest{Query: "test query"}
responses, err := service.StreamWithRetry(ctx, requestBody, 3)

if err != nil {
    log.Printf("Stream failed: %v", err)
} else {
    fmt.Printf("Collected %d responses\n", len(responses))
}
```

### Custom Configuration
```go
config := Config{
    BaseURL:    "http://my-server:8080",
    Timeout:    60 * time.Second,
    MaxRetries: 5,
    CircuitBreaker: CircuitBreakerConfig{
        FailureThreshold: 5,
        Timeout:          60 * time.Second,
    },
}
```

## 🧪 Testing

### Test with Running Server
```bash
# Start your FastAPI server first
uvicorn main:app --reload

# Then run the Go client
go run streaming_client.go
```

### Test Error Scenarios
The client handles various failure scenarios:
- Server not running → Circuit breaker opens after 3 failures
- Network timeouts → Automatic retry with backoff
- Server shutdown mid-stream → Graceful error handling
- Context cancellation → Immediate shutdown

## 🔍 Server Shutdown Behavior

When the server shuts down mid-request:

1. **During HTTP request**: Connection error → Retry with backoff
2. **During stream processing**: EOF/connection reset → Retry with backoff
3. **After partial response**: JSON parsing may fail → Treated as failed event
4. **Circuit breaker protection**: Prevents overwhelming failing services

## 🚀 Production Features

- **Thread-safe**: Atomic operations for metrics and state
- **Resource management**: Proper connection cleanup
- **Signal handling**: Graceful shutdown on SIGINT/SIGTERM
- **Context support**: Cancellation and timeout handling
- **Memory efficient**: Streaming processing without buffering entire response

## 📝 API Reference

### Main Types
```go
type StreamResponse struct {
    Count int    `json:"Count,omitempty"`
    Data  string `json:"Data"`
    Error string `json:"error,omitempty"`
}

type BodyRequest struct {
    Query string `json:"query"`
}

type StreamClient struct {
    // HTTP client, circuit breaker, metrics, processor
}

type StreamService struct {
    client *StreamClient
}
```

### Key Methods
```go
// Create new service
service := NewStreamService(config)

// Stream with retry logic
responses, err := service.StreamWithRetry(ctx, requestBody, maxRetries)

// Get metrics
metrics := service.client.GetMetrics()

// Graceful shutdown
service.client.GracefulShutdown()
```

## 🐛 Troubleshooting

### Common Issues

1. **Connection Refused**
   - Ensure FastAPI server is running on `localhost:8000`
   - Check if port 8000 is available

2. **Circuit Breaker Open**
   - Server is down or overloaded
   - Wait for recovery timeout (30s default)
   - Check server health

3. **Timeout Errors**
   - Increase timeout in configuration
   - Check server response times

4. **JSON Parsing Errors**
   - Verify server response format
   - Check for malformed SSE data

### Debug Mode
Add debug logging by modifying the stream processing:
```go
fmt.Printf("Debug: Processing line: %s\n", line)
```

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## 📄 License

This project is provided as-is for educational and production use.

---

**Ready to use!** Just run `go run streaming_client.go` with your FastAPI server running. 🎉