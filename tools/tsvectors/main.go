// Command tsvectors emits cross-language test vectors for the TypeScript ports
// of vaultchain's wire formats (vaultchain-upload, vaultchain-explorer), and
// verifies vectors produced by those ports.
//
//	go run ./tools/tsvectors > go-vectors.json     # Go -> TypeScript
//	go run ./tools/tsvectors -verify ts-out.json   # TypeScript -> Go
//
// Everything here is deterministic except the random parts of vaultcrypt
// (stream prefix, ephemeral key, nonce); those are fixed once by capturing the
// output, which is what the TypeScript tests read.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/VaultChainHQ/vaultchain/crypto"
	"github.com/VaultChainHQ/vaultchain/node"
	"github.com/VaultChainHQ/vaultchain/pkg/chunk"
	"github.com/VaultChainHQ/vaultchain/pkg/readauth"
	"github.com/VaultChainHQ/vaultchain/pkg/vaultcrypt"
	vtypes "github.com/VaultChainHQ/vaultchain/types"
	"github.com/VaultChainHQ/vaultchain/x/access"
	"github.com/VaultChainHQ/vaultchain/x/storage"
)

// pattern returns n deterministic bytes: byte i = (i*131 + (i>>8)*17 + 7) & 0xff.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*131 + (i>>8)*17 + 7) & 0xff)
	}
	return b
}

type keyVec struct {
	Name    string `json:"name"`
	PrivHex string `json:"priv_hex"`
	PubHex  string `json:"pub_hex"`
	Address string `json:"address"`
}

type chunkVec struct {
	Size      int      `json:"size"`
	ChunkSize int      `json:"chunk_size"`
	Root      string   `json:"root"`
	TreeRoot  string   `json:"tree_root"`
	Leaves    []string `json:"leaves"`
	Proofs    []string `json:"proofs"` // per chunk: comma-separated hex, as in X-Vault-Proof
}

type txVec struct {
	Name      string          `json:"name"`
	Key       string          `json:"key"`
	ChainID   string          `json:"chain_id"`
	Nonce     uint64          `json:"nonce"`
	Fee       json.RawMessage `json:"fee"`
	Msgs      []string        `json:"msgs"` // canonical msg JSON, as Go marshals it
	SignBytes string          `json:"sign_bytes_hex"`
	Sig       string          `json:"sig_hex"`
	Raw       string          `json:"raw_tx"`
	Hash      string          `json:"hash"`
}

type tokenVec struct {
	Key      string `json:"key"`
	Provider string `json:"provider"`
	Root     string `json:"root"`
	Exp      int64  `json:"exp"`
	Token    string `json:"token"`
}

type cryptVec struct {
	PlainSize   int    `json:"plain_size"`
	SegmentSize int    `json:"segment_size"`
	KeyHex      string `json:"key_hex"`
	Cipher      string `json:"cipher_hex"`
}

type envVec struct {
	Recipient string `json:"recipient"`
	Root      string `json:"root"`
	KeyHex    string `json:"key_hex"`
	Envelope  string `json:"envelope_hex"`
}

type vectors struct {
	Note     string     `json:"note"`
	Pattern  string     `json:"pattern"`
	Keys     []keyVec   `json:"keys"`
	Chunks   []chunkVec `json:"chunks"`
	Txs      []txVec    `json:"txs"`
	Tokens   []tokenVec `json:"tokens"`
	Crypt    []cryptVec `json:"crypt"`
	Envelope []envVec   `json:"envelopes"`
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsvectors:", err)
		os.Exit(1)
	}
}

func hx(h chunk.Hash) string { return hex.EncodeToString(h[:]) }

func privOf(hexKey string) *secp256k1.PrivKey {
	b, err := hex.DecodeString(hexKey)
	must(err)
	return &secp256k1.PrivKey{Key: b}
}

