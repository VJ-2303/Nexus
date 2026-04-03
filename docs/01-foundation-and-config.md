# NEXUS - Part 1: Foundation & Configuration

**Goal:** Build project structure and configuration system with deep understanding of every decision.

---

## Phase 0: Project Setup

### Step 1: Initialize Module

```bash
mkdir -p ~/Code/nexus
cd ~/Code/nexus
go mod init github.com/VJ-2303/nexus
```

**What `go mod init` does:**
- Creates `go.mod` file - this declares your module
- The path `github.com/VJ-2303/nexus` is your module's identity
- Other code imports your packages using this path: `import "github.com/VJ-2303/nexus/internal/config"`
- The path looks like a URL but doesn't have to exist on GitHub yet
- Go uses this for dependency resolution in the module graph

**Why modules matter:** Before modules, Go used `$GOPATH`. Modules let you work anywhere and have reproducible builds via `go.sum` checksums.

### Step 2: Create Directory Structure

```bash
mkdir -p cmd/nexus
mkdir -p internal/config
mkdir -p internal/proxy
mkdir -p internal/balancer
mkdir -p internal/health
mkdir -p internal/pool
mkdir -p internal/metrics
mkdir -p internal/circuit
mkdir -p internal/admin
```

**Why `cmd/`?**
- Standard Go convention for executable commands
- `cmd/nexus/` will contain `main.go` with `package main`
- If you had multiple binaries (e.g., `cmd/admin-cli/`), each gets its own directory

**Why `internal/`?**
- Go compiler enforces: code outside your module CANNOT import `internal/` packages
- This is encapsulation at the toolchain level
- Example: someone imports your module but can't import `github.com/VJ-2303/nexus/internal/proxy` - compile error
- Prevents accidental API exposure

**Why these specific packages?**
- `config` - configuration loading and validation
- `proxy` - core HTTP forwarding logic
- `balancer` - algorithm for picking which backend gets the request
- `health` - checks if backends are alive
- `pool` - manages HTTP connection reuse
- `metrics` - counters for requests/errors/latency
- `circuit` - circuit breaker to protect failing backends
- `admin` - HTTP API for runtime inspection

### Step 3: Create Empty Files

```bash
touch cmd/nexus/main.go
touch internal/config/config.go
touch config.yaml
```

We'll create other files as needed.

### Step 4: Initialize Packages

**File: `cmd/nexus/main.go`**
```go
package main

func main() {}
```

**Why `package main`?**
- Special package name that creates an executable
- Must have a `main()` function - the entry point
- `go build` on a `main` package produces a binary

**File: `internal/config/config.go`**
```go
package config
```

**Why `package config`?**
- Package name matches directory name (convention)
- Imported as `import "github.com/VJ-2303/nexus/internal/config"`
- Referenced in code as `config.Load()`

### Step 5: Validate Structure

```bash
go build ./...
```

**What `./...` means:**
- `.` is current directory
- `...` means "and all subdirectories recursively"
- So `./...` = "build every package in this module"

Should succeed silently (empty packages are valid).

---

## Phase 1: HTTP Handler Model

Before writing code, understand Go's HTTP system.

**Core Interface:**
```go
type Handler interface {
    ServeHTTP(ResponseWriter, *Request)
}
```

**Why this matters:**
- Everything that handles HTTP implements this
- `http.ListenAndServe` accepts any `Handler`
- Middleware wraps handlers to add behavior
- Your proxy will implement `Handler`

**Request Flow:**
```
Client sends TCP connection
    ↓
ListenAndServe accepts connection
    ↓
Spawns goroutine (one per request)
    ↓
Calls your Handler's ServeHTTP
    ↓
You write response
    ↓
Connection closes or reuses (keep-alive)
```

**Critical Rule:** Headers must be set BEFORE first `Write()` to body.

**Why?** HTTP protocol sends headers before body. Once you write body bytes, headers are already on the wire. Can't change them.

---

## Phase 2: Configuration System

