package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Incoming request from the load balancer
type PaymentRequest struct {
	CorrelationID string  `json:"correlationId"`
	Amount        float64 `json:"amount"`
}

// Outgoing request to the payment processors
type ProcessorRequest struct {
	CorrelationID string    `json:"correlationId"`
	Amount        float64   `json:"amount"`
	RequestedAt   time.Time `json:"requestedAt"`
}

type SummaryResponse struct {
	Default  ProcessorSummary `json:"default"`
	Fallback ProcessorSummary `json:"fallback"`
}

type ProcessorSummary struct {
	TotalRequests int64   `json:"totalRequests"`
	TotalAmount   float64 `json:"totalAmount"`
}

// Circuit Breaker States
const (
	StateClosed   = "CLOSED"
	StateOpen     = "OPEN"
	StateHalfOpen = "HALF_OPEN"
)

// HealthStatus holds the circuit breaker and health info for a processor
type HealthStatus struct {
	mu                  sync.RWMutex
	State               string
	ConsecutiveFailures int
	OpenTime            time.Time
}

// HealthCheckResponse is the response from the health check endpoint
type HealthCheckResponse struct {
	Failing         bool `json:"failing"`
	MinResponseTime int  `json:"minResponseTime"`
}

var (
	dbpool       *pgxpool.Pool
	httpClient   *http.Client
	healthStatus map[string]*HealthStatus
)

const (
	defaultProcessorURL    = "http://payment-processor-default:8080/payments"
	fallbackProcessorURL   = "http://payment-processor-fallback:8080/payments"
	defaultHealthCheckURL  = "http://payment-processor-default:8080/payments/service-health"
	fallbackHealthCheckURL = "http://payment-processor-fallback:8080/payments/service-health"
	failureThreshold       = 5
	openStateTimeout       = 10 * time.Second
	healthCheckInterval    = 13 * time.Second
)

func main() {
	// Database connection
	dbHost := os.Getenv("DB_HOSTNAME")
	dbUrl := fmt.Sprintf("postgres://rinha:rinha@%s:5432/rinha", dbHost)

	var err error
	dbpool, err = pgxpool.New(context.Background(), dbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer dbpool.Close()

	// Shared HTTP client for performance
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}

	// Initialize health status for processors
	healthStatus = map[string]*HealthStatus{
		"default":  {State: StateClosed},
		"fallback": {State: StateClosed},
	}

	// Start background health checkers
	go startHealthChecker()

	// HTTP handlers
	http.HandleFunc("POST /payments", handlePayments)
	http.HandleFunc("GET /payments-summary", handleSummary)

	log.Println("Server starting on port 8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Could not start server: %s\n", err)
	}
}

func startHealthChecker() {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	// Run once at the start
	pollHealth("default", defaultHealthCheckURL)
	pollHealth("fallback", fallbackHealthCheckURL)

	for range ticker.C {
		go pollHealth("default", defaultHealthCheckURL)
		go pollHealth("fallback", fallbackHealthCheckURL)
	}
}

func pollHealth(processorName, url string) {
	status := healthStatus[processorName]

	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("[HealthCheck-%s] Error polling: %v", processorName, err)
		recordFailure(processorName)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[HealthCheck-%s] Non-200 status: %d", processorName, resp.StatusCode)
		recordFailure(processorName)
		return
	}

	var healthResp HealthCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		log.Printf("[HealthCheck-%s] Error decoding response: %v", processorName, err)
		return
	}

	status.mu.Lock()
	defer status.mu.Unlock()

	if healthResp.Failing {
		if status.State != StateOpen {
			log.Printf("[CircuitBreaker-%s] Health check reports failing, opening circuit.", processorName)
			status.State = StateOpen
			status.OpenTime = time.Now()
		}
	} else {
		if status.State == StateOpen {
			log.Printf("[CircuitBreaker-%s] Health check reports healthy, moving to HALF_OPEN.", processorName)
			status.State = StateHalfOpen
		}
	}
}

func isCircuitOpen(processorName string) bool {
	status := healthStatus[processorName]
	status.mu.Lock()
	defer status.mu.Unlock()

	if status.State == StateOpen {
		if time.Since(status.OpenTime) > openStateTimeout {
			log.Printf("[CircuitBreaker-%s] Timeout passed, moving to HALF_OPEN.", processorName)
			status.State = StateHalfOpen
			return false // Allow one request through
		}
		return true // Still open
	}
	return false // Closed or Half-Open
}

