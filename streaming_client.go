package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// StreamResponse represents the server response structure
type StreamResponse struct {
	Count int    `json:"Count,omitempty"`
	Data  string `json:"Data"`
	Error string `json:"error,omitempty"`
}

// BodyRequest represents the request body
type BodyRequest struct {
	Query string `json:"query"`
}

// Config holds client configuration
type Config struct {
	BaseURL        string
	Timeout        time.Duration
	MaxRetries     int
	CircuitBreaker CircuitBreakerConfig
	HTTP           HTTPConfig
}

// CircuitBreakerConfig holds circuit breaker settings
type CircuitBreakerConfig struct {
	FailureThreshold int64
	Timeout          time.Duration
}

// HTTPConfig holds HTTP client settings
type HTTPConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// DefaultConfig returns a default configuration
func DefaultConfig() Config {
	return Config{
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
}

// Metrics tracks streaming statistics
type Metrics struct {
	totalEvents   int64
	successEvents int64
	failedEvents  int64
	retryCount    int64
	startTime     time.Time
}

// CircuitBreaker implements circuit breaker pattern
type CircuitBreaker struct {
	failures    int64
	lastFailure time.Time
	state       int32 // 0: closed, 1: open, 2: half-open
	config      CircuitBreakerConfig
	mu          sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
	}
}

// StreamProcessor handles stream processing logic
type StreamProcessor interface {
	ProcessStream(ctx context.Context, body io.ReadCloser) ([]StreamResponse, error)
}

// HTTPClient interface for easier testing
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
	CloseIdleConnections()
}

// StreamClient provides robust streaming with circuit breaker and metrics
type StreamClient struct {
	client         HTTPClient
	baseURL        string
	timeout        time.Duration
	circuitBreaker *CircuitBreaker
	metrics        *Metrics
	processor      StreamProcessor
}

// NewStreamClient creates a new streaming client
func NewStreamClient(config Config) *StreamClient {
	transport := &http.Transport{
		MaxIdleConns:        config.HTTP.MaxIdleConns,
		MaxIdleConnsPerHost: config.HTTP.MaxIdleConnsPerHost,
		IdleConnTimeout:     config.HTTP.IdleConnTimeout,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}

	streamClient := &StreamClient{
		client:         client,
		baseURL:        config.BaseURL,
		timeout:        config.Timeout,
		circuitBreaker: NewCircuitBreaker(config.CircuitBreaker),
		metrics:        &Metrics{startTime: time.Now()},
		processor:      &DefaultStreamProcessor{},
	}

	return streamClient
}

// DefaultStreamProcessor implements StreamProcessor
type DefaultStreamProcessor struct{}

// ProcessStream handles Server-Sent Events processing
func (p *DefaultStreamProcessor) ProcessStream(ctx context.Context, body io.ReadCloser) ([]StreamResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var responses []StreamResponse

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return responses, fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		var response StreamResponse

		if err := json.Unmarshal([]byte(data), &response); err != nil {
			continue
		}

		if response.Error != "" {
			if strings.Contains(response.Error, "cancelled") ||
				strings.Contains(response.Error, "timed out") {
				return responses, fmt.Errorf("stream %s", response.Error)
			}
			return responses, fmt.Errorf("server error: %s", response.Error)
		}

		// Add response to collection
		responses = append(responses, response)

		if response.Count > 0 {
			fmt.Printf("Event %d: %s\n", response.Count, response.Data)
		} else {
			fmt.Printf("Message: %s\n", response.Data)
		}

		if strings.Contains(response.Data, "completed") {
			return responses, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return responses, fmt.Errorf("scanner error: %w", err)
	}

	return responses, fmt.Errorf("stream ended unexpectedly")
}

// StreamService handles the streaming business logic
type StreamService struct {
	client *StreamClient
}

// NewStreamService creates a new stream service
func NewStreamService(config Config) *StreamService {
	return &StreamService{
		client: NewStreamClient(config),
	}
}

// StreamWithRetry handles streaming with circuit breaker and retry logic
func (s *StreamService) StreamWithRetry(ctx context.Context, req BodyRequest, maxRetries int) ([]StreamResponse, error) {
	var lastErr error
	var lastResponses []StreamResponse

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if !s.client.circuitBreaker.canExecute() {
			return nil, fmt.Errorf("circuit breaker open")
		}

		if attempt > 0 {
			atomic.AddInt64(&s.client.metrics.retryCount, 1)
			backoff := s.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return lastResponses, fmt.Errorf("context cancelled: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		responses, err := s.client.stream(ctx, req)
		if err == nil {
			s.client.circuitBreaker.recordSuccess()
			return responses, nil
		}

		lastErr = err
		lastResponses = responses
		s.client.circuitBreaker.recordFailure()

		if ctx.Err() != nil || s.shouldNotRetry(err) {
			return lastResponses, err
		}
	}

	return lastResponses, fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// calculateBackoff calculates exponential backoff with jitter
func (s *StreamService) calculateBackoff(attempt int) time.Duration {
	baseBackoff := time.Duration(attempt*attempt) * time.Second
	jitter := time.Duration(attempt) * 500 * time.Millisecond
	return baseBackoff + jitter
}

// shouldNotRetry determines if an error should not be retried
func (s *StreamService) shouldNotRetry(err error) bool {
	errStr := err.Error()
	return strings.Contains(errStr, "cancelled") ||
		strings.Contains(errStr, "completed") ||
		strings.Contains(errStr, "circuit breaker")
}

// stream handles a single streaming session
func (c *StreamClient) stream(ctx context.Context, req BodyRequest) ([]StreamResponse, error) {
	jsonBody, err := json.Marshal(req)
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/trigger_invocation", bytes.NewBuffer(jsonBody))
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if err := c.validateResponse(resp); err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, err
	}

	return c.processStream(ctx, resp.Body)
}

// setHeaders sets the required headers for SSE
func (c *StreamClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
}

// validateResponse validates the HTTP response
func (c *StreamClient) validateResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		return fmt.Errorf("unexpected content type: %s", contentType)
	}

	return nil
}

