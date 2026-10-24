package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/x/storage"
)

// prover answers the chain's storage challenges for this provider's open
// deals: every few seconds it asks the chain for each deal's challenge, reads
// the challenged chunk from the local object store, and submits a signed
// MsgSubmitProof. It needs the provider's key (-home/-key); without one the
// provider only serves data and its deals will eventually fail.
type prover struct {
	s       *server
	key     string
	pubHex  string
	sign    func([]byte) ([]byte, error)
	fee     sdk.Coins
	mu      sync.Mutex
	sentFor map[uint64]int64 // deal -> epoch we last submitted for
}

func newProver(s *server, key, pubHex string, sign func([]byte) ([]byte, error), fee sdk.Coins) *prover {
	return &prover{s: s, key: key, pubHex: pubHex, sign: sign, fee: fee, sentFor: map[uint64]int64{}}
}

func (p *prover) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := p.tick(); err != nil {
			log.Printf("prover: %v", err)
		} else if n > 0 {
			log.Printf("prover: submitted %d proof(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// tick submits a proof for every open deal that has an unanswered challenge.
func (p *prover) tick() (int, error) {
	var deals struct {
		Deals []storage.Deal `json:"deals"`
	}
	if code, err := p.s.chainGet("/vaultchain/storage/deals?provider="+p.s.addr, &deals); err != nil || code != http.StatusOK {
		return 0, fmt.Errorf("list deals: code=%d err=%v", code, err)
	}
	sent := 0
	for _, d := range deals.Deals {
		if d.Status != "open" {
			continue
		}
		var res struct {
			Challenge storage.Challenge `json:"challenge"`
		}
		code, err := p.s.chainGet(fmt.Sprintf("/vaultchain/storage/challenge/%d", d.ID), &res)
		if err != nil {
			return sent, fmt.Errorf("challenge %d: %w", d.ID, err)
		}
		if code != http.StatusOK { // 409: not challengeable yet
			continue
		}
		ch := res.Challenge
		p.mu.Lock()
		done := p.sentFor[d.ID] == ch.Epoch || d.LastProvenEpoch == ch.Epoch
		p.mu.Unlock()
		if done {
			continue
		}
		msg, err := p.buildProof(ch)
		if err != nil {
			log.Printf("prover: deal %d epoch %d: %v", d.ID, ch.Epoch, err)
			continue
		}
		if err := p.submit(msg); err != nil {
			log.Printf("prover: deal %d: submit: %v", d.ID, err)
			continue
		}
		p.mu.Lock()
		p.sentFor[d.ID] = ch.Epoch
		p.mu.Unlock()
		sent++
	}
	return sent, nil
}

func (p *prover) buildProof(ch storage.Challenge) (storage.MsgSubmitProof, error) {
	m, err := p.s.store.manifest(ch.Root)
	if err != nil {
		return storage.MsgSubmitProof{}, fmt.Errorf("object %s not stored here: %w", ch.Root, err)
	}
	data, err := p.s.store.readChunk(m, int(ch.Index))
	if err != nil {
		return storage.MsgSubmitProof{}, fmt.Errorf("read chunk %d: %w", ch.Index, err)
	}
	path, err := p.s.store.proof(m, int(ch.Index))
	if err != nil {
		return storage.MsgSubmitProof{}, fmt.Errorf("proof for chunk %d: %w", ch.Index, err)
	}
	hexPath := make([]string, len(path))
	for i, h := range path {
		hexPath[i] = hex.EncodeToString(h)
	}
	return storage.MsgSubmitProof{
		Type: storage.MsgTypeSubmitProof, DealID: ch.DealID, CID: ch.Root,
		Epoch: ch.Epoch, Chunk: data, Proof: hexPath,
	}, nil
}

func (p *prover) submit(msg storage.MsgSubmitProof) error {
	var acct struct {
		Pending uint64 `json:"pending_nonce"`
	}
	if code, err := p.s.chainGet("/vaultchain/auth/accounts/"+p.s.addr, &acct); err != nil || code != http.StatusOK {
		return fmt.Errorf("account: code=%d err=%v", code, err)
	}
	raw, err := node.NewSignedTx(p.s.chainID, acct.Pending, []any{msg}, p.fee, p.pubHex, p.sign)
	if err != nil {
		return err
	}
	resp, err := p.s.client.Post(p.s.rest+"/vaultchain/txs", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}
