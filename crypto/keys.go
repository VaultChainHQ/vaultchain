package crypto

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/go-bip39"
)

type KeyRecord struct {
	Name       string `json:"name"`
	Address    string `json:"address"`
	PubKeyHex  string `json:"pubkey_hex"`
	PrivKeyHex string `json:"privkey_hex"`
	Mnemonic   string `json:"mnemonic,omitempty"`
}

type Keyring struct {
	dir  string
	keys map[string]KeyRecord
}

func NewKeyring(home string) (*Keyring, error) {
	dir := filepath.Join(home, "keyring-test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	kr := &Keyring{dir: dir, keys: make(map[string]KeyRecord)}
	if err := kr.load(); err != nil {
		return nil, err
	}
	return kr, nil
}

func (k *Keyring) path() string { return filepath.Join(k.dir, "keys.json") }

func (k *Keyring) load() error {
	b, err := os.ReadFile(k.path())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []KeyRecord
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, rec := range list {
		k.keys[rec.Name] = rec
	}
	return nil
}

func (k *Keyring) save() error {
	list := make([]KeyRecord, 0, len(k.keys))
	for _, rec := range k.keys {
		list = append(list, rec)
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(k.path(), b, 0o600)
}

func (k *Keyring) Add(name string) (KeyRecord, error) {
	if _, exists := k.keys[name]; exists {
		return KeyRecord{}, fmt.Errorf("key %q already exists", name)
	}
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return KeyRecord{}, err
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return KeyRecord{}, err
	}
	seed := bip39.NewSeed(mnemonic, "")
	priv := secp256k1.GenPrivKeyFromSecret(seed[:32])
	addr, err := AddressFromPubKey(priv.PubKey().Bytes())
	if err != nil {
		return KeyRecord{}, err
	}
	rec := KeyRecord{
		Name:       name,
		Address:    addr,
		PubKeyHex:  hex.EncodeToString(priv.PubKey().Bytes()),
		PrivKeyHex: hex.EncodeToString(priv.Bytes()),
		Mnemonic:   mnemonic,
	}
	k.keys[name] = rec
	if err := k.save(); err != nil {
		return KeyRecord{}, err
	}
	return rec, nil
}

func (k *Keyring) Get(name string) (KeyRecord, error) {
	rec, ok := k.keys[name]
	if !ok {
		return KeyRecord{}, fmt.Errorf("key %q not found", name)
	}
	return rec, nil
}

func (k *Keyring) List() []KeyRecord {
	out := make([]KeyRecord, 0, len(k.keys))
	for _, rec := range k.keys {
		out = append(out, rec)
	}
	return out
}

func (k *Keyring) Sign(name string, msg []byte) ([]byte, error) {
	rec, err := k.Get(name)
	if err != nil {
		return nil, err
	}
	privBytes, err := hex.DecodeString(rec.PrivKeyHex)
	if err != nil {
		return nil, err
	}
	priv := &secp256k1.PrivKey{Key: privBytes}
	return priv.Sign(msg)
}
