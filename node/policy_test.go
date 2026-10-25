package node

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VaultChainHQ/vaultchain/x/ibc"
)

func policyEngine(t *testing.T, cfg Config, lab bool, funded ...testAcct) *Engine {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteGenesis(home, testGenesis(lab, funded...)); err != nil {
		t.Fatal(err)
	}
	cfg.Home = home
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func post(h http.Handler, remote string, hdr map[string]string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/vaultchain/txs", bytes.NewReader(body))
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRestrictedMsgNeedsAdminToken(t *testing.T) {
	alice := newAcct(t)
	e := policyEngine(t, Config{AdminToken: "s3cret"}, true, alice)
	h := e.RESTHandler()
	mint := func(nonce uint64) []byte {
		return alice.tx(t, testChain, nonce, uv(10), ibc.MsgSimulateIBCDeposit{Type: ibc.MsgTypeSimulateDeposit, To: alice.addr, Denom: "ibc/ATOM", Amount: 5})
	}
	if w := post(h, "203.0.113.5:1", nil, mint(0)); w.Code != http.StatusForbidden {
		t.Fatalf("anonymous mint: %d %s", w.Code, w.Body)
	}
	if w := post(h, "203.0.113.5:1", map[string]string{AdminTokenHeader: "wrong"}, mint(0)); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token mint: %d", w.Code)
	}
	if w := post(h, "203.0.113.5:1", map[string]string{AdminTokenHeader: "s3cret"}, mint(0)); w.Code != http.StatusOK {
		t.Fatalf("admin mint: %d %s", w.Code, w.Body)
	}
	// An ordinary signed tx from a stranger still works.
	send := alice.tx(t, testChain, 1, uv(10), MsgBankSend{Type: MsgTypeBankSend, To: newAcct(t).addr, Amount: uv(1)})
	if w := post(h, "203.0.113.5:1", nil, send); w.Code != http.StatusOK {
		t.Fatalf("normal tx: %d %s", w.Code, w.Body)
	}
}

func TestNoAdminTokenConfiguredMeansNobodyIsAdmin(t *testing.T) {
	alice := newAcct(t)
	e := policyEngine(t, Config{}, true, alice)
	mint := alice.tx(t, testChain, 0, uv(10), ibc.MsgSimulateIBCDeposit{Type: ibc.MsgTypeSimulateDeposit, To: alice.addr, Denom: "ibc/ATOM", Amount: 5})
	// An empty header must not match an empty configured token.
	if w := post(e.RESTHandler(), "203.0.113.5:1", map[string]string{AdminTokenHeader: ""}, mint); w.Code != http.StatusForbidden {
		t.Fatalf("mint with no admin token configured: %d", w.Code)
	}
}

func TestMinFee(t *testing.T) {
	alice := newAcct(t)
	e := policyEngine(t, Config{MinFee: uv(100)}, false, alice)
	to := newAcct(t).addr
	low := alice.tx(t, testChain, 0, uv(99), MsgBankSend{Type: MsgTypeBankSend, To: to, Amount: uv(1)})
	if w := post(e.RESTHandler(), "203.0.113.5:1", nil, low); w.Code != http.StatusBadRequest {
		t.Fatalf("low fee accepted: %d", w.Code)
	}
	nofee := alice.tx(t, testChain, 0, nil, MsgBankSend{Type: MsgTypeBankSend, To: to, Amount: uv(1)})
	if w := post(e.RESTHandler(), "203.0.113.5:1", nil, nofee); w.Code != http.StatusBadRequest {
		t.Fatalf("zero fee accepted: %d", w.Code)
	}
	ok := alice.tx(t, testChain, 0, uv(100), MsgBankSend{Type: MsgTypeBankSend, To: to, Amount: uv(1)})
	if w := post(e.RESTHandler(), "203.0.113.5:1", nil, ok); w.Code != http.StatusOK {
		t.Fatalf("min fee rejected: %d %s", w.Code, w.Body)
	}
}

func TestTxRateLimit(t *testing.T) {
	e := policyEngine(t, Config{TxRatePerMin: 20}, false) // burst 10
	h := e.RESTHandler()
	got429 := -1
	for i := 0; i < 30; i++ {
		if w := post(h, "203.0.113.5:1", nil, []byte(`{}`)); w.Code == http.StatusTooManyRequests {
			got429 = i
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing Retry-After")
			}
			break
		}
	}
	if got429 != 10 {
		t.Fatalf("first 429 at request %d, want 10 (burst)", got429)
	}
	// Another public IP has its own bucket; loopback and private callers are exempt.
	if w := post(h, "198.51.100.9:1", nil, []byte(`{}`)); w.Code == http.StatusTooManyRequests {
		t.Fatal("other IP limited")
	}
	for i := 0; i < 50; i++ {
		if w := post(h, "127.0.0.1:1", nil, []byte(`{}`)); w.Code == http.StatusTooManyRequests {
			t.Fatal("loopback limited")
		}
		if w := post(h, "[fdaa:0:1::5]:1", nil, []byte(`{}`)); w.Code == http.StatusTooManyRequests {
			t.Fatal("6PN caller limited")
		}
	}
}

func TestRateLimitUsesProxyHeaderOnlyWhenConfigured(t *testing.T) {
	e := policyEngine(t, Config{TxRatePerMin: 10, ClientIPHeader: "Fly-Client-IP"}, false) // burst 5
	h := e.RESTHandler()
	hit := func(ip string) int {
		return post(h, "172.16.0.2:1", map[string]string{"Fly-Client-IP": ip}, []byte(`{}`)).Code
	}
	for i := 0; i < 5; i++ {
		hit("203.0.113.5")
	}
	if hit("203.0.113.5") != http.StatusTooManyRequests {
		t.Fatal("same client behind the proxy not limited")
	}
	if hit("203.0.113.6") == http.StatusTooManyRequests {
		t.Fatal("different client behind the proxy shares a bucket")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	l := newRateLimiter(60) // 1/s, burst 30
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 30; i++ {
		if ok, _ := l.allow("k"); !ok {
			t.Fatalf("burst token %d denied", i)
		}
	}
	if ok, wait := l.allow("k"); ok || wait <= 0 {
		t.Fatalf("empty bucket allowed (ok=%v wait=%v)", ok, wait)
	}
	now = now.Add(3 * time.Second)
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("k"); !ok {
			t.Fatalf("refilled token %d denied", i)
		}
	}
	if ok, _ := l.allow("k"); ok {
		t.Fatal("over-refill")
	}
}
