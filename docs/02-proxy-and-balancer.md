# NEXUS - Part 2: Core Reverse Proxy & Load Balancing

**Goal:** Build the HTTP forwarding mechanism and load balancer with deep understanding.

---

## Phase 3: Understanding Reverse Proxies (Concept First)

### What is a Reverse Proxy?

**Analogy:** A hotel concierge.
- Guest (client) asks concierge (proxy) for room service
- Concierge (proxy) calls kitchen (backend)
- Kitchen sends food to concierge
- Concierge delivers to guest
- **Guest never talks directly to kitchen**

**vs Forward Proxy:**
- Forward proxy: client knows it's using a proxy (like corporate proxy)
- Reverse proxy: client thinks it's talking to the real server

### Request Flow

```
Client                     Nexus (Proxy)              Backend
  |                              |                        |
  |--GET /api/users------------>|                        |
  |   Host: api.example.com     |                        |
  |                              |                        |
  |                              |--GET /api/users------->|
  |                              |  Host: localhost:8081  |
  |                              |  X-Forwarded-For: ...  |
  |                              |  X-Real-IP: ...        |
  |                              |                        |
  |                              |<--200 OK + JSON--------|
  |                              |                        |
  |<--200 OK + JSON--------------|                        |
  |                              |                        |
```

**Key transformations:**
1. **URL rewriting:** `/api/users` on proxy becomes `http://localhost:8081/api/users` to backend
2. **Header additions:** Proxy adds `X-Forwarded-For`, `X-Real-IP`
3. **Header stripping:** Remove hop-by-hop headers like `Connection`, `Keep-Alive`
4. **Response streaming:** Copy backend response back to client without buffering all of it

### Critical Headers

**X-Forwarded-For:**
- Contains client's real IP address
- Why? Backend sees proxy's IP, not client's
- Backend needs client IP for logging, rate limiting, geolocation
- Example: `X-Forwarded-For: 203.0.113.42`
- If request went through multiple proxies: `X-Forwarded-For: 203.0.113.42, 198.51.100.5`

