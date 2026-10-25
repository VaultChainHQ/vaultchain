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
	"github.com/vaultchain/vaultchain/pkg/chunk"
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

	acceptSent map[uint64]time.Time // deal -> when we last sent an accept
}

func newProver(s *server, key, pubHex string, sign func([]byte) ([]byte, error), fee sdk.Coins) *prover {
	return &prover{s: s, key: key, pubHex: pubHex, sign: sign, fee: fee, sentFor: map[uint64]int64{}, acceptSent: map[uint64]time.Time{}}
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

// tick walks this provider's deals: it accepts assigned replicas whose object
// it already holds (fetching it from another active replica first when it is a
// replacement), and answers the current challenge of its active replicas.
func (p *prover) tick() (int, error) {
	var deals struct {
		Deals []storage.Deal `json:"deals"`
	}
	if code, err := p.s.chainGet("/vaultchain/storage/deals?provider="+p.s.addr, &deals); err != nil || code != http.StatusOK {
		return 0, fmt.Errorf("list deals: code=%d err=%v", code, err)
	}
	sent := 0
	for _, d := range deals.Deals {
		if d.Status != storage.DealPending && d.Status != storage.DealOpen {
			continue
		}
		ri, ok := d.ReplicaIndex(p.s.addr)
		if !ok {
			continue
		}
		rep := d.Replicas[ri]
		if rep.Status == storage.ReplicaAssigned {
			if p.maybeAccept(d) {
				sent++
			}
			continue
		}
		if d.Status != storage.DealOpen {
			continue
		}
		var res struct {
			Challenge storage.Challenge `json:"challenge"`
		}
		code, err := p.s.chainGet(fmt.Sprintf("/vaultchain/storage/challenge/%d?provider=%s", d.ID, p.s.addr), &res)
		if err != nil {
			return sent, fmt.Errorf("challenge %d: %w", d.ID, err)
		}
		if code != http.StatusOK { // 409: not challengeable yet
			continue
		}
		ch := res.Challenge
		p.mu.Lock()
		done := p.sentFor[d.ID] == ch.Epoch || rep.LastProvenEpoch == ch.Epoch
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

// maybeAccept sends MsgAcceptDeal for an assigned replica once the object is
// stored locally. A replacement provider (the deal already has active replicas)
// first copies the object from one of them.
func (p *prover) maybeAccept(d storage.Deal) bool {
	p.mu.Lock()
	last := p.acceptSent[d.ID]
	p.mu.Unlock()
	if time.Since(last) < 10*time.Second {
		return false
	}
	m, err := p.s.chainManifest(d.CID)
	if err != nil {
		log.Printf("prover: deal %d: manifest: %v", d.ID, err)
		return false
	}
	if !p.s.complete(m) {
		if !p.repairFetch(d, m) {
			return false // wait for the owner's upload
		}
	}
	err = p.submit(storage.MsgAcceptDeal{Type: storage.MsgTypeAcceptDeal, DealID: d.ID})
	p.mu.Lock()
	p.acceptSent[d.ID] = time.Now()
	p.mu.Unlock()
	if err != nil {
		log.Printf("prover: deal %d: accept: %v", d.ID, err)
		return false
	}
	log.Printf("prover: accepted deal %d", d.ID)
	return true
}

// repairFetch copies the object for d from another active replica.
func (p *prover) repairFetch(d storage.Deal, m chunk.Manifest) bool {
	for _, r := range d.Replicas {
		if r.Status != storage.ReplicaActive || r.Provider == p.s.addr {
			continue
		}
		var res struct {
			Provider storage.Provider `json:"provider"`
		}
		if code, err := p.s.chainGet("/vaultchain/storage/providers/"+r.Provider, &res); err != nil || code != http.StatusOK || res.Provider.Endpoint == "" {
			continue
		}
		if err := p.s.fetchObject(res.Provider.Endpoint, m); err != nil {
			log.Printf("prover: deal %d: fetch from %s: %v", d.ID, r.Provider, err)
			continue
		}
		log.Printf("prover: deal %d: fetched %s from %s", d.ID, m.Root, r.Provider)
		return true
	}
	return false
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

func (p *prover) submit(msg any) error {
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