func makeKeys() []keyVec {
	var out []keyVec
	for _, name := range []string{"alice", "bob", "carol"} {
		sum := sha256.Sum256([]byte("vaultchain-ts-vector-key/" + name))
		priv := &secp256k1.PrivKey{Key: sum[:]}
		pub := priv.PubKey().Bytes()
		addr, err := vcrypto.AddressFromPubKey(pub)
		must(err)
		out = append(out, keyVec{name, hex.EncodeToString(sum[:]), hex.EncodeToString(pub), addr})
	}
	return out
}

func makeChunks() []chunkVec {
	cases := [][2]int{
		{0, 16}, {1, 16}, {15, 16}, {16, 16}, {17, 16}, {32, 16}, {48, 16}, {80, 16}, {81, 16}, {16 * 7, 16}, {16*9 + 5, 16},
		{1000, 64}, {5000, 1024}, {700000, chunk.DefaultChunkSize}, {chunk.DefaultChunkSize, chunk.DefaultChunkSize},
		{chunk.DefaultChunkSize + 1, chunk.DefaultChunkSize},
	}
	var out []chunkVec
	for _, c := range cases {
		m, leaves, err := chunk.BuildBytes(pattern(c[0]), c[1], "application/octet-stream", "")
		must(err)
		v := chunkVec{Size: c[0], ChunkSize: c[1], Root: m.Root, TreeRoot: hx(chunk.TreeRoot(leaves))}
		for i := range leaves {
			v.Leaves = append(v.Leaves, hx(leaves[i]))
			p, err := chunk.Prove(leaves, i)
			must(err)
			s := ""
			for j, h := range p {
				if j > 0 {
					s += ","
				}
				s += hex.EncodeToString(h)
			}
			v.Proofs = append(v.Proofs, s)
		}
		out = append(out, v)
	}
	return out
}

func coins(amount int64) sdk.Coins { return sdk.NewCoins(sdk.NewInt64Coin(vtypes.NativeDenom, amount)) }

func makeTx(name string, k keyVec, chainID string, nonce uint64, fee sdk.Coins, msgs ...any) txVec {
	priv := privOf(k.PrivHex)
	raw, err := node.NewSignedTx(chainID, nonce, msgs, fee, k.PubHex, priv.Sign)
	must(err)
	var tx node.Tx
	must(json.Unmarshal(raw, &tx))
	sb, err := node.SignBytes(chainID, nonce, tx.Msgs, fee, k.PubHex)
	must(err)
	feeJSON, err := json.Marshal(fee)
	must(err)
	if fee.IsZero() {
		feeJSON = []byte("[]")
	}
	v := txVec{Name: name, Key: k.Name, ChainID: chainID, Nonce: nonce, Fee: feeJSON,
		SignBytes: hex.EncodeToString(sb), Sig: tx.Sig, Raw: string(raw), Hash: node.HashTx(raw)}
	for _, m := range tx.Msgs {
		v.Msgs = append(v.Msgs, string(m))
	}
	return v
}