**X-Real-IP:**
- Alternative to X-Forwarded-For
- Contains single IP (client's)
- Simpler for backends to parse

**X-Forwarded-Proto:**
- Was request HTTP or HTTPS?
- Example: `X-Forwarded-Proto: https`
- Backend needs this to generate correct redirect URLs

### Hop-by-Hop Headers

**What are they?**
- Headers that apply to single connection, not end-to-end
- **Must be stripped** by proxies
- If forwarded, they confuse the backend

**List of hop-by-hop headers:**
- `Connection`
- `Keep-Alive`
- `Proxy-Authenticate`
- `Proxy-Authorization`
- `TE` (transfer encodings)
- `Trailer`
- `Transfer-Encoding`
- `Upgrade`

**Why strip them?**
- `Connection: keep-alive` means "keep THIS connection alive" (client→proxy)
- Forwarding it to backend confuses connection management
- Backend might try to talk to client directly (impossible)

### Connection Pooling

**Problem:** Creating TCP connections is expensive.
- TCP handshake (SYN, SYN-ACK, ACK) = 1.5 round trips
- TLS handshake = additional 2 round trips
- Total: ~100ms overhead per request on distant servers

**Solution:** Reuse connections.
- After response completes, keep connection open
- Next request to same backend reuses it
- Saves handshake time

**Go's http.Transport manages this automatically:**
- Maintains pool of idle connections per host
- When you make request, checks pool first
- If connection available, reuses it
- If not, creates new one and adds to pool after

---

## Phase 4: Core Reverse Proxy

### Step 1: Define Backend Type

**File: `internal/proxy/proxy.go`**

```go
package proxy

import "net/url"

type Backend struct {
	URL    *url.URL
	Alive  bool
	Weight int
}
```

**Why `*url.URL` not `string`?**
- Parsed URL structure with scheme, host, path components
- Don't want to parse URL string on every request (slow)
- Parse once at startup, reuse

**Why `Alive bool`?**
- Tracks if backend is healthy
- Health checker sets this
- Load balancer skips dead backends

**Why `Weight int`?**
- For weighted load balancing
- Backend with weight 2 gets 2x traffic
- From config

### Step 2: Create Proxy Struct

Add to `proxy.go`:

```go
import (
	"net/http"
	"sync"
)

type Proxy struct {
	backends []*Backend
	mu       sync.RWMutex
	client   *http.Client
}
```

**Why `[]*Backend` slice?**
- List of available backends
- Pointers because we'll modify `Alive` field

**Why `sync.RWMutex`?**
- Protects `backends` slice from concurrent access
- Multiple goroutines read (handling requests)
- One goroutine writes (health checker updating `Alive`)
- `RWMutex` allows many readers OR one writer
- Better than `Mutex` when reads vastly outnumber writes

**Why `*http.Client`?**
- Used to make requests to backends
- Contains `Transport` with connection pool
- Reuse single client, don't create per request

### Step 3: Create Constructor

Add to `proxy.go`:

```go
func New(backendURLs []string) (*Proxy, error) {
	if len(backendURLs) == 0 {
		return nil, fmt.Errorf("no backends provided")
	}
```

**Why validate backend count?**
- Defensive programming
- Even though config validates, constructor might be called from tests

Continue:

```go
	backends := make([]*Backend, len(backendURLs))
	for i, urlStr := range backendURLs {
		u, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("parsing backend URL %q: %w", urlStr, err)
		}
		backends[i] = &Backend{
			URL:    u,
			Alive:  true,
			Weight: 1,
		}
	}
```

**Why `make([]*Backend, len(backendURLs))`?**
- Pre-allocates slice with exact size
- More efficient than append in loop
- We know final size

**Why `url.Parse`?**
- Parses string URL into `*url.URL` struct
- Returns error for invalid URLs (e.g., no scheme)
- Do once here, not on every request

**Why start with `Alive: true`?**
- Optimistic: assume backends are up
- Health checker will mark down if needed
- Prevents all backends being dead at startup

Add client creation:

```go
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
	}
```

**Why set `Timeout` on client?**
- Total time for request (includes reading response)
- Prevents hanging forever on slow backend
- 30s is reasonable default

**Why customize `Transport`?**
- Default transport has conservative limits
- `MaxIdleConns: 100` allows more reuse
- `MaxIdleConnsPerHost: 10` per backend
- `IdleConnTimeout: 90s` keeps connections longer

**Why not use `http.DefaultTransport`?**
- It's a package-level global (shared state)
- Other code might modify it
- We want isolated transport for our proxy

Return:

```go
	return &Proxy{
		backends: backends,
		client:   client,
	}, nil
}
```

**Why not initialize `mu`?**
- Zero value of `sync.RWMutex` is ready to use
- No initialization needed

### Step 4: Implement ServeHTTP

This makes `Proxy` implement `http.Handler` interface.

```go
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend := p.getNextBackend()
	if backend == nil {
		http.Error(w, "No healthy backends", http.StatusServiceUnavailable)
		return
	}
```

**Why `http.Handler` interface?**
- Standard Go HTTP abstraction
- Works with `http.ListenAndServe`
- Composable with middleware

**What's `getNextBackend()`?**
- We'll implement this - returns next backend to use
- `nil` if all backends dead

**Why `StatusServiceUnavailable` (503)?**
- Correct HTTP status when service can't handle request
- Tells client to retry later
- Better than 500 (implies bug in code)

Continue:

```go
	targetURL := backend.URL.ResolveReference(r.URL)
```

**What's `ResolveReference`?**
- Combines backend base URL with request path
- Example:
  - Backend: `http://localhost:8081`
  - Request: `/api/users`
  - Result: `http://localhost:8081/api/users`
- Handles query strings, fragments correctly

Add request cloning:

```go
	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}
```

**Why create new request?**
- Can't modify original request (might be read elsewhere)
- Need to change URL to backend's URL
- Need to add headers without affecting original

**Why copy `r.Method`?**
- Preserve original HTTP method (GET, POST, etc.)
- Backend needs to know what operation to perform

**Why pass `r.Body`?**
- Request body (for POST/PUT) needs to go to backend
- `io.ReadCloser` can be passed through

Add header copying:

```go
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
```

**Why nested loop?**
- HTTP headers can have multiple values
- Example: `Cookie: a=1` and `Cookie: b=2`
- `Header` is `map[string][]string`
- Inner loop copies all values for each header

**Why `Add` not `Set`?**
- `Add` appends value
- `Set` replaces all values
- We want to preserve multiple values

Add forwarding headers:

```go
	proxyReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	proxyReq.Header.Set("X-Real-IP", r.RemoteAddr)
	if r.TLS != nil {
		proxyReq.Header.Set("X-Forwarded-Proto", "https")
	} else {
		proxyReq.Header.Set("X-Forwarded-Proto", "http")
	}
```

**Why `X-Forwarded-For`?**
- Backend needs client's real IP
- `r.RemoteAddr` is client's address (format: `IP:port`)
- Backend uses this for logging, rate limiting

**Why check `r.TLS`?**
- `r.TLS` is non-nil for HTTPS requests
- Backend needs to know original protocol
- Important for generating correct redirect URLs

Strip hop-by-hop headers:

```go
	proxyReq.Header.Del("Connection")
	proxyReq.Header.Del("Keep-Alive")
	proxyReq.Header.Del("Proxy-Authorization")
	proxyReq.Header.Del("Proxy-Authenticate")
	proxyReq.Header.Del("Trailer")
	proxyReq.Header.Del("Transfer-Encoding")
	proxyReq.Header.Del("Upgrade")
```

**Why delete these?**
- They apply only to client↔proxy connection
- Forwarding them confuses backend
- HTTP spec requires proxies to remove them

Execute request:

```go
	resp, err := p.client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
```

**What's `client.Do`?**
- Sends HTTP request, returns response
- Uses connection pool in transport
- Returns error if backend unreachable

**Why `StatusBadGateway` (502)?**
- Standard status when proxy can't reach backend
- Distinguishes from 503 (service unavailable)
- Tells client the backend failed, not the proxy

**Why `defer resp.Body.Close()`?**
- Response body must be closed to free resources
- `defer` ensures it runs even if we return early
- Deferred calls run in LIFO order at function exit

Copy response headers:

```go
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
```

**Why copy all headers?**
- Backend might set important headers (Content-Type, Cache-Control)
- Client needs these to interpret response

**Why `Add` not `Set`?**
- Preserve multiple header values (like `Set-Cookie`)

Set status and copy body:

```go
	w.WriteHeader(resp.StatusCode)
	
	io.Copy(w, resp.Body)
}
```

**Why write status before body?**
- HTTP protocol: status line and headers before body
- Once you write body, can't change status

**What's `io.Copy`?**
- Streams data from source (resp.Body) to destination (w)
- Doesn't buffer entire response in memory
- Critical for large responses

**Why streaming matters:**
- 1GB response would use 1GB RAM if buffered
- Streaming uses fixed ~32KB buffer
- Starts sending to client while still receiving from backend

### Step 5: Implement getNextBackend (Simple Version)

Add method:

```go
func (p *Proxy) getNextBackend() *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	for _, backend := range p.backends {
		if backend.Alive {
			return backend
		}
	}
	
	return nil
}
```

**Why `RLock` not `Lock`?**
- We're only reading, not modifying
- `RLock` allows concurrent readers
- More performant when multiple requests happening

**Why `defer Unlock`?**
- Ensures we unlock even if we return early
- Critical: forgetting to unlock causes deadlock

**Why iterate until `Alive`?**
- Skip dead backends
- Return first healthy one found
- If all dead, loop completes and return `nil`

**Why is this temporary?**
- Always returns same backend (first alive one)
- No load balancing yet
- We'll replace this with proper balancer soon

---

Continued in next message...

### Step 6: Test Simple Proxy

First, update main.go to create and start proxy:

**File: `cmd/nexus/main.go`**

```go
package main

import (
0"log"
0"net/http"

0"github.com/VJ-2303/nexus/internal/config"
0"github.com/VJ-2303/nexus/internal/proxy"
)

func main() {
0cfg, err := config.Load("config.yaml")
0if err != nil {
0log.Fatalf("Failed to load config: %v", err)
0}

0backendURLs := make([]string, len(cfg.Backends))
0for i, b := range cfg.Backends {
0backendURLs[i] = b.URL
0}

0p, err := proxy.New(backendURLs)
0if err != nil {
0log.Fatalf("Failed to create proxy: %v", err)
0}

0log.Printf("Starting proxy on %s", cfg.ListenAddr)
0log.Printf("Proxying to %d backends", len(backendURLs))
0
0if err := http.ListenAndServe(cfg.ListenAddr, p); err != nil {
0log.Fatalf("Server failed: %v", err)
0}
}
```

**Why extract URLs into slice?**
- `proxy.New` expects `[]string`
- Config has `[]BackendConfig` (includes weight)
- Convert one to other

**What's `http.ListenAndServe`?**
- Starts HTTP server on specified address
- Accepts `Handler` interface - our `Proxy` implements it
- Blocks forever (or until error)

**Why pass `p` directly?**
- `Proxy` implements `http.Handler` (has `ServeHTTP` method)
- No need for additional routing

Create test backend:

**File: `test-backend.go` (in project root)**

```go
package main

import (
0"fmt"
0"log"
0"net/http"
0"os"
)

func main() {
0port := ":8081"
0if len(os.Args) > 1 {
0port = ":" + os.Args[1]
0}

0http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
0fmt.Fprintf(w, "Backend on port %s\n", port)
0fmt.Fprintf(w, "Path: %s\n", r.URL.Path)
0fmt.Fprintf(w, "X-Forwarded-For: %s\n", r.Header.Get("X-Forwarded-For"))
0})

0log.Printf("Backend starting on %s", port)
0log.Fatal(http.ListenAndServe(port, nil))
}
```

**Why accept port as arg?**
- Can run multiple instances: `go run test-backend.go 8081`, `go run test-backend.go 8082`
- Tests load balancing

**Why print headers?**
- Verifies proxy added `X-Forwarded-For`
- Good for debugging

Run test:
```bash
# Terminal 1: Start backend 1
go run test-backend.go 8081

# Terminal 2: Start backend 2
go run test-backend.go 8082

# Terminal 3: Start proxy
go run cmd/nexus/main.go

# Terminal 4: Test
curl http://localhost:8080/api/test
```

**Expected output:**
```
Backend on port :8081
Path: /api/test
X-Forwarded-For: 127.0.0.1:xxxxx
```

**What this proves:**
- Proxy forwards requests to backend
- Path preserved (`/api/test`)
- `X-Forwarded-For` added
- Response streams back to client

---

## Phase 5: Load Balancer Interface

### Step 1: Design Balancer Interface

**File: `internal/balancer/balancer.go`**

```go
package balancer

import (
0"net/http"
0"net/url"
)

type Backend struct {
0URL    *url.URL
0Alive  bool
0Weight int
}
```

**Why duplicate `Backend` type?**
- `balancer` package is separate from `proxy`
- Shouldn't depend on `proxy` package (circular dependency)
- This is the balancer's view of a backend

**Alternative approach:**
- Could put `Backend` in separate `types` package
- Both import from there
- More files but cleaner architecture
- For now, duplication is simpler

Add interface:

```go
type Balancer interface {
0Next(r *http.Request) *Backend
0UpdateBackends(backends []*Backend)
}
```

**Why `Next(r *http.Request)` takes request?**
- Some algorithms need request info
- IP-hash needs client IP (in `r.RemoteAddr`)
- Round-robin doesn't need it, but interface must be general

**Why return `*Backend` not index?**
- Cleaner API - caller doesn't need backend slice
- Can return `nil` if no healthy backends
- Backend pointer has all needed info (URL, etc.)

**Why `UpdateBackends`?**
- Health checker marks backends dead/alive
- Balancer needs updated list
- Allows runtime backend changes

### Step 2: Implement Round Robin

**File: `internal/balancer/roundrobin.go`**

```go
package balancer

import (
0"net/http"
0"sync"
0"sync/atomic"
)

type RoundRobin struct {
0backends []*Backend
0mu       sync.RWMutex
0current  uint64
}
```

**Why `uint64` for current?**
- Counter that increments forever
- No need for negative values
- 64-bit won't overflow in practice (2^64 requests)

**Why `atomic` package imported?**
- We'll use `atomic.AddUint64` for the counter
- Lock-free increment, faster than mutex

**Why both `mu` and `atomic`?**
- `mu` protects `backends` slice (read by many, written rarely)
- `atomic` operations on `current` counter (read/write by all)
- Different data, different protection mechanisms

Add constructor:

```go
func NewRoundRobin(backends []*Backend) *RoundRobin {
0return &RoundRobin{
0backends: backends,
0current:  0,
0}
}
```

**Why no error return?**
- Nothing can fail here
- Empty backend list will be handled in `Next()`

Implement `Next`:

```go
func (rr *RoundRobin) Next(r *http.Request) *Backend {
0rr.mu.RLock()
0defer rr.mu.RUnlock()
0
0if len(rr.backends) == 0 {
0return nil
0}
```

**Why `RLock`?**
- We're reading `backends` slice
- Multiple requests can read simultaneously
- `UpdateBackends` uses `Lock` for writes

**Why check length?**
- No backends = nothing to return
- Prevents panic on empty slice

Continue:

```go
0healthy := make([]*Backend, 0, len(rr.backends))
0for _, b := range rr.backends {
0if b.Alive {
0healthy = append(healthy, b)
0}
0}
0
0if len(healthy) == 0 {
0return nil
0}
```

**Why filter for healthy backends?**
- Don't send requests to dead backends
- Health checker sets `Alive = false`
- Return only from healthy pool

**Why capacity `len(rr.backends)`?**
- Pre-allocate assuming all healthy (best case)
- Avoids reallocations during append
- Slight over-allocation if some dead, but cheap

Add round-robin logic:

```go
0idx := atomic.AddUint64(&rr.current, 1) % uint64(len(healthy))
0return healthy[idx]
}
```

**Why `atomic.AddUint64`?**
- Increments counter atomically (thread-safe)
- Returns new value
- No mutex needed - faster

**Why `% len(healthy)`?**
- Modulo wraps counter back to 0
- Counter is 0, 1, 2, ..., N-1, 0, 1, ...
- Cycles through backends

**Example with 3 backends:**
- Request 1: current=1, idx=1%3=1 → backend[1]
- Request 2: current=2, idx=2%3=2 → backend[2]
- Request 3: current=3, idx=3%3=0 → backend[0]
- Request 4: current=4, idx=4%3=1 → backend[1]
- ...

**Why filter healthy INSIDE lock?**
- `healthy` slice is local (not shared)
- Filtering happens on this request's stack
- No concurrent access to `healthy`

Implement `UpdateBackends`:

```go
func (rr *RoundRobin) UpdateBackends(backends []*Backend) {
0rr.mu.Lock()
0defer rr.mu.Unlock()
0rr.backends = backends
}
```

**Why `Lock` not `RLock`?**
- We're modifying `backends` slice (write operation)
- Exclusive lock needed
- Blocks all readers until done

**Why replace entire slice?**
- Simpler than updating in place
- Caller (health checker) builds new slice with updated states
- Single atomic replacement

### Step 3: Integrate Balancer with Proxy

Update `proxy.go` to use balancer:

**File: `internal/proxy/proxy.go`**

Add import:
```go
import (
0"github.com/VJ-2303/nexus/internal/balancer"
)
```

Update `Proxy` struct:
```go
type Proxy struct {
0balancer balancer.Balancer
0client   *http.Client
}
```

**Why store `Balancer` interface, not `*RoundRobin`?**
- Dependency on abstraction, not concrete type
- Can swap algorithms without changing `Proxy`
- Follows SOLID principles (Dependency Inversion)

Update constructor:
```go
func New(backendURLs []string, bal balancer.Balancer) (*Proxy, error) {
0if len(backendURLs) == 0 {
0return nil, fmt.Errorf("no backends provided")
0}

0client := &http.Client{
0Timeout: 30 * time.Second,
0Transport: &http.Transport{
0MaxIdleConns:        100,
0MaxIdleConnsPerHost: 10,
0IdleConnTimeout:     90 * time.Second,
0},
0}

0return &Proxy{
0balancer: bal,
0client:   client,
0}, nil
}
```

**Why pass balancer as parameter?**
- Caller decides which algorithm
- `Proxy` doesn't care about algorithm details
- Easier to test (can pass mock balancer)

Update `ServeHTTP`:
```go
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
0backend := p.balancer.Next(r)
0if backend == nil {
0http.Error(w, "No healthy backends", http.StatusServiceUnavailable)
0return
0}
0
0// ... rest of ServeHTTP stays same, just use backend.URL ...
}
```

**What changed?**
- Removed `getNextBackend()` method
- Call `p.balancer.Next(r)` instead
- Balancer handles the selection logic

### Step 4: Update main.go

**File: `cmd/nexus/main.go`**

```go
func main() {
0cfg, err := config.Load("config.yaml")
0if err != nil {
0log.Fatalf("Failed to load config: %v", err)
0}

0backends := make([]*balancer.Backend, len(cfg.Backends))
0for i, b := range cfg.Backends {
0u, err := url.Parse(b.URL)
0if err != nil {
0log.Fatalf("Invalid backend URL %q: %v", b.URL, err)
0}
0backends[i] = &balancer.Backend{
0URL:    u,
0Alive:  true,
0Weight: b.Weight,
0}
0}

0var bal balancer.Balancer
0switch cfg.Balancer {
0case "roundrobin":
0bal = balancer.NewRoundRobin(backends)
0default:
0log.Fatalf("Unknown balancer: %s", cfg.Balancer)
0}

0backendURLs := make([]string, len(cfg.Backends))
0for i, b := range cfg.Backends {
0backendURLs[i] = b.URL
0}

0p, err := proxy.New(backendURLs, bal)
0if err != nil {
0log.Fatalf("Failed to create proxy: %v", err)
0}

0log.Printf("Starting proxy on %s", cfg.ListenAddr)
0log.Printf("Using %s balancer with %d backends", cfg.Balancer, len(backends))
0
0if err := http.ListenAndServe(cfg.ListenAddr, p); err != nil {
0log.Fatalf("Server failed: %v", err)
0}
}
```

**Why switch on `cfg.Balancer`?**
- Create appropriate balancer based on config
- Extensible: add more cases for new algorithms
- Fail fast if unknown algorithm

**Why parse URLs here?**
- Config has URL strings
- Balancer needs `*url.URL`
- Parse once at startup, not per request

Add missing import:
```go
import (
0"log"
0"net/http"
0"net/url"

0"github.com/VJ-2303/nexus/internal/balancer"
0"github.com/VJ-2303/nexus/internal/config"
0"github.com/VJ-2303/nexus/internal/proxy"
)
```

### Step 5: Test Load Balancing

Run same test as before:
```bash
# Terminal 1
go run test-backend.go 8081

# Terminal 2
go run test-backend.go 8082

# Terminal 3
go run cmd/nexus/main.go

# Terminal 4
curl http://localhost:8080/test
curl http://localhost:8080/test
curl http://localhost:8080/test
curl http://localhost:8080/test
```

**Expected output (alternating):**
```
Backend on port :8081
...
Backend on port :8082
...
Backend on port :8081
...
Backend on port :8082
```

**What this proves:**
- Round-robin cycles through backends
- Each backend gets roughly equal traffic
- No backend is skipped

---

## Checkpoint

You now have:
- ✅ Working reverse proxy that forwards HTTP requests
- ✅ Header manipulation (X-Forwarded-For, hop-by-hop stripping)
- ✅ Response streaming (no buffering)
- ✅ Connection pooling via http.Transport
- ✅ Balancer interface with round-robin implementation
- ✅ Atomic operations for lock-free counter
- ✅ Proper RWMutex usage for concurrent reads/writes

**Test Your Understanding:**

Modify the round-robin to respect backend weights:

1. If backend has weight 2, it should get twice as many requests
2. Hint: build `healthy` slice with duplicates based on weight
3. Example: weights [1, 2] → healthy = [backend0, backend1, backend1]
4. Round-robin over this expanded list

If you can implement this, you understand the core mechanics.

---

## What's Next: Part 3

**Part 3** covers:
- Health checking with goroutines and state machines
- Circuit breaker pattern
- Structured logging with slog
- Metrics collection with atomic counters
- Admin API for runtime inspection
- Graceful shutdown

You'll learn:
- Goroutine lifecycle and leak prevention
- Context cancellation for cleanup
- State machines for circuit breaker
- Why atomic counters beat mutexes for metrics
- How graceful shutdown prevents data loss