### Step 1: Define Sentinel Errors

**File: `internal/config/config.go`**

Add after `package config`:
```go
import "errors"

var (
	ErrNoBackends = errors.New("no backends configured")
)
```

**Why a `var` block?**
- Groups related declarations
- Common Go style for package-level variables

**Why `errors.New()`?**
- Creates a sentinel error - a specific error value
- Callers can check: `if errors.Is(err, ErrNoBackends)`
- Even if error is wrapped, `errors.Is()` unwraps and checks

**When to use sentinel errors:**
- When caller needs to handle specific errors differently
- Example: `ErrNoBackends` might mean "fail fast", but a parse error might mean "show validation message"

Add two more:
```go
var (
	ErrNoBackends        = errors.New("no backends configured")
	ErrInvalidAlgorithm  = errors.New("invalid balancing algorithm")
	ErrInvalidListenAddr = errors.New("invalid listen address")
)
```

### Step 2: Define Config Struct

Add after the error declarations:

```go
import "time"

type Config struct {
	ListenAddr string `yaml:"listen_addr"`
}
```

**Why `ListenAddr string`?**
- The address the proxy listens on (e.g., `:8080` or `127.0.0.1:8080`)
- String because it's a network address in Go's format

**What's `yaml:"listen_addr"`?**
- A struct tag - metadata on the field
- The `yaml` package reads this via reflection at runtime
- Maps YAML key `listen_addr` to field `ListenAddr`
- Without tag, it would look for `ListenAddr` (exact match)
- Tags let you use snake_case in YAML, PascalCase in Go

Add more fields one by one:

```go
type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	AdminAddr  string `yaml:"admin_addr"`
}
```

**Why separate `AdminAddr`?**
- Admin API runs on different port from main proxy
- Security: admin endpoints (metrics, backend management) shouldn't be public
- Example: proxy on `:8080`, admin on `127.0.0.1:9090` (localhost only)

Add:
```go
type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	AdminAddr  string `yaml:"admin_addr"`
	Balancer   string `yaml:"balancer"`
}
```

**Why `Balancer string`?**
- Specifies which load balancing algorithm to use
- Could be `"roundrobin"`, `"leastconn"`, `"iphash"`
- String for simplicity - we'll validate it later
- Alternative: could use an `enum` type, but strings are flexible for config

Add:
```go
type Config struct {
	ListenAddr string `yaml:"listen_addr"`
	AdminAddr  string `yaml:"admin_addr"`
	Balancer   string `yaml:"balancer"`
	LogLevel   string `yaml:"log_level"`
}
```

**Why `LogLevel string`?**
- Controls logging verbosity: `"debug"`, `"info"`, `"warn"`, `"error"`
- String so it's human-readable in config files
- We'll use this to configure `slog` logger later

Now add the complex field:
```go
type Config struct {
	ListenAddr string          `yaml:"listen_addr"`
	AdminAddr  string          `yaml:"admin_addr"`
	Balancer   string          `yaml:"balancer"`
	LogLevel   string          `yaml:"log_level"`
	Backends   []BackendConfig `yaml:"backends"`
}
```

**Why `[]BackendConfig` slice?**
- A proxy needs multiple backend servers
- Slice = dynamic size, can have 2 or 200 backends
- `BackendConfig` is a struct we'll define next
- YAML will parse a list of backends into this slice

### Step 3: Define BackendConfig

Add after `Config`:

```go
type BackendConfig struct {
	URL string `yaml:"url"`
}
```

**Why `URL string`?**
- The backend server's address: `"http://localhost:8081"`
- String because it's a URL in text format
- We'll parse it into `*url.URL` later when we use it

Add weight:
```go
type BackendConfig struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}
```

