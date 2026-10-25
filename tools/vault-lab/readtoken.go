package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const readTokenTTL = 10 * time.Minute

type cachedToken struct {
	tok     string
	expires time.Time
}

var (
	readTokMu  sync.Mutex
	readTokens = map[string]cachedToken{}
)

// readToken returns a read token for root signed by the lab's key (--key) and
// addressed to the lab's provider, reusing one until it is about to expire.
func (l *Lab) readToken(root string) (string, error) {
	readTokMu.Lock()
	defer readTokMu.Unlock()
	if c, ok := readTokens[root]; ok && time.Until(c.expires) > time.Minute {
		return c.tok, nil
	}
	paddr, err := l.keyAddr(l.ProviderKey)
	if err != nil {
		return "", fmt.Errorf("provider key: %w", err)
	}
	out, err := l.execVaultd("read-token", root, "--provider", paddr, "--from", l.Key, "--ttl", readTokenTTL.String())
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(out)
	readTokens[root] = cachedToken{tok: tok, expires: time.Now().Add(readTokenTTL)}
	return tok, nil
}
