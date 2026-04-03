# NEXUS - Part 3: Health Checking, Observability & Production Hardening

**Goal:** Make the proxy production-ready with health checking, logging, metrics, and graceful shutdown.

---

## Phase 6: Health Checking

### Understanding the State Machine

```
           fail_threshold reached
  Healthy ───────────────────────────> Unhealthy
     ^                                     |
     |      pass_threshold reached         |
     └─────────────────────────────────────┘
```

**Why a threshold, not single failure?**
- Networks have transient errors
- Single timeout doesn't mean backend is down
- 3 consecutive failures = real problem
- Prevents "flapping" (rapid healthy↔unhealthy)

**Example scenario:**
- Backend drops 1 packet → 1 failure
- Next check succeeds → failure count resets
- If dropping many packets → hits threshold → marked unhealthy
- Must pass `pass_threshold` times to return to healthy

### Step 1: Define HealthChecker

**File: `internal/health/checker.go`**

```go
package health

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type Backend struct {
	URL           string
	Alive         bool
	failCount     int
	successCount  int
}
```

**Why embed failure/success counts?**
- Track consecutive results
- Reset opposite counter on state change
- Don't mark unhealthy on first failure

**Why not exported (lowercase)?**
- Internal state for health checker only
- External code shouldn't manipulate these

Add checker struct:

```go
type Checker struct {
	backends      []*Backend
	mu            sync.RWMutex
	client        *http.Client
	interval      time.Duration
	timeout       time.Duration
	passThreshold int
	failThreshold int
	cancel        context.CancelFunc
}
```

**Why `*Backend` slice?**
- Need to modify `Alive` field
- Pointer allows in-place updates

**Why `sync.RWMutex`?**
- Many readers (proxy checking `Alive`)
- One writer (health checker updating state)
- RWMutex optimizes this pattern

**Why separate `client`?**
- Health checks have different timeout than proxy requests
- Shouldn't share client with proxy (different connection pool)

**Why `cancel context.CancelFunc`?**
- To stop health check goroutines on shutdown
- Call `cancel()` to signal all goroutines to stop
- Part of graceful shutdown

### Step 2: Constructor

```go
func NewChecker(backends []string, interval, timeout time.Duration, passThreshold, failThreshold int) *Checker {
	ctx, cancel := context.WithCancel(context.Background())
	
	backendList := make([]*Backend, len(backends))
	for i, url := range backends {
		backendList[i] = &Backend{
			URL:   url,
			Alive: true,
		}
	}
	
	checker := &Checker{
		backends:      backendList,
		client:        &http.Client{Timeout: timeout},
		interval:      interval,
		timeout:       timeout,
		passThreshold: passThreshold,
		failThreshold: failThreshold,
		cancel:        cancel,
	}
	
	return checker
}
```

**Why `context.WithCancel`?**
- Creates cancellable context
- Calling `cancel()` closes `ctx.Done()` channel
- All goroutines watching this context will stop

**Why `context.Background()`?**
- Root context with no deadline
- Health checker runs until explicitly stopped
- Not tied to a single request

**Why start `Alive: true`?**
- Optimistic: assume backends up at start
- Health checker will correct if wrong
- Avoids marking all dead before first check

### Step 3: Start Method

```go
func (c *Checker) Start(ctx context.Context) {
	for _, backend := range c.backends {
		go c.checkLoop(ctx, backend)
	}
}
```

**Why one goroutine per backend?**
- Each backend checked independently
- One slow backend doesn't delay others
- Parallel health checks complete faster

**Pattern:** goroutine-per-entity
- Common in Go for independent tasks
- Each goroutine has focused responsibility
- Scales to hundreds of backends

**Why pass `ctx`?**
- Each goroutine watches for cancellation
- When `ctx.Done()` closes, all goroutines stop
- Enables graceful shutdown

### Step 4: Check Loop

```go
func (c *Checker) checkLoop(ctx context.Context, backend *Backend) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	
	c.checkOnce(backend)
	
	for {
		select {
		case <-ticker.C:
			c.checkOnce(backend)
		case <-ctx.Done():
			return
		}
	}
}
```