**Why `Weight int`?**
- For weighted load balancing
- Backend with `weight: 2` gets twice as many requests as `weight: 1`
- Useful when backends have different capacities
- Default to `1` if not specified (we'll add that validation)

### Step 4: Add Health Config

Add to `Config` struct:
```go
type Config struct {
	ListenAddr string          `yaml:"listen_addr"`
	AdminAddr  string          `yaml:"admin_addr"`
	Balancer   string          `yaml:"balancer"`
	LogLevel   string          `yaml:"log_level"`
	Backends   []BackendConfig `yaml:"backends"`
	Health     HealthConfig    `yaml:"health"`
}
```

**Why `HealthConfig` as a nested struct?**
- Health checking has multiple related settings
- Grouping them keeps config organized
- In YAML: `health:` section with sub-fields

Define `HealthConfig`:
```go
type HealthConfig struct {
	Interval time.Duration `yaml:"interval"`
}
```

**Why `time.Duration`?**
- Go's type for representing time spans
- YAML package can parse `"10s"`, `"5m"`, `"1h"` into `Duration`
- Type-safe: can't accidentally mix up seconds and milliseconds

**Why `Interval`?**
- How often to check if backends are alive
- Example: every 10 seconds, send a request to `/health` endpoint
- Too frequent = wasted requests, too rare = slow failure detection

Add more health fields:
```go
type HealthConfig struct {
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
	PassThreshold int           `yaml:"pass_threshold"`
	FailThreshold int           `yaml:"fail_threshold"`
}
```

**Why `Timeout`?**
- Max time to wait for health check response
- If backend takes longer, consider it unhealthy
- Prevents hanging on a slow backend

**Why `PassThreshold`?**
- Number of consecutive successful checks to mark backend healthy
- Example: `2` means need 2 passes in a row
- Prevents flapping: one lucky response doesn't mean backend is stable

**Why `FailThreshold`?**
- Number of consecutive failures to mark unhealthy
- Example: `3` means 3 fails in a row before removing from rotation
- Same reason: avoid flapping on transient errors

### Step 5: Add Transport Config

Add to `Config`:
```go
type Config struct {
	ListenAddr string          `yaml:"listen_addr"`
	AdminAddr  string          `yaml:"admin_addr"`
	Balancer   string          `yaml:"balancer"`
	LogLevel   string          `yaml:"log_level"`
	Backends   []BackendConfig `yaml:"backends"`
	Health     HealthConfig    `yaml:"health"`
	Transport  TransportConfig `yaml:"transport"`
}
```

**Why Transport config?**
- `http.Transport` manages connection pools to backends
- Default settings are conservative - bad for a proxy
- We need to tune it

Define `TransportConfig`:
```go
type TransportConfig struct {
	MaxIdleConns int `yaml:"max_idle_conns"`
}
```

**Why `MaxIdleConns`?**
- Maximum idle (reusable) connections across ALL hosts
- Idle = connection that's open but not actively sending data
- Reusing connections is MUCH faster than creating new ones (no TCP handshake)
- Setting this too low = connections constantly close and reopen
- Too high = memory waste

Add more:
```go
type TransportConfig struct {
	MaxIdleConns        int           `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int           `yaml:"max_idle_conns_per_host"`
	IdleConnTimeout     time.Duration `yaml:"idle_conn_timeout"`
	ResponseTimeout     time.Duration `yaml:"response_timeout"`
}
```

**Why `MaxIdleConnsPerHost`?**
- Maximum idle connections PER backend
- Controls per-backend connection pool size
- Example: 100 total, 10 per host = can have 10 hosts with full pools

**Why `IdleConnTimeout`?**
- How long an idle connection stays in the pool
- After this time, connection closes automatically
- Balances reuse (fast) vs resource cleanup
- Example: 90s is a good default

**Why `ResponseTimeout`?**
- Maximum time to wait for backend response
- Protects against slow backends hanging the proxy
- Includes time to read full response body

---

Your complete `config.go` should now have all these types defined.

### Step 6: Add Load Function

**Continue in `internal/config/config.go`**

Update imports at top:
```go
import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)
```

Add the `Load` function:
```go
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
```

**Why `os.ReadFile`?**
- Reads entire file into memory as `[]byte`
- Returns error if file doesn't exist or isn't readable
- Simple for small config files (not suitable for GB files)

**Why `return nil, fmt.Errorf(...)`?**
- On error, we return `nil` pointer (no config) and an error
- Go convention: return `(result, error)`, check error first
- The error describes what we were trying to do: "reading config file"

**What's `%w`?**
- Error wrapping verb
- Preserves original error so caller can use `errors.Is(err, os.ErrNotExist)`
- Without `%w`, the original error is converted to string and lost

Continue the function:
```go
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing yaml: %w", err)
	}
