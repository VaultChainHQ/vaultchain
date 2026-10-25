// Package client is a small typed client for a vaultchain node's REST API,
// shared by the vaultd CLI helpers and the vault-ui tool: sign and submit
// transactions, wait for inclusion, and read manifests, grants and public keys.
package client

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
	"github.com/vaultchain/vaultchain/node"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/storage"
)

// Client talks to one node's REST endpoint.
type Client struct {
	REST       string // e.g. https://vaultl1-devnet.fly.dev
	HTTP       *http.Client
	AdminToken string // optional, for admin-gated messages
}

// New returns a client for a REST base URL.
func New(rest string) *Client {
	return &Client{REST: strings.TrimRight(rest, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// HTTPError is a non-2xx response.
type HTTPError struct {
	Code int
	URL  string
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d: %s", e.URL, e.Code, strings.TrimSpace(e.Body))
}

// IsNotFound reports whether err is an HTTP 404 from the node.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Code == http.StatusNotFound
}

// GetJSON GETs path (relative to the REST base) into out.
func (c *Client) GetJSON(path string, out any) error {
	u := c.REST + path
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &HTTPError{Code: resp.StatusCode, URL: u, Body: string(b)}
	}
	return json.Unmarshal(b, out)
}

// ChainID asks the node for its chain id.
func (c *Client) ChainID() (string, error) {
	var info struct {
		Default struct {
			Network string `json:"network"`
		} `json:"default_node_info"`
	}
	if err := c.GetJSON("/cosmos/base/tendermint/v1beta1/node_info", &info); err != nil {
		return "", err
	}
	return info.Default.Network, nil
}

// Signer signs transactions for one account.
type Signer struct {
	Address string
	PubKey  string // hex, 33 bytes
	Sign    func([]byte) ([]byte, error)
}

// KeyringSigner builds a Signer for a key in the test keyring.
func KeyringSigner(kr *vcrypto.Keyring, name string) (Signer, vcrypto.KeyRecord, error) {
	rec, err := kr.Get(name)
	if err != nil {
		return Signer{}, rec, err
	}
	return Signer{Address: rec.Address, PubKey: rec.PubKeyHex, Sign: func(b []byte) ([]byte, error) { return kr.Sign(name, b) }}, rec, nil
}

// Send signs msgs and submits them as one transaction, returning its hash.
func (c *Client) Send(chainID string, s Signer, fee sdk.Coins, msgs ...any) (string, error) {
	var acct struct {
		Pending uint64 `json:"pending_nonce"`
	}
	if err := c.GetJSON("/vaultchain/auth/accounts/"+s.Address, &acct); err != nil {
		return "", err
	}
	if chainID == "" {
		var err error
		if chainID, err = c.ChainID(); err != nil {
			return "", err
		}
	}
	if fee.IsZero() {
		fee = nil
	}
	raw, err := node.NewSignedTx(chainID, acct.Pending, msgs, fee, s.PubKey, s.Sign)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.REST+"/vaultchain/txs", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.AdminToken != "" {
		req.Header.Set(node.AdminTokenHeader, c.AdminToken)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("submit tx: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Hash == "" {
		return "", fmt.Errorf("submit tx: unexpected response %q", b)
	}
	return out.Hash, nil
}

// WaitTx polls until the tx is included (nil if it succeeded) or timeout passes.
func (c *Client) WaitTx(hash string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var rec struct {
			Tx struct {
				OK     bool   `json:"ok"`
				Result string `json:"result"`
			} `json:"tx"`
		}
		err := c.GetJSON("/vaultchain/txs/"+hash, &rec)
		if err == nil {
			if !rec.Tx.OK {
				return fmt.Errorf("tx %s failed: %s", hash, rec.Tx.Result)
			}
			return nil
		}
		if !IsNotFound(err) {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("tx %s not included within %s", hash, timeout)
}

// SendWait is Send followed by WaitTx.
func (c *Client) SendWait(chainID string, s Signer, fee sdk.Coins, msgs ...any) (string, error) {
	h, err := c.Send(chainID, s, fee, msgs...)
	if err != nil {
		return "", err
	}
	return h, c.WaitTx(h, 30*time.Second)
}

// Manifest reads a committed manifest (IsNotFound if the root is unknown).
func (c *Client) Manifest(root string) (storage.ManifestRecord, error) {
	var r struct {
		Manifest storage.ManifestRecord `json:"manifest"`
	}
	err := c.GetJSON("/vaultchain/storage/manifests/"+url.PathEscape(root), &r)
	return r.Manifest, err
}

// ManifestsOf lists the manifests committed by owner.
func (c *Client) ManifestsOf(owner string) ([]storage.ManifestRecord, error) {
	var r struct {
		Manifests []storage.ManifestRecord `json:"manifests"`
	}
	err := c.GetJSON("/vaultchain/storage/manifests?owner="+url.QueryEscape(owner), &r)
	return r.Manifests, err
}

// Grants lists the grants on cid ("" lists all).
func (c *Client) Grants(cid string) ([]access.AccessGrant, error) {
	var r struct {
		Grants []access.AccessGrant `json:"grants"`
	}
	err := c.GetJSON("/vaultchain/access/grants?cid="+url.QueryEscape(cid), &r)
	return r.Grants, err
}

// PubKey returns the registered public key of addr, checking that it really
// hashes to addr. It fails with a not-found error until addr has sent a tx.
func (c *Client) PubKey(addr string) ([]byte, error) {
	var r struct {
		PubKey string `json:"pubkey"`
	}
	if err := c.GetJSON("/vaultchain/auth/pubkeys/"+url.PathEscape(addr), &r); err != nil {
		return nil, err
	}
	return CheckPubKey(addr, r.PubKey)
}

// CheckPubKey decodes a hex public key and verifies it belongs to addr.
func CheckPubKey(addr, pubHex string) ([]byte, error) {
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != vcrypto.PubKeyLen {
		return nil, fmt.Errorf("public key must be %d bytes of hex", vcrypto.PubKeyLen)
	}
	got, err := vcrypto.AddressFromPubKey(pub)
	if err != nil {
		return nil, err
	}
	if got != addr {
		return nil, fmt.Errorf("public key belongs to %s, not %s", got, addr)
	}
	return pub, nil
}

// Deals lists the deals for cid.
func (c *Client) Deals(cid string) ([]storage.Deal, error) {
	var r struct {
		Deals []storage.Deal `json:"deals"`
	}
	err := c.GetJSON("/vaultchain/storage/deals?cid="+url.QueryEscape(cid), &r)
	return r.Deals, err
}

// Height returns the node's latest block height.
func (c *Client) Height() (int64, error) {
	var info struct {
		Height json.Number `json:"height"`
	}
	if err := c.GetJSON("/cosmos/base/tendermint/v1beta1/node_info", &info); err != nil {
		return 0, err
	}
	return info.Height.Int64()
}

// Balance returns addr's balance of the given denom (0 if none).
func (c *Client) Balance(addr, denom string) (int64, error) {
	var r struct {
		Balances []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"balances"`
	}
	if err := c.GetJSON("/cosmos/bank/v1beta1/balances/"+url.PathEscape(addr), &r); err != nil {
		return 0, err
	}
	for _, b := range r.Balances {
		if b.Denom == denom {
			return json.Number(b.Amount).Int64()
		}
	}
	return 0, nil
}
