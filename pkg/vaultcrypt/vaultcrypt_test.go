package vaultcrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func testKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(rand.Reader, b)
	return b
}

func encrypt(t *testing.T, plain []byte, key Key, seg int) []byte {
	t.Helper()
	var out bytes.Buffer
	n, err := Encrypt(&out, bytes.NewReader(plain), key, seg)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != out.Len() {
		t.Fatalf("Encrypt reported %d bytes, wrote %d", n, out.Len())
	}
	return out.Bytes()
}

func TestRoundtripAndSizes(t *testing.T) {
	const seg = 1024
	key := testKey(t)
	for _, n := range []int{0, 1, seg - 1, seg, seg + 1, 2 * seg, 2*seg + 5, 10 * seg} {
		plain := randBytes(n)
		ct := encrypt(t, plain, key, seg)
		if want := CiphertextSize(uint64(n), seg); uint64(len(ct)) != want {
			t.Errorf("n=%d: ciphertext %d bytes, CiphertextSize says %d", n, len(ct), want)
		}
		if n > 0 && bytes.Contains(ct, plain[:min(n, 32)]) {
			t.Errorf("n=%d: plaintext visible in ciphertext", n)
		}
		var got bytes.Buffer
		if _, err := Decrypt(&got, bytes.NewReader(ct), key); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(got.Bytes(), plain) {
			t.Fatalf("n=%d: roundtrip mismatch", n)
		}
	}
}

func TestDefaultSegmentSizeAndFreshNonces(t *testing.T) {
	key := testKey(t)
	plain := randBytes(3*DefaultSegmentSize + 7)
	a, b := encrypt(t, plain, key, 0), encrypt(t, plain, key, 0)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same file are identical (nonce prefix reused)")
	}
	if uint64(len(a)) != CiphertextSize(uint64(len(plain)), DefaultSegmentSize) {
		t.Fatal("size mismatch")
	}
}

