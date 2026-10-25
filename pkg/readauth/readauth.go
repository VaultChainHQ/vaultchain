// Package readauth implements the short-lived signed read token a client (or
// another provider) presents to a vault-provider to download an object.
//
// A token is "<pubkey_hex>.<expiry_unix>.<sig_hex>". The signature covers the
// audience (the provider's address), the object root and the expiry, so a token
// cannot be replayed against another provider or for another object, and it
// stops working after at most MaxTTL. The reader's identity is the address
// derived from the embedded public key; the provider then asks the chain whether
// that address may read the root.
package readauth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	vcrypto "github.com/vaultchain/vaultchain/crypto"
)

const (
	// Scheme is the Authorization scheme: "Authorization: Vault <token>".
	Scheme = "Vault"
	// QueryParam carries the token in a URL for plain browser links.
	QueryParam = "vault_token"
	// MaxTTL is the longest lifetime a provider accepts for a token.
	MaxTTL = time.Hour

	domain = "vaultchain/read/v1"
)

func message(provider, root string, exp int64) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%d", domain, provider, root, exp))
}

// Sign creates a token for reading root from the provider with the given
// address, valid until expiry. sign signs with the key behind pubHex.
func Sign(provider, root string, expiry time.Time, pubHex string, sign func([]byte) ([]byte, error)) (string, error) {
	exp := expiry.Unix()
	sig, err := sign(message(provider, root, exp))
	if err != nil {
		return "", err
	}
	return pubHex + "." + strconv.FormatInt(exp, 10) + "." + hex.EncodeToString(sig), nil
}

// Verify checks a token for (provider, root) at time now and returns the
// reader's address.
func Verify(token, provider, root string, now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed read token")
	}
	pub, err := hex.DecodeString(parts[0])
	if err != nil {
		return "", errors.New("malformed read token key")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("malformed read token expiry")
	}
	sig, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("malformed read token signature")
	}
	expiry := time.Unix(exp, 0)
	if !now.Before(expiry) {
		return "", errors.New("read token expired")
	}
	if expiry.After(now.Add(MaxTTL)) {
		return "", fmt.Errorf("read token lives longer than %s", MaxTTL)
	}
	if !vcrypto.VerifySignature(pub, message(provider, root, exp), sig) {
		return "", errors.New("read token signature invalid for this provider and object")
	}
	return vcrypto.AddressFromPubKey(pub)
}

// FromRequest extracts a token from "Authorization: Vault <token>" or the
// vault_token query parameter. It returns "" if there is none.
func FromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, Scheme+" "); ok {
			return strings.TrimSpace(tok)
		}
	}
	return r.URL.Query().Get(QueryParam)
}

// Attach sets the Authorization header on an outgoing request.
func Attach(req *http.Request, token string) {
	req.Header.Set("Authorization", Scheme+" "+token)
}