```

**Why `var cfg Config`?**
- Creates a zero-value `Config` struct
- All fields are zero: strings are `""`, ints are `0`, slices are `nil`

**Why `&cfg`?**
- `Unmarshal` needs a pointer to fill the struct
- It modifies `cfg` in place
- Without `&`, passing a copy wouldn't work

**What does `Unmarshal` do?**
- Parses YAML bytes into Go struct
- Uses struct tags to map YAML keys to fields
- Handles nested structs automatically

Continue:
```go
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}
```

**Why validate after parsing?**
- YAML parser checks syntax, not semantics
- Parser allows `backends: []` (empty list), but we need at least one
- Validation enforces business rules

**Why return `&cfg, nil`?**
- Success case: return pointer to config, no error
- Pointer because `Config` might be large (many backends)
- Avoids copying the entire struct

### Step 7: Add Validate Method

```go
func (c *Config) Validate() error {
	if c.ListenAddr == "" {
		return ErrInvalidListenAddr
	}
```

**What's `(c *Config)`?**
- Method receiver - makes this a method on `Config`
- Called as `cfg.Validate()`
- Pointer receiver (`*Config`) because it's idiomatic for methods, even if we don't modify

**Why check `ListenAddr == ""`?**
- Empty address would make `ListenAndServe` fail
- Better to catch at startup than runtime

Continue:
```go
	if len(c.Backends) == 0 {
		return ErrNoBackends
	}
```

**Why check backend count?**
- A proxy with no backends is useless
- Fail fast with clear error message

Add algorithm validation:
```go
	validAlgos := map[string]bool{
		"roundrobin": true,
		"leastconn":  true,
		"iphash":     true,
	}
	if !validAlgos[c.Balancer] {
		return ErrInvalidAlgorithm
	}
```

**Why a map for validation?**
- Fast O(1) lookup
- Clear list of valid values
- Idiomatic Go pattern for membership testing

**Why not a switch?**
- Map is more maintainable - easy to add new algorithms
- Could use switch, but map scales better

Add default handling:
```go
	if c.Health.PassThreshold < 1 {
		c.Health.PassThreshold = 2
	}
	if c.Health.FailThreshold < 1 {
		c.Health.FailThreshold = 3
	}

	return nil
}
```

**Why set defaults?**
- If user doesn't specify, we use sensible defaults
- `2` passes = stable enough to trust
- `3` fails = clearly having problems
- Prevents invalid values like `0` (would never mark anything unhealthy)

**Why return `nil`?**
- `nil` error means success
- Go idiom: `return nil` for "no error occurred"

---

Continued in next message...

### Step 8: Create config.yaml

**File: `config.yaml` (in project root)**

Start with listen addresses:
```yaml
listen_addr: ":8080"
admin_addr: ":9090"
```

**Why `:8080`?**
- Colon with no IP means "listen on all interfaces"
- `8080` is a common HTTP alternate port
- Equivalent to `0.0.0.0:8080`

**Why `127.0.0.1:9090` for admin?**
- Actually, use `:9090` for testing, but in production you'd use `127.0.0.1:9090`
- `127.0.0.1` = localhost only (not accessible from network)
- Keeps admin API private

Add algorithm and log level:
```yaml
listen_addr: ":8080"
admin_addr: ":9090"
balancer: "roundrobin"
log_level: "info"
```

**Why `"roundrobin"`?**
- Simplest algorithm - cycles through backends
- Good default before you know traffic patterns

**Why `"info"` log level?**
- Balance between verbosity and quiet
- `debug` = too noisy for production
- `warn` = might miss important events

Add backends:
```yaml
listen_addr: ":8080"
admin_addr: ":9090"
balancer: "roundrobin"
log_level: "info"

