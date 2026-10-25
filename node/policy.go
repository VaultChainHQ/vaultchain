package node

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/VaultChainHQ/vaultchain/x/ibc"
)

// AdminTokenHeader carries the node admin secret on REST tx submission.
const AdminTokenHeader = "X-Admin-Token"

// Node-local submission policy for the public REST endpoint. None of this is
// consensus: it decides what this node admits to its mempool and from whom.

// restricted reports whether msg may only be submitted by the node admin.
// Lab-only messages mint value out of thin air, so a node that is reachable by
// strangers must not relay them from anyone else (even when genesis enables
// lab mode).
func restricted(msg any) bool {
	switch msg.(type) {
	case ibc.MsgSimulateIBCDeposit:
		return true
	}
	return false
}

// isAdmin reports whether the request carries the configured admin token.
// With no token configured nobody is admin.
func (e *Engine) isAdmin(r *http.Request) bool {
	if e.cfg.AdminToken == "" {
		return false
	}
	got := r.Header.Get(AdminTokenHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(e.cfg.AdminToken)) == 1
}

// checkPublicTx applies the REST-only policy to a raw tx.
func (e *Engine) checkPublicTx(raw []byte, admin bool) error {
	dt, err := DecodeTx(raw)
	if err != nil {
		return err
	}
	if admin {
		return nil
	}
	for _, m := range dt.Msgs {
		if restricted(m) {
			return errRestricted
		}
	}
	return nil
}

var errRestricted = fmt.Errorf("this message type is restricted to the node admin (send the %s header)", AdminTokenHeader)

// clientIP identifies the caller for rate limiting. ClientIPHeader (for example
// Fly-Client-IP behind Fly's proxy, which overwrites it) is trusted only when
// configured, because otherwise any client could spoof it.
func (e *Engine) clientIP(r *http.Request) string {
	if h := e.cfg.ClientIPHeader; h != "" {
		if v := strings.TrimSpace(strings.Split(r.Header.Get(h), ",")[0]); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter is a per-key token bucket: perMinute sustained, burst = perMinute/2 (min 5).
type rateLimiter struct {
	mu        sync.Mutex
	perMinute int
	burst     float64
	buckets   map[string]*bucket
	now       func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	burst := float64(perMinute) / 2
	if burst < 5 {
		burst = 5
	}
	return &rateLimiter{perMinute: perMinute, burst: burst, buckets: map[string]*bucket{}, now: time.Now}
}

// allow takes one token for key. It returns false and a retry hint when empty.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) > 10000 { // bound memory: drop idle buckets
			for k, v := range l.buckets {
				if now.Sub(v.last) > 5*time.Minute {
					delete(l.buckets, k)
				}
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * float64(l.perMinute) / 60
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) / float64(l.perMinute) * float64(time.Minute))
	}
	b.tokens--
	return true, 0
}

// exemptFromRateLimit is true for loopback and private (including Fly 6PN)
// callers when no proxy header is in use: providers and admins on the private
// network are not the public.
func (e *Engine) exemptFromRateLimit(r *http.Request) bool {
	if e.cfg.ClientIPHeader != "" && r.Header.Get(e.cfg.ClientIPHeader) != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