**Why `time.NewTicker`?**
- Fires at regular intervals
- Sends timestamp on `ticker.C` channel every `c.interval`
- More accurate than `time.Sleep` in loop (doesn't accumulate drift)

**Why `defer ticker.Stop()`?**
- Stops ticker when goroutine exits
- Prevents goroutine leak
- Ticker holds resources (goroutine + channel)

**Why check once before loop?**
- Don't wait `interval` before first check
- Get initial health status immediately
- Then continue periodic checks

**What's `select`?**
- Waits on multiple channel operations
- Whichever channel is ready first wins
- Like `switch` but for channels

**How cancellation works:**
- `ctx.Done()` is closed channel when context cancelled
- Reading from closed channel succeeds immediately
- `case <-ctx.Done():` triggers, goroutine returns
- Clean exit, no leaked goroutines

**Why this pattern prevents leaks:**
- Without cancellation, goroutine runs forever
- With cancellation, goroutine exits cleanly
- All tickers stopped, resources freed

### Step 5: Check Once

```go
func (c *Checker) checkOnce(backend *Backend) {
	req, err := http.NewRequest("GET", backend.URL+"/health", nil)
	if err != nil {
		c.markFailure(backend)
		return
	}
	
	req.Header.Set("User-Agent", "Nexus-HealthChecker/1.0")
	
	resp, err := c.client.Do(req)
	if err != nil {
		c.markFailure(backend)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.markSuccess(backend)
	} else {
		c.markFailure(backend)
	}
}
```

**Why GET `/health`?**
- Common convention for health endpoints
- Should be fast (just "am I alive?")
- Not a real endpoint that does work

**Why custom User-Agent?**
- Backend logs can identify health checks
- Helps debug: "why so many /health requests?"
- Professional touch

**Why check status code range?**
- 2xx = success (200 OK, 204 No Content, etc.)
- Anything else = failure
- Includes 5xx (server error) and 4xx (client error)

**Why mark failure on error?**
- Network error = backend unreachable
- Treat as unhealthy
- Includes timeouts, DNS failures, connection refused

### Step 6: Mark Success/Failure

```go
func (c *Checker) markSuccess(backend *Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	backend.failCount = 0
	backend.successCount++
	
	if !backend.Alive && backend.successCount >= c.passThreshold {
		backend.Alive = true
		backend.successCount = 0
		log.Printf("Backend %s is now HEALTHY", backend.URL)
	}
}
```

**Why `Lock` not `RLock`?**
- Modifying backend state (writes)
- Need exclusive access
- Blocks all readers and writers

**Why reset `failCount` on success?**
- Success breaks the failure streak
- Need consecutive failures to mark unhealthy
- Fresh start after any success

**Why threshold check?**
- Don't mark healthy on first success
- Need `passThreshold` consecutive successes
- Ensures backend is stable, not flapping

**Why reset `successCount` after marking healthy?**
- Start fresh for next cycle
- Otherwise counter keeps growing forever
- Only matters when transitioning states

**Why log state changes?**
- Observability: operators see when backends go down/up
- Critical for debugging production issues
- Don't log every check (too noisy)

Add mark failure:

```go
func (c *Checker) markFailure(backend *Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	backend.successCount = 0
	backend.failCount++
	
	if backend.Alive && backend.failCount >= c.failThreshold {
		backend.Alive = false
		backend.failCount = 0
		log.Printf("Backend %s is now UNHEALTHY", backend.URL)
	}
}
```

**Symmetric to `markSuccess`:**
- Reset opposite counter
- Increment this counter
- Check threshold
- Transition state if threshold reached
- Reset counter after transition

### Step 7: Get Healthy Backends

```go
func (c *Checker) GetHealthy() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	healthy := make([]string, 0, len(c.backends))
	for _, b := range c.backends {
		if b.Alive {
			healthy = append(healthy, b.URL)
		}
	}
	return healthy
}
```

**Why `RLock`?**
- Only reading, not modifying
- Allow concurrent reads from multiple goroutines
- Proxy can check while health checker updates

**Why return slice copy?**
- Caller gets snapshot at this moment
- Caller can iterate without holding lock
- Prevents deadlocks

**Why capacity `len(c.backends)`?**
- Best case: all healthy
- Avoids reallocations
- Small waste if many unhealthy

### Step 8: Stop Method

```go
func (c *Checker) Stop() {
	c.cancel()
}
```

**Why so simple?**
- `cancel()` closes context
- All goroutines watching context exit
- Cleanup happens automatically via `defer`

**Critical pattern:**
- Start: spawn goroutines with context
- Stop: cancel context
- Goroutines: watch context, clean up on cancel

### Step 9: Integrate with Proxy

**File: `cmd/nexus/main.go`**

Add health checker creation:

```go
func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Create backends for balancer
	backends := make([]*balancer.Backend, len(cfg.Backends))
	backendURLs := make([]string, len(cfg.Backends))
	for i, b := range cfg.Backends {
		u, err := url.Parse(b.URL)
		if err != nil {
			log.Fatalf("Invalid backend URL: %v", err)
		}
		backends[i] = &balancer.Backend{
			URL:    u,
			Alive:  true,
			Weight: b.Weight,
		}
		backendURLs[i] = b.URL
	}

	// Create balancer
	var bal balancer.Balancer
	switch cfg.Balancer {
	case "roundrobin":
		bal = balancer.NewRoundRobin(backends)
	default:
		log.Fatalf("Unknown balancer: %s", cfg.Balancer)
	}

	// Create proxy
	p, err := proxy.New(backendURLs, bal)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}

	// Create and start health checker
	checker := health.NewChecker(
		backendURLs,
		cfg.Health.Interval,
		cfg.Health.Timeout,
		cfg.Health.PassThreshold,
		cfg.Health.FailThreshold,
	)
	
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	
	checker.Start(ctx)

	log.Printf("Starting proxy on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, p); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
```

**Why `signal.NotifyContext`?**
- Creates context that cancels on OS signals
- SIGINT = Ctrl+C
- SIGTERM = `kill` command
- Enables graceful shutdown

**Add imports:**
```go
import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os/signal"
	"syscall"

	"github.com/VJ-2303/nexus/internal/balancer"
	"github.com/VJ-2303/nexus/internal/config"
	"github.com/VJ-2303/nexus/internal/health"
	"github.com/VJ-2303/nexus/internal/proxy"
)
```

### Step 10: Test Health Checking

Update test backend to have health endpoint:

**File: `test-backend.go`**

```go
func main() {
	port := ":8081"
	if len(os.Args) > 1 {
		port = ":" + os.Args[1]
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "OK")
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Backend on port %s\n", port)
		fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
	})

	log.Printf("Backend starting on %s", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
```

Run test:
```bash
# Terminal 1: Backend 1
go run test-backend.go 8081

# Terminal 2: Backend 2
go run test-backend.go 8082

# Terminal 3: Proxy
go run cmd/nexus/main.go

# Terminal 4: Kill backend 1, watch logs
# Ctrl+C in terminal 1

# After ~30 seconds (3 failures at 10s interval):
# Proxy logs: "Backend http://localhost:8081 is now UNHEALTHY"

# Terminal 5: Test - should only hit backend 2
curl http://localhost:8080/test
curl http://localhost:8080/test

# Restart backend 1
go run test-backend.go 8081

# After ~20 seconds (2 successes):
# Proxy logs: "Backend http://localhost:8081 is now HEALTHY"

# Now both backends serve again
curl http://localhost:8080/test
```

**What this proves:**
- Health checker detects failure after threshold
- Dead backend removed from rotation
- Recovered backend re-added after pass threshold
- No manual intervention needed

---

Continued in next file...

## Phase 7: Structured Logging with slog

### Why Structured Logging?

**Problem with `log.Printf`:**
```go
log.Printf("request: %s %s from %s took %dms", method, path, ip, duration)
// Output: request: GET /api/users from 203.0.113.42 took 45ms
```

**Issues:**
- Human-readable, but not machine-parseable
- Can't easily query "all requests > 100ms"
- Parsing requires regex
- No consistent format across team

**Structured logging:**
```go
slog.Info("request", "method", "GET", "path", "/api/users", "ip", "203.0.113.42", "duration_ms", 45)
// JSON: {"level":"info","msg":"request","method":"GET","path":"/api/users","ip":"203.0.113.42","duration_ms":45}
```

**Benefits:**
- Machine-parseable (JSON)
- Query with `jq`, Elasticsearch, etc.
- Consistent key names
- Log aggregation systems love this

### Step 1: Setup Logger

**File: `cmd/nexus/main.go`**

Add logger setup at start of `main()`:

```go
func main() {
0var logHandler slog.Handler
0if os.Getenv("LOG_FORMAT") == "json" {
0logHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
0Level: slog.LevelInfo,
0})
0} else {
0logHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
0Level: slog.LevelInfo,
0})
0}
0slog.SetDefault(slog.New(logHandler))
```

**Why check `LOG_FORMAT` env var?**
- Development: text format (human-readable)
- Production: JSON format (machine-parseable)
- No code change needed

**What's `slog.Handler`?**
- Interface for output formatting
- `JSONHandler` → JSON lines
- `TextHandler` → key=value pairs
- Can write custom handlers

**Why `slog.SetDefault`?**
- Sets package-level default logger
- `slog.Info()`, `slog.Error()` use this logger
- One place to configure, whole app uses it

**Add import:**
```go
import (
0"log/slog"
0"os"
)
```

### Step 2: Convert Existing Logs

Replace all `log.Printf` with structured slog:

```go
// Old:
log.Printf("Starting proxy on %s", cfg.ListenAddr)

// New:
slog.Info("starting proxy",
0"listen_addr", cfg.ListenAddr,
0"balancer", cfg.Balancer,
0"backends", len(backends))
```

**Why lowercase keys?**
- Convention for log field names
- Matches JSON conventions
- Easier to type

**Why multiple fields?**
- Each field queryable independently
- Example query: all logs with `balancer=roundrobin`
- Adding fields is cheap

### Step 3: Request Logging Middleware

**File: `internal/proxy/middleware.go`**

```go
package proxy

import (
0"log/slog"
0"net/http"
0"time"
)

func LoggingMiddleware(next http.Handler) http.Handler {
0return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
0start := time.Now()
0
0wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
0
0next.ServeHTTP(wrapped, r)
0
0duration := time.Since(start)
0
0slog.Info("request completed",
0"method", r.Method,
0"path", r.URL.Path,
0"remote_addr", r.RemoteAddr,
0"status", wrapped.statusCode,
0"duration_ms", duration.Milliseconds(),
0)
0})
}
```

**What's middleware pattern?**
- Function that wraps a handler
- Takes handler, returns wrapped handler
- Wrapper adds behavior (logging, auth, etc.)

**Why `time.Now()` before, `time.Since()` after?**
- Captures request duration
- `time.Since()` returns `time.Duration`
- Convert to milliseconds for logging

**What's `responseWriter` wrapper?**
- Need to see next...

Add response writer wrapper:

```go
type responseWriter struct {
0http.ResponseWriter
0statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
0rw.statusCode = code
0rw.ResponseWriter.WriteHeader(code)
}
```

**Why wrap `ResponseWriter`?**
- Standard `ResponseWriter` doesn't expose status code
- We want to log status code
- Wrapper intercepts `WriteHeader` call and saves code

**How embedding works:**
- `http.ResponseWriter` embedded
- Wrapper inherits all methods (Write, Header, etc.)
- Only override what we need (WriteHeader)

**Why default to `http.StatusOK`?**
- If handler never calls `WriteHeader`, status is 200
- Must match default behavior

### Step 4: Request ID Middleware

```go
import (
0"context"
0"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "request_id"

func RequestIDMiddleware(next http.Handler) http.Handler {
0return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
0requestID := uuid.New().String()
0
0w.Header().Set("X-Request-ID", requestID)
0
0ctx := context.WithValue(r.Context(), requestIDKey, requestID)
0r = r.WithContext(ctx)
0
0next.ServeHTTP(w, r)
0})
}
```

**Why request IDs?**
- Trace single request through logs
- Client includes ID when reporting issues
- Correlate across services

**Why UUID?**
- Unique across all servers
- No coordination needed
- Collision probability negligible

**What's `context.WithValue`?**
- Stores key-value pair in context
- Context passes through call chain
- Retrieve anywhere in request handling

**Why custom `contextKey` type?**
- Prevents collisions with other packages
- If two packages use string key "id", they collide
- Custom type ensures uniqueness

**Why set response header?**
- Client receives request ID
- Can include in bug reports
- Professional API design

**What's `r.WithContext`?**
- Creates new request with different context
- Original request immutable
- Returns modified copy

Install UUID package:
```bash
go get github.com/google/uuid
go mod tidy
```

### Step 5: Update Logging to Use Request ID

Modify `LoggingMiddleware`:

```go
func LoggingMiddleware(next http.Handler) http.Handler {
0return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
0start := time.Now()
0
0requestID, _ := r.Context().Value(requestIDKey).(string)
0
0wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
0
0next.ServeHTTP(wrapped, r)
0
0duration := time.Since(start)
0
0slog.Info("request completed",
0"request_id", requestID,
0"method", r.Method,
0"path", r.URL.Path,
0"remote_addr", r.RemoteAddr,
0"status", wrapped.statusCode,
0"duration_ms", duration.Milliseconds(),
0)
0})
}
```

**What's type assertion `.(string)`?**
- `context.Value` returns `interface{}`
- Need to convert to actual type (string)
- Returns value and boolean (true if conversion succeeded)
- We ignore boolean - empty string is fine if not found

**Why request ID first?**
- Most important for filtering logs
- Convention to put it early

### Step 6: Chain Middlewares

**File: `internal/proxy/middleware.go`**

```go
func Chain(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
0return func(final http.Handler) http.Handler {
0for i := len(middlewares) - 1; i >= 0; i-- {
0final = middlewares[i](final)
0}
0return final
0}
}
```

**Why variadic `...func`?**
- Accept any number of middleware functions
- Called like: `Chain(mid1, mid2, mid3)`

**Why iterate backwards?**
- Middleware order matters
- Want: Request → M1 → M2 → M3 → Handler
- Build from inside out: `M1(M2(M3(Handler)))`
- Last middleware wraps handler, then second-to-last wraps that, etc.

**Example:**
```go
Chain(RequestIDMiddleware, LoggingMiddleware)(proxy)
// Expands to: RequestIDMiddleware(LoggingMiddleware(proxy))
// Request → RequestID → Logging → Proxy
```

### Step 7: Integrate Middleware

**File: `cmd/nexus/main.go`**

Update server setup:

```go
0handler := proxy.Chain(
0proxy.RequestIDMiddleware,
0proxy.LoggingMiddleware,
0)(p)

0log.Printf("Starting proxy on %s", cfg.ListenAddr)
0if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
0log.Fatalf("Server failed: %v", err)
0}
```

**Why wrap `p` (proxy)?**
- `p` is the core handler
- Middleware adds cross-cutting concerns
- Clean separation of concerns

**Order matters:**
- `RequestIDMiddleware` first: creates ID
- `LoggingMiddleware` second: uses ID for logging
- If reversed, logging wouldn't have ID yet

---

## Phase 8: Metrics

### Step 1: Define Metrics Struct

**File: `internal/metrics/metrics.go`**

```go
package metrics

import (
0"sync/atomic"
)

type Metrics struct {
0RequestsTotal   atomic.Uint64
0RequestsActive  atomic.Int64
0RequestsFailed  atomic.Uint64
0BytesReceived   atomic.Uint64
0BytesSent       atomic.Uint64
}
```

**Why `atomic.Uint64`?**
- Thread-safe counter without mutex
- `atomic.Uint64` is a type with methods
- `Add`, `Load`, `Store` are atomic operations
- Go 1.19+ feature

**Why not `sync.Mutex + uint64`?**
- Atomic operations are faster (no lock)
- Hot path: every request increments counters
- Mutex would be bottleneck

**What "atomic" means:**
- Operation completes in one indivisible step
- At CPU level: single instruction
- No partial reads/writes
- No race conditions

**Why `RequestsActive` is `Int64` not `Uint64`?**
- Can go negative if not careful (decrement before increment)
- Signed int more forgiving
- Actually should never be negative, but prevents underflow issues

### Step 2: Metrics Methods

```go
func (m *Metrics) IncrementTotal() {
0m.RequestsTotal.Add(1)
}

func (m *Metrics) IncrementActive() {
0m.RequestsActive.Add(1)
}

func (m *Metrics) DecrementActive() {
0m.RequestsActive.Add(-1)
}

func (m *Metrics) IncrementFailed() {
0m.RequestsFailed.Add(1)
}

func (m *Metrics) AddBytesReceived(n uint64) {
0m.BytesReceived.Add(n)
}

func (m *Metrics) AddBytesSent(n uint64) {
0m.BytesSent.Add(n)
}
```

**Why wrapper methods?**
- Cleaner API: `m.IncrementTotal()` vs `m.RequestsTotal.Add(1)`
- Encapsulation: can change implementation later
- Self-documenting

**Why `Add(-1)` for decrement?**
- No separate `Sub` method on `atomic.Int64`
- Adding negative number = subtraction
- Works fine

### Step 3: Snapshot Method

```go
type Snapshot struct {
0RequestsTotal  uint64 `json:"requests_total"`
0RequestsActive int64  `json:"requests_active"`
0RequestsFailed uint64 `json:"requests_failed"`
0BytesReceived  uint64 `json:"bytes_received"`
0BytesSent      uint64 `json:"bytes_sent"`
}

func (m *Metrics) Snapshot() Snapshot {
0return Snapshot{
0RequestsTotal:  m.RequestsTotal.Load(),
0RequestsActive: m.RequestsActive.Load(),
0RequestsFailed: m.RequestsFailed.Load(),
0BytesReceived:  m.BytesReceived.Load(),
0BytesSent:      m.BytesSent.Load(),
0}
}
```

**Why separate `Snapshot` type?**
- `atomic.Uint64` doesn't JSON serialize nicely
- Snapshot is plain uint64 values
- Read all values atomically at one moment

**Why JSON struct tags?**
- Control JSON field names
- Consistent with API conventions (snake_case)

**What's `Load()`?**
- Atomically reads current value
- Returns regular uint64
- Safe to call concurrently

### Step 4: Metrics Middleware

**File: `internal/proxy/middleware.go`**

```go
import (
0"github.com/VJ-2303/nexus/internal/metrics"
)

func MetricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
0return func(next http.Handler) http.Handler {
0return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
0m.IncrementTotal()
0m.IncrementActive()
0defer m.DecrementActive()
0
0wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
0
0next.ServeHTTP(wrapped, r)
0
0if wrapped.statusCode >= 400 {
0m.IncrementFailed()
0}
0})
0}
}
```

**Why return function?**
- Middleware needs access to `m` (*Metrics)
- Closure captures `m`
- Returns standard middleware signature

**Why increment active, then defer decrement?**
- Active = requests currently being handled
- Increment at start
- Defer ensures decrement even if panic
- Accurate concurrent request count

**Why check status >= 400?**
- 4xx = client error (bad request, not found)
- 5xx = server error (internal error, bad gateway)
- Both are "failed" requests

### Step 5: Admin API for Metrics

**File: `internal/admin/admin.go`**

```go
package admin

import (
0"encoding/json"
0"net/http"
0
0"github.com/VJ-2303/nexus/internal/metrics"
)

type Server struct {
0metrics *metrics.Metrics
}

func NewServer(m *metrics.Metrics) *Server {
0return &Server{metrics: m}
}

func (s *Server) Handler() http.Handler {
0mux := http.NewServeMux()
0mux.HandleFunc("/metrics", s.handleMetrics)
0return mux
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
0if r.Method != http.MethodGet {
0http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
0return
0}
0
0snapshot := s.metrics.Snapshot()
0
0w.Header().Set("Content-Type", "application/json")
0json.NewEncoder(w).Encode(snapshot)
}
```

**Why separate admin server?**
- Runs on different port (9090 vs 8080)
- Can bind to localhost only (security)
- Admin endpoints not exposed to public

**Why check method?**
- GET for reading, not POST/PUT
- Return 405 if wrong method
- REST API best practice

**Why `json.NewEncoder(w).Encode()`?**
- Streams JSON directly to response
- More efficient than `json.Marshal` + `w.Write`
- Handles errors automatically

### Step 6: Integrate Everything

**File: `cmd/nexus/main.go`**

Add metrics creation:

```go
func main() {
0// ... logger setup ...
0// ... config loading ...
0// ... proxy setup ...

0m := &metrics.Metrics{}

0handler := proxy.Chain(
0proxy.RequestIDMiddleware,
0proxy.MetricsMiddleware(m),
0proxy.LoggingMiddleware,
0)(p)

0adminSrv := admin.NewServer(m)
0go func() {
0slog.Info("starting admin server", "addr", cfg.AdminAddr)
0if err := http.ListenAndServe(cfg.AdminAddr, adminSrv.Handler()); err != nil {
0slog.Error("admin server failed", "error", err)
0}
0}()

0slog.Info("starting proxy", "addr", cfg.ListenAddr)
0if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
0slog.Error("proxy server failed", "error", err)
0}
}
```

**Why start admin in goroutine?**
- `ListenAndServe` blocks
- Need to start two servers
- Admin in goroutine, proxy in main

**Why metrics middleware before logging?**
- Metrics counts all requests
- Logging depends on metrics being counted
- Order: RequestID → Metrics → Logging → Proxy

**Add import:**
```go
import (
0"github.com/VJ-2303/nexus/internal/admin"
0"github.com/VJ-2303/nexus/internal/metrics"
)
```

### Step 7: Test Metrics

Run proxy and test:

```bash
# Start backends
go run test-backend.go 8081 &
go run test-backend.go 8082 &

# Start proxy
go run cmd/nexus/main.go

# Make requests
curl http://localhost:8080/test
curl http://localhost:8080/test
curl http://localhost:8080/test

# Check metrics
curl http://localhost:9090/metrics
```

**Expected output:**
```json
{
  "requests_total": 3,
  "requests_active": 0,
  "requests_failed": 0,
  "bytes_received": 0,
  "bytes_sent": 150
}
```

**What this proves:**
- Metrics counting works
- Admin API serves JSON
- Atomic operations thread-safe (try concurrent requests with `ab` or `wrk`)

---

## Final Checkpoint

You now have:
- ✅ Health checking with goroutine-per-backend pattern
- ✅ Threshold-based state machine (prevents flapping)
- ✅ Context cancellation for graceful goroutine shutdown
- ✅ Structured logging with slog (JSON in production)
- ✅ Request ID tracking across call chain
- ✅ Middleware pattern for cross-cutting concerns
- ✅ Atomic metrics (lock-free performance)
- ✅ Admin API for observability

**Test Your Understanding:**

Add circuit breaker:

1. Create `internal/circuit/breaker.go`
2. Three states: Closed, Open, Half-Open
3. Open after 5 failures in 10 seconds
4. Half-Open after 30 seconds
5. Back to Closed on success in half-open

Key concepts to use:
- `atomic.Uint32` for state
- `atomic.Uint64` for failure count and timestamp
- `time.Since()` for timeout
- State machine logic

This is the final production feature. If you can implement this, you've mastered the proxy architecture.

---

## Complete. You have NEXUS.

A production-grade reverse proxy with:
- Load balancing (round robin, extendable to least-conn/IP-hash)
- Active health checking with automatic recovery
- Connection pooling
- Structured logging
- Metrics collection
- Admin API
- Graceful shutdown
- Middleware architecture

**Resume-worthy. Deploy-ready. Debug-capable.**