backends:
  - url: "http://localhost:8081"
    weight: 1
  - url: "http://localhost:8082"
    weight: 1
```

**Why this format?**
- YAML list (array) with `-` prefix for each item
- Each item is a map with `url` and `weight`
- Indentation matters in YAML (2 spaces per level)

**Why localhost:8081/8082?**
- For testing, we'll run two simple backend servers
- Different ports on same machine
- In production, these would be different hosts

Add health config:
```yaml
health:
  interval: 10s
  timeout: 5s
  pass_threshold: 2
  fail_threshold: 3
```

**Why `10s` interval?**
- Checks every 10 seconds
- Frequent enough to detect failures quickly
- Not so frequent it wastes resources

**Why `5s` timeout?**
- If backend doesn't respond in 5s, it's too slow
- Should be less than interval (otherwise checks pile up)

Add transport config:
```yaml
transport:
  max_idle_conns: 100
  max_idle_conns_per_host: 10
  idle_conn_timeout: 90s
  response_timeout: 30s
```

**Why `100` max idle conns?**
- Can handle bursts of traffic without creating new connections
- Not so large it wastes memory
- Tune based on expected load

**Why `10` per host?**
- Each backend gets up to 10 idle connections
- With 2 backends = up to 20 connections
- Prevents one backend hogging all connections

**Why `90s` idle timeout?**
- Common value, balances reuse and cleanup
- Longer than most request intervals
- Shorter than most server idle timeouts (prevents surprise closes)

**Why `30s` response timeout?**
- Protects against hung backends
- Should be longer than expected slowest response
- Shorter than client's patience

### Step 9: Install Dependencies

```bash
go get gopkg.in/yaml.v3
go mod tidy
```

**What does `go get` do?**
- Downloads the module `gopkg.in/yaml.v3`
- Adds it to `go.mod` as a requirement
- Updates `go.sum` with cryptographic checksums

**What does `go mod tidy` do?**
- Adds missing requirements by scanning imports
- Removes unused requirements
- Updates `go.sum`
- Should be run after changing imports

**Why `gopkg.in/yaml.v3`?**
- This is a stable YAML parser
- `v3` = version 3 API
- The `.v3` suffix is part of the import path (gopkg.in convention)

### Step 10: Test Configuration Loading

**File: `cmd/nexus/main.go`**

```go
package main