func makeTxs(keys []keyVec) []txVec {
	alice, bob := keys[0], keys[1]
	const chain = "vault-devnet-1"
	root := hex.EncodeToString(sha256.New().Sum(nil))
	env := hex.EncodeToString(bytes.Repeat([]byte{0xab}, vaultcrypt.EnvelopeSize))
	return []txVec{
		makeTx("bank-send", alice, chain, 0, coins(1000), node.MsgBankSend{Type: node.MsgTypeBankSend, To: bob.Address, Amount: coins(123456)}),
		makeTx("bank-send-no-fee", alice, chain, 7, nil, node.MsgBankSend{Type: node.MsgTypeBankSend, To: bob.Address, Amount: coins(1)}),
		makeTx("commit-plain", alice, chain, 1, coins(1000), storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest,
			Root: root, Size: 65, ChunkSize: chunk.DefaultChunkSize, Mime: "text/plain", Name: "hello.txt"}),
		makeTx("commit-html-escapes", alice, chain, 2, coins(1000), storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest,
			Root: root, Size: 3, ChunkSize: 16, Mime: "application/octet-stream", Name: "a<b>&c \u2028\u2029 résumé ✓.txt"}),
		makeTx("commit-encrypted", alice, chain, 3, coins(1000), storage.MsgCommitManifest{Type: storage.MsgTypeCommitManifest,
			Root: root, Size: 91, ChunkSize: chunk.DefaultChunkSize, Mime: "text/plain", Name: "secret.txt",
			Encryption: storage.EncryptionV1, KeyEnvelope: env}),
		makeTx("open-deal", alice, chain, 4, coins(1000), storage.MsgOpenDeal{Type: storage.MsgTypeOpenDeal, Class: storage.ClassMarket,
			CID: root, Provider: bob.Address, Duration: 10000, Denom: vtypes.NativeDenom}),
		makeTx("grant-read", alice, chain, 5, coins(1000), access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: root,
			Grantee: bob.Address, Capability: access.CapabilityRead, ExpiryH: 12345, KeyEnvelope: env}),
		makeTx("grant-public", alice, chain, 6, coins(1000), access.MsgGrantAccess{Type: access.MsgTypeGrantAccess, CID: root,
			Grantee: access.Public, Capability: access.CapabilityRead}),
		makeTx("revoke-grantee", alice, chain, 8, coins(1000), access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: root, Grantee: bob.Address}),
		makeTx("revoke-id", alice, chain, 9, coins(1000), access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: root, GrantID: 42}),
		makeTx("multi-msg", bob, chain, 10, coins(2000),
			node.MsgBankSend{Type: node.MsgTypeBankSend, To: alice.Address, Amount: coins(5)},
			access.MsgRevokeAccess{Type: access.MsgTypeRevokeAccess, CID: root, Grantee: access.Public}),
	}
}

func makeTokens(keys []keyVec) []tokenVec {
	var out []tokenVec
	root := hex.EncodeToString(sha256.New().Sum(nil))
	for i, k := range keys {
		exp := time.Unix(1893456000+int64(i)*3600, 0)
		tok, err := readauth.Sign("vault1providerexample", root, exp, k.PubHex, privOf(k.PrivHex).Sign)
		must(err)
		out = append(out, tokenVec{k.Name, "vault1providerexample", root, exp.Unix(), tok})
	}
	return out
}

func makeCrypt() []cryptVec {
	var out []cryptVec
	cases := [][2]int{{0, 16}, {1, 16}, {15, 16}, {16, 16}, {17, 16}, {32, 16}, {33, 16}, {100, 16}, {3000, 1024}, {2048, 1024}}
	for i, c := range cases {
		sum := sha256.Sum256([]byte(fmt.Sprintf("vaultchain-ts-vector-file-key/%d", i)))
		key := vaultcrypt.Key(sum)
		var buf bytes.Buffer
		_, err := vaultcrypt.Encrypt(&buf, bytes.NewReader(pattern(c[0])), key, c[1])
		must(err)
		out = append(out, cryptVec{c[0], c[1], hex.EncodeToString(key[:]), hex.EncodeToString(buf.Bytes())})
	}
	return out
}

func makeEnvelopes(keys []keyVec) []envVec {
	var out []envVec
	root := hex.EncodeToString(sha256.New().Sum(nil))
	for i, k := range keys {
		sum := sha256.Sum256([]byte(fmt.Sprintf("vaultchain-ts-vector-env-key/%d", i)))
		pub, err := hex.DecodeString(k.PubHex)
		must(err)
		env, err := vaultcrypt.SealKey(vaultcrypt.Key(sum), pub, root, k.Address)
		must(err)
		out = append(out, envVec{k.Address, root, hex.EncodeToString(sum[:]), hex.EncodeToString(env)})
	}
	return out
}

