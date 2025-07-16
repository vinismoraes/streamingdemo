# Go Streaming Client for FastAPI Server

This project provides robust Go clients for handling Server-Sent Events (SSE) streaming from a FastAPI server. It includes both basic and advanced implementations with comprehensive error handling, retry logic, and graceful stream management.

## Features

### Basic Client (`main.go`)
- Simple retry logic with exponential backoff
- Graceful error handling for stream failures
- Context cancellation support
- Signal handling for graceful shutdown
- Proper HTTP connection management

### Advanced Client (`advanced_client.go`)
- **Circuit Breaker Pattern**: Prevents cascading failures
- **Connection Pooling**: Efficient HTTP connection management
- **Detailed Metrics**: Track success rates, retry counts, and performance
- **Enhanced Error Handling**: Sophisticated error classification and recovery
- **Exponential Backoff with Jitter**: Prevents thundering herd problems
- **Atomic Operations**: Thread-safe metrics and state management

## Project Structure

```
streamingdemo/
├── main.go              # Basic streaming client
├── advanced_client.go   # Advanced client with circuit breaker
├── demo.go             # Demo functions for both clients
├── go.mod              # Go module dependencies
└── README.md           # This file
```

## Prerequisites

- Go 1.21 or later
- FastAPI server running on `http://localhost:8000` (or modify the URL in the code)

## Installation

1. Clone or download the project files
2. Install dependencies:
   ```bash
   go mod tidy
   ```

## Usage

### Running the Basic Client

```bash
go run .
```

### Running the Advanced Client

```bash
go run . advanced
```

### Using Demo Functions

You can also use the demo functions in your own code:

```go
package main

import "streamingdemo"

func main() {
    // Run basic client demo
    DemoBasicClient()

    // Or run advanced client demo
    DemoAdvancedClient()

    // Or run multiple concurrent streams
    DemoMultipleStreams()
}
```

## Configuration

### Basic Client Configuration

```go
baseURL := "http://localhost:8000"
timeout := 30 * time.Second
maxRetries := 3

client := NewStreamClient(baseURL, timeout)
```

### Advanced Client Configuration

```go
baseURL := "http://localhost:8000"
timeout := 30 * time.Second
maxRetries := 3

client := NewAdvancedStreamClient(baseURL, timeout)
```

## Error Handling

The clients handle various types of errors gracefully:

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

## Retry Logic

### Basic Client
- Simple retry with exponential backoff
- Configurable max retry attempts
- Skips retries for certain error types

### Advanced Client
- Circuit breaker pattern prevents cascading failures
- Exponential backoff with jitter
- Sophisticated error classification
- Automatic recovery after timeout

## Circuit Breaker Pattern

The advanced client implements a circuit breaker with three states:

1. **Closed**: Normal operation, requests pass through
2. **Open**: Requests are blocked due to high failure rate
3. **Half-Open**: Limited requests allowed to test recovery

Configuration:
- Failure threshold: 3 consecutive failures
- Timeout: 30 seconds before attempting recovery

## Metrics and Monitoring

The advanced client provides detailed metrics:

- Total events processed
- Successful events count
- Failed events count
- Retry attempts
- Stream duration
- Success rate percentage

Example output:
```
=== Streaming Metrics ===
Duration: 15.234s
Total Events: 6
Successful Events: 5
Failed Events: 1
Retry Count: 2
Success Rate: 83.33%
=======================
```

## Graceful Shutdown

Both clients support graceful shutdown:

1. **Signal Handling**: Responds to SIGINT and SIGTERM
2. **Context Cancellation**: Supports context-based cancellation
3. **Resource Cleanup**: Properly closes HTTP connections
4. **In-flight Request Handling**: Cancels ongoing requests

## Best Practices

### For Production Use

1. **Configure Timeouts**: Set appropriate timeouts based on your server's response times
2. **Monitor Metrics**: Use the advanced client's metrics for monitoring
3. **Handle Errors**: Implement proper error handling in your application
4. **Resource Management**: Always call `GracefulShutdown()` when done
5. **Connection Pooling**: Use the advanced client for high-throughput scenarios

### Error Recovery

```go
client := NewAdvancedStreamClient(baseURL, timeout)
defer client.GracefulShutdown()

ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
defer cancel()

err := client.StreamWithAdvancedRetry(ctx, requestBody, 3)
if err != nil {
    // Handle error appropriately
    log.Printf("Stream failed: %v", err)
    // Implement fallback logic or alerting
}
```

## Testing with FastAPI Server

Make sure your FastAPI server is running:

```python
# Your FastAPI server code
from fastapi import FastAPI
from fastapi.responses import StreamingResponse
# ... rest of your server code

# Run with: uvicorn main:app --reload
```

Then run the Go client:

```bash
# Basic client
go run .

# Advanced client
go run . advanced
```

## Troubleshooting

### Common Issues

1. **Connection Refused**: Ensure the FastAPI server is running
2. **Timeout Errors**: Increase the timeout duration
3. **Circuit Breaker Open**: Wait for recovery or restart the client
4. **JSON Parsing Errors**: Check server response format

### Debug Mode

Add debug logging by modifying the client code:

```go
// Add debug prints in the stream processing functions
fmt.Printf("Debug: Processing line: %s\n", line)
```

## Performance Considerations

- **Connection Pooling**: The advanced client reuses HTTP connections
- **Buffer Management**: Large message buffers prevent scanner errors
- **Concurrent Streams**: Use goroutines for multiple streams
- **Memory Management**: Proper cleanup prevents memory leaks

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

This project is provided as-is for educational and production use.