func TestTamperingIsDetected(t *testing.T) {
	const seg = 256
	key := testKey(t)
	plain := randBytes(5*seg + 10)
	ct := encrypt(t, plain, key, seg)
	sealed := seg + 16

	dec := func(b []byte) error { _, err := Decrypt(io.Discard, bytes.NewReader(b), key); return err }
	if err := dec(ct); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{}
	// Flip one bit in the header, the first byte, the middle and the last byte of the stream.
	for _, pos := range []int{5, 9, HeaderSize, len(ct) / 2, len(ct) - 1} {
		c := append([]byte(nil), ct...)
		c[pos] ^= 0x01
		cases["bitflip@"+itoa(pos)] = c
	}
	cases["truncate last byte"] = ct[:len(ct)-1]
	cases["truncate at segment boundary"] = ct[:HeaderSize+2*sealed]
	cases["truncate to header"] = ct[:HeaderSize]
	cases["append garbage"] = append(append([]byte(nil), ct...), 1, 2, 3)
	cases["append a whole segment"] = append(append([]byte(nil), ct...), ct[HeaderSize:HeaderSize+sealed]...)
	swapped := append([]byte(nil), ct...)
	s0, s1 := HeaderSize, HeaderSize+sealed
	copy(swapped[s0:], ct[s1:s1+sealed])
	copy(swapped[s1:], ct[s0:s0+sealed])
	cases["swap segments"] = swapped
	dropped := append(append([]byte(nil), ct[:s1]...), ct[s1+sealed:]...)
	cases["drop a middle segment"] = dropped
	for name, c := range cases {
		if err := dec(c); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrNotEncrypted) {
			t.Errorf("%s: got %v, want corruption error", name, err)
		}
	}
	if err := dec(nil); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("empty input: %v", err)
	}
	if err := dec([]byte("this is plain text, not an encrypted stream at all")); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("plain input: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestWrongKeyFails(t *testing.T) {
	ct := encrypt(t, randBytes(3000), testKey(t), 512)
	if _, err := Decrypt(io.Discard, bytes.NewReader(ct), testKey(t)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestVerifiedPrefixOnlyBeforeFailure(t *testing.T) {
	// Decrypt streams verified segments; the error must still surface for a damaged tail.
	const seg = 128
	key := testKey(t)
	plain := randBytes(4 * seg)
	ct := encrypt(t, plain, key, seg)
	ct[len(ct)-1] ^= 1
	var got bytes.Buffer
	_, err := Decrypt(&got, bytes.NewReader(ct), key)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
	if !bytes.HasPrefix(plain, got.Bytes()) || got.Len() >= len(plain) {
		t.Fatalf("output must be a strict verified prefix, got %d of %d bytes", got.Len(), len(plain))
	}
}

type recipient struct {
	priv []byte
	pub  []byte
	addr string
}

func newRecipient(t *testing.T, addr string) recipient {
	t.Helper()
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return recipient{priv: k.Serialize(), pub: k.PubKey().SerializeCompressed(), addr: addr}
}

func TestEnvelopeOnlyOpensForItsRecipientAndObject(t *testing.T) {
	root := "aa11"
	alice, bob, eve := newRecipient(t, "vault1alice"), newRecipient(t, "vault1bob"), newRecipient(t, "vault1eve")
	key := testKey(t)

	envA, err := SealKey(key, alice.pub, root, alice.addr)
	if err != nil {
		t.Fatal(err)
	}
	envB, err := SealKey(key, bob.pub, root, bob.addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(envA) != EnvelopeSize || bytes.Equal(envA, envB) {
		t.Fatalf("envelope size %d / identical envelopes", len(envA))
	}
	for name, c := range map[string]struct {
		env  []byte
		r    recipient
		root string
		addr string
	}{
		"alice": {envA, alice, root, alice.addr},
		"bob":   {envB, bob, root, bob.addr},
	} {
		got, err := OpenKey(c.env, c.r.priv, c.root, c.addr)
		if err != nil || got != key {
			t.Fatalf("%s cannot open own envelope: %v", name, err)
		}
	}
	bad := map[string]func() error{
		"wrong private key": func() error { _, err := OpenKey(envA, eve.priv, root, alice.addr); return err },
		"bob opens alice's": func() error { _, err := OpenKey(envA, bob.priv, root, alice.addr); return err },
		"other object":      func() error { _, err := OpenKey(envA, alice.priv, "bb22", alice.addr); return err },
		"other address":     func() error { _, err := OpenKey(envA, alice.priv, root, "vault1eve"); return err },
		"short envelope":    func() error { _, err := OpenKey(envA[:50], alice.priv, root, alice.addr); return err },
		"flipped bit": func() error {
			c := append([]byte(nil), envA...)
			c[EnvelopeSize-1] ^= 1
			_, err := OpenKey(c, alice.priv, root, alice.addr)
			return err
		},
		"flipped ephemeral key": func() error {
			c := append([]byte(nil), envA...)
			c[10] ^= 1
			_, err := OpenKey(c, alice.priv, root, alice.addr)
			return err
		},
	}
	for name, f := range bad {
		if err := f(); !errors.Is(err, ErrEnvelope) {
			t.Errorf("%s: got %v, want ErrEnvelope", name, err)
		}
	}
	if _, err := SealKey(key, []byte{1, 2, 3}, root, "x"); err == nil {
		t.Error("sealed to a malformed public key")
	}
}

func TestGranteeDecryptsAndRevokedKeyHolderStillCan(t *testing.T) {
	// A grantee who received an envelope decrypts the file. Removing the grant
	// on chain cannot take that key back: whoever holds the key and a copy of
	// the ciphertext can still read it. This test pins that documented limit.
	root := "cc33"
	owner, bob := newRecipient(t, "vault1owner"), newRecipient(t, "vault1bob")
	key := testKey(t)
	plain := randBytes(20000)
	ct := encrypt(t, plain, key, 0)

	envBob, err := SealKey(key, bob.pub, root, bob.addr)
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := OpenKey(envBob, bob.priv, root, bob.addr)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := Decrypt(&got, bytes.NewReader(ct), bobKey); err != nil || !bytes.Equal(got.Bytes(), plain) {
		t.Fatalf("grantee could not decrypt: %v", err)
	}
	// "Revocation": the owner re-encrypts under a new key for the remaining readers only.
	newKey := testKey(t)
	ct2 := encrypt(t, plain, newKey, 0)
	if _, err := Decrypt(io.Discard, bytes.NewReader(ct2), bobKey); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("old key opened the re-encrypted copy: %v", err)
	}
	// ...but bob's old key still opens the old ciphertext if he kept a copy.
	if _, err := Decrypt(io.Discard, bytes.NewReader(ct), bobKey); err != nil {
		t.Fatal(err)
	}
	_ = owner
}