// processStream delegates to the processor
func (c *StreamClient) processStream(ctx context.Context, body io.ReadCloser) ([]StreamResponse, error) {
	atomic.AddInt64(&c.metrics.totalEvents, 1)

	responses, err := c.processor.ProcessStream(ctx, body)
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, err
	}

	atomic.AddInt64(&c.metrics.successEvents, 1)
	return responses, nil
}

// Circuit breaker methods
func (cb *CircuitBreaker) canExecute() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	state := atomic.LoadInt32(&cb.state)
	switch state {
	case 0: // closed
		return true
	case 1: // open
		if time.Since(cb.lastFailure) > cb.config.Timeout {
			atomic.StoreInt32(&cb.state, 2) // half-open
			return true
		}
		return false
	case 2: // half-open
		return true
	default:
		return false
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	atomic.StoreInt32(&cb.state, 0)
	atomic.StoreInt64(&cb.failures, 0)
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	failures := atomic.AddInt64(&cb.failures, 1)
	cb.lastFailure = time.Now()

	if failures >= cb.config.FailureThreshold {
		atomic.StoreInt32(&cb.state, 1)
	}
}

// GetMetrics returns a copy of current metrics
func (c *StreamClient) GetMetrics() Metrics {
	return Metrics{
		totalEvents:   atomic.LoadInt64(&c.metrics.totalEvents),
		successEvents: atomic.LoadInt64(&c.metrics.successEvents),
		failedEvents:  atomic.LoadInt64(&c.metrics.failedEvents),
		retryCount:    atomic.LoadInt64(&c.metrics.retryCount),
		startTime:     c.metrics.startTime,
	}
}

// printMetrics displays streaming statistics
func (c *StreamClient) printMetrics() {
	metrics := c.GetMetrics()
	duration := time.Since(metrics.startTime)

	fmt.Printf("\n=== Streaming Metrics ===\n")
	fmt.Printf("Duration: %v\n", duration)
	fmt.Printf("Total Events: %d\n", metrics.totalEvents)
	fmt.Printf("Successful: %d\n", metrics.successEvents)
	fmt.Printf("Failed: %d\n", metrics.failedEvents)
	fmt.Printf("Retries: %d\n", metrics.retryCount)

	if metrics.totalEvents > 0 {
		successRate := float64(metrics.successEvents) / float64(metrics.totalEvents) * 100
		fmt.Printf("Success Rate: %.1f%%\n", successRate)
	}
	fmt.Printf("=======================\n")
}

// GracefulShutdown handles cleanup
func (c *StreamClient) GracefulShutdown() {
	c.client.CloseIdleConnections()
}

func main() {
	config := DefaultConfig()
	service := NewStreamService(config)
	defer service.client.GracefulShutdown()

	// Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nGraceful shutdown initiated...")
		cancel()
	}()

	// Start streaming
	requestBody := BodyRequest{Query: "advanced streaming test"}

	fmt.Printf("Starting advanced stream to %s\n", config.BaseURL)
	fmt.Println("Press Ctrl+C to stop")

	responses, err := service.StreamWithRetry(ctx, requestBody, config.MaxRetries)
	service.client.printMetrics()

	if err != nil {
		if ctx.Err() != nil {
			fmt.Printf("Stream cancelled: %v\n", err)
		} else {
			fmt.Printf("Stream failed: %v\n", err)
		}
		os.Exit(1)
	}

	fmt.Println("Stream completed successfully")

	// Display collected response data
	if len(responses) > 0 {
		fmt.Printf("\n=== Collected Response Data ===\n")
		for i, response := range responses {
			if response.Count > 0 {
				fmt.Printf("Event %d: %s\n", response.Count, response.Data)
			} else {
				fmt.Printf("Message %d: %s\n", i+1, response.Data)
			}
		}
		fmt.Printf("Total responses collected: %d\n", len(responses))
		fmt.Printf("==============================\n")
	} else {
		fmt.Println("No response data collected")
	}
}
