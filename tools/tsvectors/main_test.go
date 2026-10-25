package main

import (
	"encoding/json"
	"testing"
)

// TestVectorsRoundTrip feeds the generated vectors back through the verifier
// that checks TypeScript output, so the generator and the verifier stay in sync.
func TestVectorsRoundTrip(t *testing.T) {
	v := generate()
	out := tsOut{Keys: v.Keys, Chunks: v.Chunks, Crypt: v.Crypt, Envelopes: v.Envelope, Tokens: v.Tokens}
	for _, tx := range v.Txs {
		out.Txs = append(out.Txs, tx.Raw)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if code := verifyData(raw); code != 0 {
		t.Fatalf("verify failed with code %d", code)
	}
}