func recordFailure(processorName string) {
	status := healthStatus[processorName]
	status.mu.Lock()
	defer status.mu.Unlock()

	if status.State == StateHalfOpen {
		log.Printf("[CircuitBreaker-%s] Failure in HALF_OPEN, re-opening circuit.", processorName)
		status.State = StateOpen
		status.OpenTime = time.Now()
	} else {
		status.ConsecutiveFailures++
		if status.ConsecutiveFailures >= failureThreshold && status.State == StateClosed {
			log.Printf("[CircuitBreaker-%s] Failure threshold reached, opening circuit.", processorName)
			status.State = StateOpen
			status.OpenTime = time.Now()
		}
	}
}

func recordSuccess(processorName string) {
	status := healthStatus[processorName]
	status.mu.Lock()
	defer status.mu.Unlock()

	if status.State == StateHalfOpen {
		log.Printf("[CircuitBreaker-%s] Success in HALF_OPEN, closing circuit.", processorName)
	}
	status.State = StateClosed
	status.ConsecutiveFailures = 0
}

func handlePayments(w http.ResponseWriter, r *http.Request) {
	var req PaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Try default processor if its circuit is not open
	if !isCircuitOpen("default") {
		if processPayment(r.Context(), &req, "default", defaultProcessorURL) {
			recordSuccess("default")
			w.WriteHeader(http.StatusOK)
			return
		} else {
			recordFailure("default")
		}
	}

	// Try fallback processor if its circuit is not open
	if !isCircuitOpen("fallback") {
		if processPayment(r.Context(), &req, "fallback", fallbackProcessorURL) {
			recordSuccess("fallback")
			w.WriteHeader(http.StatusOK)
			return
		} else {
			recordFailure("fallback")
		}
	}

	http.Error(w, "Payment processing failed with both processors", http.StatusServiceUnavailable)
}

func processPayment(ctx context.Context, req *PaymentRequest, processorName, url string) bool {
	procReq := ProcessorRequest{
		CorrelationID: req.CorrelationID,
		Amount:        req.Amount,
		RequestedAt:   time.Now(),
	}

	jsonBody, err := json.Marshal(procReq)
	if err != nil {
		log.Printf("[%s] Error marshalling request: %v", processorName, err)
		return false
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Printf("[%s] Error creating request: %v", processorName, err)
		return false
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		log.Printf("[%s] Error sending request: %v", processorName, err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return recordPayment(ctx, req, processorName)
	}

	log.Printf("[%s] Received non-2xx response: %d", processorName, resp.StatusCode)
	return false
}

func recordPayment(ctx context.Context, req *PaymentRequest, processorName string) bool {
	_, err := dbpool.Exec(ctx,
		"INSERT INTO payments (correlation_id, amount, processor) VALUES ($1, $2, $3)",
		req.CorrelationID, req.Amount, processorName)

	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			log.Printf("Duplicate payment correlation ID: %s", req.CorrelationID)
			return true
		}
		log.Printf("Error inserting payment into DB: %v", err)
		return false
	}

	return true
}

func handleSummary(w http.ResponseWriter, r *http.Request) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	var response SummaryResponse
	var err error

	response.Default, err = getSummaryForProcessor(r.Context(), "default", fromStr, toStr)
	if err != nil {
		log.Printf("Error getting summary for default processor: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	response.Fallback, err = getSummaryForProcessor(r.Context(), "fallback", fromStr, toStr)
	if err != nil {
		log.Printf("Error getting summary for fallback processor: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func getSummaryForProcessor(ctx context.Context, processor, fromStr, toStr string) (ProcessorSummary, error) {
	query := "SELECT COALESCE(COUNT(*), 0), COALESCE(SUM(amount), 0) FROM payments WHERE processor = $1"
	args := []interface{}{processor}

	if fromStr != "" && toStr != "" {
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return ProcessorSummary{}, fmt.Errorf("invalid 'from' timestamp: %w", err)
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return ProcessorSummary{}, fmt.Errorf("invalid 'to' timestamp: %w", err)
		}
		query += " AND created_at >= $2 AND created_at <= $3"
		args = append(args, from, to)
	}

	var summary ProcessorSummary
	err := dbpool.QueryRow(ctx, query, args...).Scan(&summary.TotalRequests, &summary.TotalAmount)
	if err != nil {
		// An aggregate query like this should always return a row, so ErrNoRows is unexpected.
		// However, it's good practice to handle it gracefully.
		if err == pgx.ErrNoRows {
			return ProcessorSummary{TotalRequests: 0, TotalAmount: 0}, nil
		}
		return ProcessorSummary{}, err
	}

	return summary, nil
}
