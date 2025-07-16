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

// ErrorType represents different categories of errors
type ErrorType int

const (
	ErrorTypeUnknown ErrorType = iota
	ErrorTypeNetwork
	ErrorTypeServer
	ErrorTypeClient
	ErrorTypeTimeout
	ErrorTypeCircuitBreaker
	ErrorTypeStreamClosed
	ErrorTypeInvalidResponse
)

// ErrorInfo contains detailed error information
type ErrorInfo struct {
	Type       ErrorType
	Message    string
	Retryable  bool
	StatusCode int
}

// String returns the string representation of ErrorType
func (et ErrorType) String() string {
	switch et {
	case ErrorTypeNetwork:
		return "Network"
	case ErrorTypeServer:
		return "Server"
	case ErrorTypeClient:
		return "Client"
	case ErrorTypeTimeout:
		return "Timeout"
	case ErrorTypeCircuitBreaker:
		return "CircuitBreaker"
	case ErrorTypeStreamClosed:
		return "StreamClosed"
	case ErrorTypeInvalidResponse:
		return "InvalidResponse"
	default:
		return "Unknown"
	}
}

// ClassifiedError wraps an error with classification information
type ClassifiedError struct {
	Err  error
	Info ErrorInfo
}

func (ce *ClassifiedError) Error() string {
	return fmt.Sprintf("[%s] %s", ce.Info.Type.String(), ce.Err.Error())
}

func (ce *ClassifiedError) Unwrap() error {
	return ce.Err
}

// HealthStatus represents server health information
type HealthStatus struct {
	Healthy      bool
	LastCheck    time.Time
	ResponseTime time.Duration
	StatusCode   int
	Error        error
}

// HealthChecker interface for health check functionality
type HealthChecker interface {
	CheckHealth(ctx context.Context) HealthStatus
	IsHealthy() bool
}

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
	HealthCheck    HealthCheckConfig
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

// HealthCheckConfig holds health check settings
type HealthCheckConfig struct {
	Enabled  bool
	Interval time.Duration
	Timeout  time.Duration
	Endpoint string
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
		HealthCheck: HealthCheckConfig{
			Enabled:  true,
			Interval: 30 * time.Second,
			Timeout:  5 * time.Second,
			Endpoint: "/health",
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
	errorCounts   map[ErrorType]int64
	mu            sync.RWMutex
}

// NewMetrics creates a new metrics instance
func NewMetrics() *Metrics {
	return &Metrics{
		startTime:   time.Now(),
		errorCounts: make(map[ErrorType]int64),
	}
}

// RecordError records an error by type
func (m *Metrics) RecordError(errorType ErrorType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorCounts[errorType]++
}

// GetErrorCounts returns a copy of error counts
func (m *Metrics) GetErrorCounts() map[ErrorType]int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	counts := make(map[ErrorType]int64)
	for k, v := range m.errorCounts {
		counts[k] = v
	}
	return counts
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
	healthChecker  HealthChecker
	config         Config
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
		metrics:        NewMetrics(),
		processor:      &DefaultStreamProcessor{},
		config:         config,
	}

	// Initialize health checker if enabled
	if config.HealthCheck.Enabled {
		streamClient.healthChecker = NewHealthChecker(streamClient, config.HealthCheck)
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
			return responses, &ClassifiedError{
				Err: fmt.Errorf("context cancelled: %w", ctx.Err()),
				Info: ErrorInfo{
					Type:      ErrorTypeStreamClosed,
					Message:   "Stream cancelled by context",
					Retryable: false,
				},
			}
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
				return responses, &ClassifiedError{
					Err: fmt.Errorf("stream %s", response.Error),
					Info: ErrorInfo{
						Type:      ErrorTypeStreamClosed,
						Message:   response.Error,
						Retryable: false,
					},
				}
			}
			return responses, &ClassifiedError{
				Err: fmt.Errorf("server error: %s", response.Error),
				Info: ErrorInfo{
					Type:      ErrorTypeServer,
					Message:   response.Error,
					Retryable: true,
				},
			}
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
		return responses, &ClassifiedError{
			Err: fmt.Errorf("scanner error: %w", err),
			Info: ErrorInfo{
				Type:      ErrorTypeStreamClosed,
				Message:   "Scanner error during stream processing",
				Retryable: true,
			},
		}
	}

	return responses, &ClassifiedError{
		Err: fmt.Errorf("stream ended unexpectedly"),
		Info: ErrorInfo{
			Type:      ErrorTypeStreamClosed,
			Message:   "Stream ended without completion",
			Retryable: true,
		},
	}
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
			err := &ClassifiedError{
				Err: fmt.Errorf("circuit breaker open"),
				Info: ErrorInfo{
					Type:      ErrorTypeCircuitBreaker,
					Message:   "Circuit breaker is open",
					Retryable: false,
				},
			}
			s.client.metrics.RecordError(ErrorTypeCircuitBreaker)
			return nil, err
		}

		if attempt > 0 {
			atomic.AddInt64(&s.client.metrics.retryCount, 1)
			backoff := s.calculateBackoff(attempt)
			select {
			case <-ctx.Done():
				return lastResponses, &ClassifiedError{
					Err: fmt.Errorf("context cancelled: %w", ctx.Err()),
					Info: ErrorInfo{
						Type:      ErrorTypeStreamClosed,
						Message:   "Context cancelled during retry",
						Retryable: false,
					},
				}
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

		// Classify and record the error
		if classifiedErr, ok := err.(*ClassifiedError); ok {
			s.client.metrics.RecordError(classifiedErr.Info.Type)
		} else {
			s.client.metrics.RecordError(ErrorTypeUnknown)
		}

		if ctx.Err() != nil || s.shouldNotRetry(err) {
			return lastResponses, err
		}
	}

	return lastResponses, &ClassifiedError{
		Err: fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr),
		Info: ErrorInfo{
			Type:      ErrorTypeUnknown,
			Message:   "Max retries exceeded",
			Retryable: false,
		},
	}
}

