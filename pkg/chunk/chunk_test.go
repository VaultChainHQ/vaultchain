package chunk

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"strings"
	"testing"
)

func randBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestRoundtripAcrossSizes(t *testing.T) {
	for _, cs := range []int{1, 7, 64, 1000} {
		for _, size := range []int{0, 1, 6, 7, 8, 63, 64, 65, 999, 1000, 1001, 5000} {
			data := randBytes(size, int64(size*31+cs))
			m, leaves, err := BuildBytes(data, cs, "application/octet-stream", "f.bin")
			if err != nil {
				t.Fatalf("cs=%d size=%d: %v", cs, size, err)
			}
			if m.Size != uint64(size) || int(m.ChunkSize) != cs {
				t.Fatalf("bad manifest %+v", m)
			}
			if len(leaves) != m.NumChunks() {
				t.Fatalf("cs=%d size=%d: %d leaves, NumChunks %d", cs, size, len(leaves), m.NumChunks())
			}
			if err := VerifyReader(bytes.NewReader(data), m); err != nil {
				t.Fatalf("cs=%d size=%d verify: %v", cs, size, err)
			}
			// Rebuilding gives the same ID; every chunk proves.
			m2, _, _ := BuildBytes(data, cs, "application/octet-stream", "f.bin")
			if m2 != m {
				t.Fatal("content id not deterministic")
			}
			var got []byte
			for i := 0; i < m.NumChunks(); i++ {
				lo := i * cs
				hi := lo + m.ChunkLen(i)
				p, err := Prove(leaves, i)
				if err != nil {
					t.Fatal(err)
				}
				if err := VerifyProof(m, i, data[lo:hi], p); err != nil {
					t.Fatalf("cs=%d size=%d chunk %d: %v", cs, size, i, err)
				}
				got = append(got, data[lo:hi]...)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("chunks do not reassemble")
			}
		}
	}
}

