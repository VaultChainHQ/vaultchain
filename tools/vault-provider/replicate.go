package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vaultchain/vaultchain/pkg/chunk"
	"github.com/vaultchain/vaultchain/pkg/readauth"
)

// complete reports whether the whole object for m is stored (and readable) here.
func (s *server) complete(m chunk.Manifest) bool {
	sm, err := s.store.manifest(m.Root)
	if err != nil {
		return false
	}
	return s.store.have(sm) == sm.NumChunks()
}

// fetchObject copies an object from another provider's HTTP endpoint into the
// local store, verifying it against the chain-committed root (ingest).
func (s *server) fetchObject(endpoint, peer string, m chunk.Manifest) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/objects/"+m.Root, nil)
	if err != nil {
		return err
	}
	if err := s.signRead(req, peer, m.Root); err != nil {
		return err
	}
	cl := &http.Client{Timeout: 30 * time.Minute}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	_, _, err = s.ingest(io.LimitReader(resp.Body, int64(m.Size)+1), m.Root, m)
	return err
}

// signRead attaches a short-lived read token for (peer, root) to an outgoing
// request, signed with this provider's key. The peer authorises it because this
// provider holds an assigned or active replica of a deal for the root.
func (s *server) signRead(req *http.Request, peer, root string) error {
	if s.readSign == nil {
		return nil
	}
	tok, err := readauth.Sign(peer, root, time.Now().Add(2*time.Minute), s.readPub, s.readSign)
	if err != nil {
		return err
	}
	readauth.Attach(req, tok)
	return nil
}