// calculateBackoff calculates exponential backoff with jitter
func (s *StreamService) calculateBackoff(attempt int) time.Duration {
	baseBackoff := time.Duration(attempt*attempt) * time.Second
	jitter := time.Duration(attempt) * 500 * time.Millisecond
	return baseBackoff + jitter
}

// shouldNotRetry determines if an error should not be retried
func (s *StreamService) shouldNotRetry(err error) bool {
	if classifiedErr, ok := err.(*ClassifiedError); ok {
		return !classifiedErr.Info.Retryable
	}

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
		return nil, &ClassifiedError{
			Err: fmt.Errorf("failed to marshal request: %w", err),
			Info: ErrorInfo{
				Type:      ErrorTypeClient,
				Message:   "Request marshaling failed",
				Retryable: false,
			},
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/trigger_invocation", bytes.NewBuffer(jsonBody))
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)
		return nil, &ClassifiedError{
			Err: fmt.Errorf("failed to create request: %w", err),
			Info: ErrorInfo{
				Type:      ErrorTypeClient,
				Message:   "Request creation failed",
				Retryable: false,
			},
		}
	}

	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		atomic.AddInt64(&c.metrics.failedEvents, 1)

		// Classify network errors
		errorType := ErrorTypeNetwork
		if strings.Contains(err.Error(), "timeout") {
			errorType = ErrorTypeTimeout
		}

		return nil, &ClassifiedError{
			Err: fmt.Errorf("request failed: %w", err),
			Info: ErrorInfo{
				Type:      errorType,
				Message:   "HTTP request failed",
				Retryable: true,
			},
		}
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

		errorType := ErrorTypeServer
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			errorType = ErrorTypeClient
		}

		return &ClassifiedError{
			Err: fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes)),
			Info: ErrorInfo{
				Type:       errorType,
				Message:    fmt.Sprintf("HTTP %d error", resp.StatusCode),
				Retryable:  resp.StatusCode >= 500,
				StatusCode: resp.StatusCode,
			},
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		return &ClassifiedError{
			Err: fmt.Errorf("unexpected content type: %s", contentType),
			Info: ErrorInfo{
				Type:      ErrorTypeInvalidResponse,
				Message:   "Invalid content type",
				Retryable: false,
			},
		}
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
		errorCounts:   c.metrics.GetErrorCounts(),
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

	// Print error breakdown
	if len(metrics.errorCounts) > 0 {
		fmt.Printf("\nError Breakdown:\n")
		for errorType, count := range metrics.errorCounts {
			fmt.Printf("  %s: %d\n", errorType.String(), count)
		}
	}
	fmt.Printf("=======================\n")
}

