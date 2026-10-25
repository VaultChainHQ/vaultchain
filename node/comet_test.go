package node

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cmttypes "github.com/cometbft/cometbft/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/kv"
	"github.com/vaultchain/vaultchain/x/bank"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestCometConfigDefaults(t *testing.T) {
	c := CometConfig(t.TempDir(), 2*time.Second, CometOptions{Moniker: "m", PersistentPeers: "a@b:1"})
	if c.Consensus.TimeoutCommit != 2*time.Second || c.Moniker != "m" || c.P2P.PersistentPeers != "a@b:1" {
		t.Fatalf("config: %+v", c.Consensus)
	}
	if !strings.HasPrefix(c.RPC.ListenAddress, "tcp://127.0.0.1:") {
		t.Fatalf("comet RPC must default to loopback, got %s", c.RPC.ListenAddress)
	}
	if err := c.ValidateBasic(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateNetworkWritesConsistentHomes(t *testing.T) {
	out := t.TempDir()
	peers, err := GenerateNetwork(NetworkOptions{
		Validators: 3, ChainID: "vault-test-9", Out: out, HostPattern: "host-%d.internal",
		P2PPort: 26656, Funds: uv(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(peers, ",")
	if len(parts) != 3 || !strings.HasSuffix(parts[1], "@host-2.internal:26656") {
		t.Fatalf("peers %q", peers)
	}
	own, err := os.ReadFile(filepath.Join(out, "node2", "persistent_peers.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(own)); strings.Contains(got, "host-2.") || len(strings.Split(got, ",")) != 2 {
		t.Fatalf("node2 peers should list the other two only: %q", got)
	}
	var docs []string
	for i := 1; i <= 3; i++ {
		home := filepath.Join(out, fmt.Sprintf("node%d", i))
		doc, err := cmttypes.GenesisDocFromFile(filepath.Join(home, CometGenesisFile))
		if err != nil {
			t.Fatal(err)
		}
		if doc.ChainID != "vault-test-9" || len(doc.Validators) != 3 {
			t.Fatalf("genesis %+v", doc)
		}
		docs = append(docs, string(doc.AppState)+doc.GenesisTime.String())
		kr, err := vcrypto.NewKeyring(home)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := kr.Get("validator"); err != nil {
			t.Fatal(err)
		}
	}
	if docs[0] != docs[1] || docs[1] != docs[2] {
		t.Fatal("nodes got different genesis documents")
	}
	if strings.Contains(docs[0], `"lab_mode":true`) {
		t.Fatal("lab mode on by default")
	}
}

// startCometNet runs n in-process validators on loopback.
func startCometNet(t *testing.T, n int) []*Engine {
	t.Helper()
	out := t.TempDir()
	peers, err := GenerateNetwork(NetworkOptions{
		Validators: n, ChainID: "vault-test-1", Out: out, HostPattern: "127.0.0.1",
		P2PPort: 1, P2PPortStep: 1, Funds: uv(1_000_000_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	p2p := make([]int, n)
	var peerList []string
	for i, p := range strings.Split(peers, ",") {
		p2p[i] = freePort(t)
		peerList = append(peerList, p[:strings.LastIndex(p, ":")+1]+fmt.Sprint(p2p[i]))
	}
	var engines []*Engine
	for i := 0; i < n; i++ {
		var others []string
		for j, p := range peerList {
			if j != i {
				others = append(others, p)
			}
		}
		e, err := NewEngine(Config{
			Home: filepath.Join(out, fmt.Sprintf("node%d", i+1)), BlockInterval: 300 * time.Millisecond,
			RESTAddr: fmt.Sprintf("127.0.0.1:%d", freePort(t)), Consensus: ConsensusComet,
			Comet: CometOptions{
				Moniker: fmt.Sprintf("n%d", i+1), P2PAddr: fmt.Sprintf("tcp://127.0.0.1:%d", p2p[i]),
				RPCAddr: fmt.Sprintf("tcp://127.0.0.1:%d", freePort(t)), PersistentPeers: strings.Join(others, ","),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = e.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done; _ = e.Close() })
		engines = append(engines, e)
	}
	return engines
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// Three real CometBFT validators in one process: blocks are agreed, a tx
// submitted to one node is committed on all of them, and app hashes match.
func TestCometThreeValidatorsAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("starts three consensus nodes")
	}
	engines := startCometNet(t, 3)
	waitFor(t, "height 3 on every node", 60*time.Second, func() bool {
		for _, e := range engines {
			if e.App() == nil || e.App().Height() < 3 {
				return false
			}
		}
		return true
	})
	home := engines[0].cfg.Home
	kr, err := vcrypto.NewKeyring(home)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := kr.Get("validator")
	bob := newAcct(t)
	raw, err := NewSignedTx("vault-test-1", 0, []any{MsgBankSend{Type: MsgTypeBankSend, To: bob.addr, Amount: uv(777)}},
		uv(1000), rec.PubKeyHex, func(b []byte) ([]byte, error) { return kr.Sign("validator", b) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engines[0].SubmitTx(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := engines[0].SubmitTx(raw); err == nil {
		t.Fatal("duplicate submit accepted")
	}
	balance := func(e *Engine) int64 {
		var v int64
		_ = e.App().View(func(s kv.Store) error {
			v = bank.NewKeeper(s).GetBalance(bob.addr).AmountOf("uvault").Int64()
			return nil
		})
		return v
	}
	waitFor(t, "tx on every node", 60*time.Second, func() bool {
		for _, e := range engines {
			if balance(e) != 777 {
				return false
			}
		}
		return true
	})
	// Heights advance independently of reads, so wait for an instant at which
	// all three agree on both height and hash.
	waitFor(t, "equal height and app hash", 30*time.Second, func() bool {
		h0, a0 := engines[0].App().Height(), engines[0].App().AppHash()
		for _, e := range engines[1:] {
			if e.App().Height() != h0 || e.App().AppHash() != a0 {
				return false
			}
		}
		return true
	})
}
