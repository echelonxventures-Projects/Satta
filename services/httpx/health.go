package httpx

import (
	"context"
	"net/http"
	"sync/atomic"
)

// Check reports whether a dependency is healthy. Checks are the readiness
// contract's port (DoD gate 2): services register closures over their pools
// and clients; healthx never imports infrastructure directly.
type Check func(ctx context.Context) error

// Checker aggregates readiness checks behind /readyz and liveness behind
// /healthz.
//
// The distinction is what lets a rolling deploy not drop traffic:
// liveness failing restarts the process, readiness failing removes it from
// load balancers. During graceful drain a pod is still alive — it is
// finishing in-flight work — but it is no longer ready.
type Checker struct {
	checks   []namedCheck
	draining atomic.Bool
}

type namedCheck struct {
	name string
	fn   Check
}

// NewChecker returns a Checker with no dependency checks; liveness works
// immediately, readiness reports ready until a check fails or drain starts.
func NewChecker() *Checker {
	return &Checker{}
}

// Register adds a named readiness check.
func (c *Checker) Register(name string, fn Check) {
	c.checks = append(c.checks, namedCheck{name: name, fn: fn})
}

// BeginDrain marks the process unready (so the load balancer stops sending
// new work) while it finishes what it is serving.
func (c *Checker) BeginDrain() { c.draining.Store(true) }

// IsDraining reports whether the checker is draining.
func (c *Checker) IsDraining() bool { return c.draining.Load() }

// ReadyHandler serves /readyz: 200 only when not draining and every check
// passes. Failed check names are reported in the body (the names are static
// configuration, not caller data) so an operator can see what broke without
// reading pod logs. Bodies go out via http.Error (no return value): probe
// consumers act on the status code, the text is for humans.
func (c *Checker) ReadyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.draining.Load() {
			http.Error(w, `{"status":"draining"}`, http.StatusServiceUnavailable)
			return
		}
		for _, chk := range c.checks {
			if err := chk.fn(r.Context()); err != nil {
				http.Error(w, `{"status":"unready","failed":"`+chk.name+`"}`, http.StatusServiceUnavailable)
				return
			}
		}
		http.Error(w, `{"status":"ready"}`, http.StatusOK)
	})
}

// LiveHandler serves /healthz: 200 while the process is running. Liveness
// deliberately does not consult dependencies — a database outage must not
// make Kubernetes restart every service simultaneously and turn a partial
// outage into a stampede.
func LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"status":"alive"}`, http.StatusOK)
	})
}