// GracefulShutdown handles cleanup
func (c *StreamClient) GracefulShutdown() {
	c.client.CloseIdleConnections()
}

// HealthChecker implementation
type DefaultHealthChecker struct {
	client *StreamClient
	config HealthCheckConfig
	status HealthStatus
	mu     sync.RWMutex
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(client *StreamClient, config HealthCheckConfig) *DefaultHealthChecker {
	return &DefaultHealthChecker{
		client: client,
		config: config,
		status: HealthStatus{Healthy: false},
	}
}

// CheckHealth performs a health check
func (hc *DefaultHealthChecker) CheckHealth(ctx context.Context) HealthStatus {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, "GET", hc.client.baseURL+hc.config.Endpoint, nil)
	if err != nil {
		hc.updateStatus(HealthStatus{
			Healthy:   false,
			LastCheck: time.Now(),
			Error:     err,
		})
		return hc.getStatus()
	}

	resp, err := hc.client.client.Do(req)
	if err != nil {
		hc.updateStatus(HealthStatus{
			Healthy:      false,
			LastCheck:    time.Now(),
			ResponseTime: time.Since(start),
			Error:        err,
		})
		return hc.getStatus()
	}
	defer resp.Body.Close()

	healthy := resp.StatusCode == http.StatusOK
	hc.updateStatus(HealthStatus{
		Healthy:      healthy,
		LastCheck:    time.Now(),
		ResponseTime: time.Since(start),
		StatusCode:   resp.StatusCode,
		Error:        nil,
	})

	return hc.getStatus()
}

// IsHealthy returns the current health status
func (hc *DefaultHealthChecker) IsHealthy() bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.status.Healthy
}

// updateStatus updates the health status
func (hc *DefaultHealthChecker) updateStatus(status HealthStatus) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	hc.status = status
}

// getStatus returns a copy of the current status
func (hc *DefaultHealthChecker) getStatus() HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.status
}

// GetHealthStatus returns the current health status
func (c *StreamClient) GetHealthStatus() HealthStatus {
	if c.healthChecker == nil {
		return HealthStatus{Healthy: false, Error: fmt.Errorf("health checker not enabled")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.config.HealthCheck.Timeout)
	defer cancel()

	return c.healthChecker.CheckHealth(ctx)
}

// IsHealthy returns whether the server is healthy
func (c *StreamClient) IsHealthy() bool {
	if c.healthChecker == nil {
		return false
	}
	return c.healthChecker.IsHealthy()
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

	// Check server health before starting
	if config.HealthCheck.Enabled {
		fmt.Printf("Checking server health at %s%s...\n", config.BaseURL, config.HealthCheck.Endpoint)
		healthStatus := service.client.GetHealthStatus()

		if healthStatus.Error != nil {
			fmt.Printf("Health check failed: %v\n", healthStatus.Error)
			fmt.Println("Proceeding anyway...")
		} else {
			fmt.Printf("Server health: %t (Response time: %v, Status: %d)\n",
				healthStatus.Healthy, healthStatus.ResponseTime, healthStatus.StatusCode)
		}
	}

	// Start streaming
	requestBody := BodyRequest{Query: "advanced streaming test"}

	fmt.Printf("Starting advanced stream to %s\n", config.BaseURL)
	fmt.Println("Press Ctrl+C to stop")

	responses, err := service.StreamWithRetry(ctx, requestBody, config.MaxRetries)
	service.client.printMetrics()

	if err != nil {
		if classifiedErr, ok := err.(*ClassifiedError); ok {
			fmt.Printf("Stream failed with %s error: %v\n", classifiedErr.Info.Type.String(), err)
		} else if ctx.Err() != nil {
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