func generate() vectors {
	keys := makeKeys()
	return vectors{
		Note:    "Generated by go run ./tools/tsvectors; do not edit.",
		Pattern: "byte i = (i*131 + (i>>8)*17 + 7) & 0xff",
		Keys:    keys, Chunks: makeChunks(), Txs: makeTxs(keys), Tokens: makeTokens(keys), Crypt: makeCrypt(), Envelope: makeEnvelopes(keys),
	}
}

// --- TypeScript -> Go -------------------------------------------------------

type tsOut struct {
	Keys []keyVec `json:"keys"` // priv hex per name, for envelope checks
	// Txs are raw signed txs built by the TypeScript code.
	Txs []string `json:"txs"`
	// Chunks are roots computed by TypeScript over pattern(size).
	Chunks []chunkVec `json:"chunks"`
	// Crypt are ciphertexts produced by TypeScript (key + size), decrypting to pattern(plain_size).
	Crypt []cryptVec `json:"crypt"`
	// Envelopes are sealed by TypeScript to Recipient (priv found in Keys).
	Envelopes []envVec `json:"envelopes"`
	// Tokens are read tokens built by TypeScript.
	Tokens []tokenVec `json:"tokens"`
}

func verifyFile(path string) int {
	raw, err := os.ReadFile(path)
	must(err)
	return verifyData(raw)
}

func verifyData(raw []byte) int {
	var in tsOut
	must(json.Unmarshal(raw, &in))
	bad := 0
	fail := func(format string, a ...any) {
		bad++
		fmt.Printf("FAIL: "+format+"\n", a...)
	}
	for i, tx := range in.Txs {
		d, err := node.DecodeTx([]byte(tx))
		if err != nil {
			fail("tx %d: %v", i, err)
		}
		_ = d
	}
	for _, c := range in.Chunks {
		m, _, err := chunk.BuildBytes(pattern(c.Size), c.ChunkSize, "application/octet-stream", "")
		if err != nil || m.Root != c.Root {
			fail("chunk root size=%d chunk=%d: ts=%s go=%s", c.Size, c.ChunkSize, c.Root, m.Root)
		}
	}
	for _, c := range in.Crypt {
		kb, _ := hex.DecodeString(c.KeyHex)
		ct, _ := hex.DecodeString(c.Cipher)
		var key vaultcrypt.Key
		copy(key[:], kb)
		var out bytes.Buffer
		if _, err := vaultcrypt.Decrypt(&out, bytes.NewReader(ct), key); err != nil || !bytes.Equal(out.Bytes(), pattern(c.PlainSize)) {
			fail("ciphertext plain=%d seg=%d: %v", c.PlainSize, c.SegmentSize, err)
		}
	}
	privs := map[string]string{}
	for _, k := range in.Keys {
		privs[k.Address] = k.PrivHex
	}
	for _, e := range in.Envelopes {
		pb, _ := hex.DecodeString(privs[e.Recipient])
		eb, _ := hex.DecodeString(e.Envelope)
		k, err := vaultcrypt.OpenKey(eb, pb, e.Root, e.Recipient)
		if err != nil || hex.EncodeToString(k[:]) != e.KeyHex {
			fail("envelope for %s: %v", e.Recipient, err)
		}
	}
	for _, t := range in.Tokens {
		addr, err := readauth.Verify(t.Token, t.Provider, t.Root, time.Unix(t.Exp, 0).Add(-time.Minute))
		if err != nil {
			fail("token %s: %v", t.Key, err)
			continue
		}
		_ = addr
	}
	fmt.Printf("verified %d txs, %d chunk roots, %d ciphertexts, %d envelopes, %d tokens: %d failures\n",
		len(in.Txs), len(in.Chunks), len(in.Crypt), len(in.Envelopes), len(in.Tokens), bad)
	if bad > 0 {
		return 1
	}
	return 0
}

func main() {
	verifyPath := flag.String("verify", "", "verify vectors produced by the TypeScript code")
	flag.Parse()
	if *verifyPath != "" {
		os.Exit(verifyFile(*verifyPath))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	must(enc.Encode(generate()))
}