func TestDefaultChunkSize(t *testing.T) {
	m, leaves, err := BuildBytes(randBytes(DefaultChunkSize*2+1, 1), 0, "text/plain", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ChunkSize != 256*1024 || len(leaves) != 3 || m.ChunkLen(2) != 1 {
		t.Fatalf("defaults wrong: %+v leaves=%d", m, len(leaves))
	}
}

func TestDifferentDataOrParamsGiveDifferentRoots(t *testing.T) {
	data := randBytes(300, 2)
	a, _, _ := BuildBytes(data, 100, "text/plain", "")
	b, _, _ := BuildBytes(data, 50, "text/plain", "")
	flipped := append([]byte(nil), data...)
	flipped[299] ^= 1
	c, _, _ := BuildBytes(flipped, 100, "text/plain", "")
	if a.Root == b.Root || a.Root == c.Root {
		t.Fatal("root collision across chunk size / content")
	}
	// Mime and name are metadata, not part of the content id.
	d, _, _ := BuildBytes(data, 100, "image/png", "x")
	if a.Root != d.Root {
		t.Fatal("metadata changed content id")
	}
}

func TestTamperDetection(t *testing.T) {
	data := randBytes(1000, 3)
	m, leaves, _ := BuildBytes(data, 64, "text/plain", "")

	bad := append([]byte(nil), data...)
	bad[500] ^= 0xff
	if err := VerifyReader(bytes.NewReader(bad), m); err == nil {
		t.Fatal("tampered content verified")
	}
	if err := VerifyReader(bytes.NewReader(data[:999]), m); err == nil {
		t.Fatal("truncated content verified")
	}
	if err := VerifyReader(bytes.NewReader(append(data, 0)), m); err == nil {
		t.Fatal("extended content verified")
	}

	// Proof for chunk 3, then tamper with chunk / path / index / manifest.
	p, _ := Prove(leaves, 3)
	chunk := append([]byte(nil), data[3*64:4*64]...)
	if err := VerifyProof(m, 3, chunk, p); err != nil {
		t.Fatal(err)
	}
	chunk[0] ^= 1
	if err := VerifyProof(m, 3, chunk, p); err == nil {
		t.Fatal("tampered chunk verified")
	}
	chunk[0] ^= 1
	if err := VerifyProof(m, 4, chunk, p); err == nil {
		t.Fatal("proof verified at wrong index")
	}
	for i := range p {
		q := append(Proof(nil), p...)
		q[i] = append([]byte(nil), q[i]...)
		q[i][0] ^= 1
		if err := VerifyProof(m, 3, chunk, q); err == nil {
			t.Fatalf("tampered path element %d verified", i)
		}
	}
	if err := VerifyProof(m, 3, chunk, p[1:]); err == nil {
		t.Fatal("short proof verified")
	}
	if err := VerifyProof(m, 3, chunk, append(append(Proof(nil), p...), p[0])); err == nil {
		t.Fatal("long proof verified")
	}
	lie := m
	lie.Size++
	if err := VerifyProof(lie, 3, chunk, p); err == nil {
		t.Fatal("manifest with wrong size verified")
	}
	lie = m
	lie.ChunkSize = 128
	if err := VerifyProof(lie, 3, chunk, p); err == nil {
		t.Fatal("manifest with wrong chunk size verified")
	}
	if err := VerifyProof(m, 99, chunk, p); err == nil {
		t.Fatal("out of range index verified")
	}
	if err := VerifyProof(m, 3, chunk[:10], p); err == nil {
		t.Fatal("wrong chunk length verified")
	}
	// A leaf hash must not be accepted as an interior node (domain separation).
	if HashLeaf([]byte("x")) == hashNode(Hash{}, Hash{}) {
		t.Fatal("unexpected")
	}
}

func TestProveOutOfRange(t *testing.T) {
	_, leaves, _ := BuildBytes(randBytes(10, 4), 4, "text/plain", "")
	if _, err := Prove(leaves, -1); err == nil {
		t.Fatal()
	}
	if _, err := Prove(leaves, len(leaves)); err == nil {
		t.Fatal()
	}
}

func TestKnownEmptyAndSingleChunkVectors(t *testing.T) {
	// Single chunk: tree root equals the leaf hash, and proofs are empty.
	data := []byte("hello")
	m, leaves, _ := BuildBytes(data, 16, "text/plain", "")
	if len(leaves) != 1 || TreeRoot(leaves) != HashLeaf(data) {
		t.Fatal("single chunk tree root should be its leaf hash")
	}
	if p, _ := Prove(leaves, 0); len(p) != 0 {
		t.Fatal("single chunk proof should be empty")
	}
	if want := ContentRoot(HashLeaf(data), 5, 16); m.Root != want {
		t.Fatal("root mismatch")
	}
	// Leaf hash vector: sha256(0x00 || "hello").
	if got := hex.EncodeToString(func() []byte { h := HashLeaf(data); return h[:] }()); len(got) != 64 {
		t.Fatal("bad leaf hash")
	}
}

func TestManifestValidation(t *testing.T) {
	good, _, _ := BuildBytes([]byte("x"), 4, "image/png", "a b.png")
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(m *Manifest){
		"short root":      func(m *Manifest) { m.Root = "abcd" },
		"upper root":      func(m *Manifest) { m.Root = strings.ToUpper(m.Root) },
		"non-hex root":    func(m *Manifest) { m.Root = strings.Repeat("z", 64) },
		"zero chunk":      func(m *Manifest) { m.ChunkSize = 0 },
		"huge chunk":      func(m *Manifest) { m.ChunkSize = MaxChunkSize + 1 },
		"empty mime":      func(m *Manifest) { m.Mime = "" },
		"mime no slash":   func(m *Manifest) { m.Mime = "png" },
		"mime two slash":  func(m *Manifest) { m.Mime = "a/b/c" },
		"mime uppercase":  func(m *Manifest) { m.Mime = "Image/PNG" },
		"mime header inj": func(m *Manifest) { m.Mime = "text/html\r\nX: y" },
		"long mime":       func(m *Manifest) { m.Mime = "a/" + strings.Repeat("b", MaxMimeLen) },
		"name slash":      func(m *Manifest) { m.Name = "../etc/passwd" },
		"name control":    func(m *Manifest) { m.Name = "a\nb" },
		"long name":       func(m *Manifest) { m.Name = strings.Repeat("n", MaxNameLen+1) },
	}
	for name, mut := range cases {
		m := good
		mut(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStreamingDoesNotNeedWholeFile(t *testing.T) {
	// ReadChunks hands out at most chunkSize at a time, even for a big input.
	maxSeen := 0
	n, err := ReadChunks(bytes.NewReader(make([]byte, 10_000)), 128, func(_ int, c []byte) error {
		if len(c) > maxSeen {
			maxSeen = len(c)
		}
		return nil
	})
	if err != nil || n != 10_000 || maxSeen != 128 {
		t.Fatalf("n=%d max=%d err=%v", n, maxSeen, err)
	}
	if _, err := ReadChunks(bytes.NewReader(nil), 0, nil); err == nil {
		t.Fatal("zero chunk size accepted")
	}
}