import (
 "fmt"
 "log"

 "github.com/VJ-2303/nexus/internal/config"
)
```

**Import structure:**
- Standard library first (`fmt`, `log`)
- Blank line
- External dependencies next (none yet)
- Blank line
- Your own packages (`github.com/VJ-2303/nexus/internal/config`)

**Why this order?**
- Go convention, enforced by `gofmt`
- Readable: standard lib → external → internal

Add main function:
```go
func main() {
 cfg, err := config.Load("config.yaml")
 if err != nil {
  log.Fatalf("Failed to load config: %v", err)
 }
```

**Why check `err` immediately?**
- Go idiom: handle errors at point of occurrence
- `if err != nil` is the error handling pattern
- Every function that returns `error` must be checked

**What does `log.Fatalf` do?**
- Prints formatted error message
- Calls `os.Exit(1)` - terminates program with error code
- Use for unrecoverable startup errors

**Why `%v` verb?**
- Generic "value" formatter
- Works for any type, uses default formatting
- For errors, prints the error message

Add printing logic:
```go
 fmt.Printf("Loaded config:\n")
 fmt.Printf("  Listen: %s\n", cfg.ListenAddr)
 fmt.Printf("  Admin: %s\n", cfg.AdminAddr)
 fmt.Printf("  Balancer: %s\n", cfg.Balancer)
 fmt.Printf("  Backends: %d\n", len(cfg.Backends))
```

**Why `fmt.Printf` here?**
- We're printing structured output for human verification
- Not logging - just showing what we loaded
- `Printf` formats and prints to stdout

**Why `%s` for strings, `%d` for ints?**
- Type-specific format verbs
- `%s` = string
- `%d` = decimal integer
- Could use `%v` for everything, but specific verbs are clearer

Add backend details:
```go
 for i, b := range cfg.Backends {
  fmt.Printf("    [%d] %s (weight: %d)\n", i, b.URL, b.Weight)
 }
```

**What's `range`?**
- Iterates over slice
- Returns index `i` and value `b`
- Like `for (int i = 0; i < len(arr); i++)` but cleaner

**Why print index?**
- Shows ordering (matters for round-robin)
- Helps debug if backends are wrong

Add health info:
```go
 fmt.Printf("  Health check interval: %v\n", cfg.Health.Interval)
}
```

**Why `%v` for Duration?**
- `Duration` has a custom String() method
- Prints as `10s`, `5m30s`, etc.
- More readable than nanoseconds

### Step 11: Run and Verify

```bash
go run cmd/nexus/main.go
```

**What `go run` does:**
- Compiles `cmd/nexus/main.go` to a temporary binary
- Runs the binary
- Cleans up temp file
- Convenient for testing, not for production builds

**Expected output:**
```
Loaded config:
  Listen: :8080
  Admin: :9090
  Balancer: roundrobin
  Backends: 2
    [0] http://localhost:8081 (weight: 1)
    [1] http://localhost:8082 (weight: 1)
  Health check interval: 10s
```

**What this proves:**
- Config file loads successfully
- YAML parsing works
- Struct tags mapped correctly
- Validation passed
- Default values applied (if you didn't set thresholds)

### Step 12: Test Error Handling

Try invalid config to verify validation works.

**Create `bad-config.yaml`:**
```yaml
listen_addr: ":8080"
admin_addr: ":9090"
balancer: "invalid_algorithm"
log_level: "info"
backends: []
```

**Run:**
```bash
go run cmd/nexus/main.go
```

Change the Load call temporarily to:
```go
cfg, err := config.Load("bad-config.yaml")
```

**Expected output:**
```
Failed to load config: validating config: no backends configured
```

**What this proves:**
- Validation catches `len(backends) == 0`
- Error wrapping preserves context ("validating config")
- Sentinel errors work

Change backends to `[{url: "http://localhost:8081", weight: 1}]` and run again:
```
Failed to load config: validating config: invalid balancing algorithm
```

**Proves:** Algorithm validation works.

Change back to `config.yaml` after testing.

---

## Checkpoint

You now have:
- ✅ Module initialized with proper structure
- ✅ Configuration system that loads, parses, and validates YAML
- ✅ Deep understanding of:
  - Why every field exists
  - Why we use struct tags
  - How error wrapping works
  - Why we validate config
  - Method receivers and pointers

**Test Your Understanding:**

Add a new field to `TransportConfig`:

1. **Field:** `MaxConnsPerHost int` with yaml tag `max_conns_per_host`
2. **Purpose:** Limits total (active + idle) connections per backend
3. **Validation:** Set default to `50` if less than 1
4. **Config:** Add `max_conns_per_host: 50` to `config.yaml`
5. **Main:** Print it: `fmt.Printf("  Max conns per host: %d\n", cfg.Transport.MaxConnsPerHost)`

If you can do this end-to-end without looking back, you understand the pattern.

---

## What's Next: Part 2

**Part 2** covers:
- HTTP reverse proxying fundamentals (request flow, headers)
- Building a working proxy that forwards to ONE backend
- Load balancer interface design
- Round-robin algorithm implementation
- Connection pooling with `http.Transport`

You'll learn:
- Why `X-Forwarded-For` exists
- What hop-by-hop headers are
- Why streaming responses matters
- How connection pooling saves massive time
- Why atomic operations beat mutexes for counters
