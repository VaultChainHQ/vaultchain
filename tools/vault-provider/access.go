package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/vaultchain/vaultchain/pkg/readauth"
	"github.com/vaultchain/vaultchain/x/storage"
)

// Read access policy. A reader is identified by a signed read token
// (pkg/readauth). The chain decides whether that address may read a root:
//
//  1. the manifest owner;
//  2. an AccessGrant on the root with capability read or write whose grantee is
//     the reader and which has not expired;
//  3. a grant to the grantee "*" makes the object public: anyone, including
//     requests without a token, may read it;
//  4. a provider that holds an assigned or active replica of a pending or open
//     deal for the root (so replicas can copy the bytes for repair).
//
// Positive decisions are cached for a few seconds so Range streaming does not
// hit the chain on every request; denials are never cached.

const publicGrantee = "*"

// accessCacheTTL bounds how long a revoked grant keeps working at a provider:
// positive decisions are re-checked against the chain after this long. It is a
// variable only so tests can shorten it.
var accessCacheTTL = 5 * time.Second

type decision struct {
	ok     bool
	public bool
}

type cachedDecision struct {
	d     decision
	until time.Time
}

func (s *server) chainOwner(root string) (string, error) {
	if _, err := s.chainManifest(root); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owners[root], nil
}

func (s *server) chainHeight() (int64, error) {
	var res struct {
		Height int64 `json:"height"`
	}
	code, err := s.chainGet("/cosmos/base/tendermint/v1beta1/node_info", &res)
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, fmt.Errorf("chain returned HTTP %d for node info", code)
	}
	return res.Height, nil
}

// readDecision applies the policy above for reader ("" = anonymous).
func (s *server) readDecision(root, reader string) (decision, error) {
	key := root + "|" + reader
	s.mu.Lock()
	if c, ok := s.access[key]; ok && time.Now().Before(c.until) {
		s.mu.Unlock()
		return c.d, nil
	}
	s.mu.Unlock()

	owner, err := s.chainOwner(root)
	if err != nil {
		return decision{}, err
	}
	var d decision
	var grants struct {
		Grants []struct {
			Grantee    string `json:"grantee"`
			Capability string `json:"capability"`
			ExpiryH    int64  `json:"expiry_height"`
		} `json:"grants"`
	}
	code, err := s.chainGet("/vaultchain/access/grants?cid="+url.QueryEscape(root), &grants)
	if err != nil || code != http.StatusOK {
		return decision{}, fmt.Errorf("cannot read access grants from chain (HTTP %d, %v)", code, err)
	}
	var height int64
	for _, g := range grants.Grants {
		if g.Capability != "read" && g.Capability != "write" {
			continue
		}
		if g.Grantee != publicGrantee && (reader == "" || g.Grantee != reader) {
			continue
		}
		if g.ExpiryH > 0 {
			if height == 0 {
				if height, err = s.chainHeight(); err != nil || height == 0 {
					return decision{}, fmt.Errorf("cannot determine chain height for grant expiry (%v)", err)
				}
			}
			if g.ExpiryH <= height {
				continue
			}
		}
		d.ok = true
		if g.Grantee == publicGrantee {
			d.public = true
		}
	}
	if reader != "" && !d.ok {
		if reader == owner {
			d.ok = true
		} else if d.ok, err = s.isReplica(root, reader); err != nil {
			return decision{}, err
		}
	}
	if d.ok {
		s.mu.Lock()
		s.access[key] = cachedDecision{d: d, until: time.Now().Add(accessCacheTTL)}
		s.mu.Unlock()
	}
	return d, nil
}

// isReplica reports whether addr holds an assigned or active replica of a
// pending or open deal for root.
func (s *server) isReplica(root, addr string) (bool, error) {
	var res struct {
		Deals []storage.Deal `json:"deals"`
	}
	q := url.Values{"cid": {root}, "provider": {addr}}
	code, err := s.chainGet("/vaultchain/storage/deals?"+q.Encode(), &res)
	if err != nil || code != http.StatusOK {
		return false, fmt.Errorf("cannot read deals from chain (HTTP %d, %v)", code, err)
	}
	for _, d := range res.Deals {
		if d.CID != root || (d.Status != storage.DealPending && d.Status != storage.DealOpen) {
			continue
		}
		if i, ok := d.ReplicaIndex(addr); ok {
			if st := d.Replicas[i].Status; st == storage.ReplicaAssigned || st == storage.ReplicaActive {
				return true, nil
			}
		}
	}
	return false, nil
}

// authorizeRead enforces the read policy for a request on root. On failure it
// has already written the response. On success it sets Cache-Control: public
// objects (a "*" grant) are cacheable, everything else is private.
func (s *server) authorizeRead(w http.ResponseWriter, r *http.Request, root string) bool {
	reader := ""
	if tok := readauth.FromRequest(r); tok != "" {
		var err error
		if reader, err = readauth.Verify(tok, s.addr, root, time.Now()); err != nil {
			w.Header().Set("WWW-Authenticate", readauth.Scheme+` realm="vault-provider"`)
			writeErr(w, http.StatusUnauthorized, err)
			return false
		}
	}
	d, err := s.readDecision(root, reader)
	switch {
	case errors.Is(err, errNotCommitted):
		writeErr(w, http.StatusNotFound, err)
		return false
	case err != nil:
		writeErr(w, http.StatusBadGateway, err)
		return false
	case !d.ok && reader == "":
		w.Header().Set("WWW-Authenticate", readauth.Scheme+` realm="vault-provider"`)
		writeErr(w, http.StatusUnauthorized, errors.New("this object is not public: present a signed read token"))
		return false
	case !d.ok:
		writeErr(w, http.StatusForbidden, errors.New("reader is not the owner and has no read grant for this object"))
		return false
	}
	if d.public {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	return true
}
