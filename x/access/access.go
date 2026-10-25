package access

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/VaultChainHQ/vaultchain/kv"
)

const (
	ModuleName          = "access"
	MsgTypeGrantAccess  = "access/GrantAccess"
	MsgTypeRevokeAccess = "access/RevokeAccess"
	CapabilityRead      = "read"
	CapabilityWrite     = "write"
)

// Public is the grantee that makes a CID readable by anyone who knows its root.
// Only a read capability may be granted publicly.
const Public = "*"

var (
	prefixGrant = []byte("access/grant/")
	keySeq      = []byte("access/seq")
)

type AccessGrant struct {
	ID         uint64 `json:"id"`
	CID        string `json:"cid"`
	Owner      string `json:"owner"`
	Grantee    string `json:"grantee"`
	Capability string `json:"capability"`
	ExpiryH    int64  `json:"expiry_height"`
	// KeyEnvelope is the file key of an encrypted object sealed to Grantee
	// (hex; see pkg/vaultcrypt). Empty for unencrypted objects.
	KeyEnvelope string `json:"key_envelope,omitempty"`
}

// MaxKeyEnvelopeHex bounds a key envelope (a sealed 32-byte key is 105 bytes = 210 hex chars).
const MaxKeyEnvelopeHex = 512

// Keeper stores access grants, one kv entry each.
type Keeper struct {
	S kv.Store
}

func NewKeeper(s kv.Store) Keeper { return Keeper{S: s} }

type MsgGrantAccess struct {
	Type       string `json:"type"`
	From       string `json:"-"`
	CID        string `json:"cid"`
	Grantee    string `json:"grantee"`
	Capability string `json:"capability"`
	ExpiryH    int64  `json:"expiry_height"`
	// KeyEnvelope carries the file key sealed to the grantee; required for
	// encrypted objects, forbidden for unencrypted ones.
	KeyEnvelope string `json:"key_envelope,omitempty"`
}

// MsgRevokeAccess withdraws grants. Set GrantID to revoke one grant, or leave
// it 0 and name a Grantee to revoke every grant of CID to that grantee
// (Public to revoke the public grant). Only the owner of the CID may revoke.
type MsgRevokeAccess struct {
	Type    string `json:"type"`
	From    string `json:"-"`
	CID     string `json:"cid"`
	Grantee string `json:"grantee,omitempty"`
	GrantID uint64 `json:"grant_id,omitempty"`
}

// OwnerResolver reports who owns a CID (e.g. the owner of its deal/manifest).
type OwnerResolver func(cid string) (owner string, ok bool)

// Grant records an access grant for an unencrypted object (see GrantChecked).
func (k Keeper) Grant(msg MsgGrantAccess, height int64, ownerOf OwnerResolver) error {
	return k.GrantChecked(msg, height, ownerOf, func(string) bool { return false })
}

// GrantChecked records an access grant. Only the owner of the CID may grant
// access to it. For an encrypted object (isEncrypted) a grant must carry the
// grantee's key envelope and cannot be public: a public grant would let
// everyone fetch ciphertext nobody can be given the key to.
func (k Keeper) GrantChecked(msg MsgGrantAccess, height int64, ownerOf OwnerResolver, isEncrypted func(cid string) bool) error {
	if msg.From == "" || msg.CID == "" || msg.Grantee == "" {
		return fmt.Errorf("invalid grant access fields")
	}
	if msg.Capability != CapabilityRead && msg.Capability != CapabilityWrite {
		return fmt.Errorf("capability must be read or write")
	}
	if msg.Grantee == Public && msg.Capability != CapabilityRead {
		return fmt.Errorf("only read may be granted publicly")
	}
	if msg.ExpiryH > 0 && msg.ExpiryH <= height {
		return fmt.Errorf("expiry must be in the future")
	}
	owner, ok := ownerOf(msg.CID)
	if !ok {
		return fmt.Errorf("cid %s has no owner (no deal/manifest)", msg.CID)
	}
	if owner != msg.From {
		return fmt.Errorf("only the owner of cid %s may grant access", msg.CID)
	}
	if err := checkEnvelope(msg.KeyEnvelope); err != nil {
		return err
	}
	if isEncrypted(msg.CID) {
		if msg.Grantee == Public {
			return fmt.Errorf("an encrypted object cannot be granted publicly")
		}
		if msg.KeyEnvelope == "" {
			return fmt.Errorf("grants on an encrypted object must carry a key_envelope for the grantee")
		}
	} else if msg.KeyEnvelope != "" {
		return fmt.Errorf("key_envelope is only valid for encrypted objects")
	}
	id := kv.GetU64(k.S, keySeq) + 1
	kv.SetU64(k.S, keySeq, id)
	g := AccessGrant{
		ID:         id,
		CID:        msg.CID,
		Owner:      msg.From,
		Grantee:    msg.Grantee,
		Capability: msg.Capability,
		ExpiryH:    msg.ExpiryH,

		KeyEnvelope: msg.KeyEnvelope,
	}
	kv.SetJSON(k.S, grantKey(id), g)
	return nil
}

func grantKey(id uint64) []byte { return append(append([]byte(nil), prefixGrant...), kv.U64(id)...) }

func checkEnvelope(env string) error {
	if len(env) > MaxKeyEnvelopeHex || len(env)%2 != 0 {
		return fmt.Errorf("key_envelope must be at most %d hex chars", MaxKeyEnvelopeHex)
	}
	if _, err := hex.DecodeString(env); err != nil {
		return fmt.Errorf("key_envelope must be hex")
	}
	return nil
}

// Revoke deletes the matching grants and returns how many it removed. It is
// an error to match nothing, so a typo cannot look like a successful revoke.
func (k Keeper) Revoke(msg MsgRevokeAccess, ownerOf OwnerResolver) (int, error) {
	if msg.From == "" || msg.CID == "" {
		return 0, fmt.Errorf("invalid revoke access fields")
	}
	if msg.GrantID == 0 && msg.Grantee == "" {
		return 0, fmt.Errorf("name a grant_id or a grantee to revoke")
	}
	owner, ok := ownerOf(msg.CID)
	if !ok {
		return 0, fmt.Errorf("cid %s has no owner (no deal/manifest)", msg.CID)
	}
	if owner != msg.From {
		return 0, fmt.Errorf("only the owner of cid %s may revoke access", msg.CID)
	}
	var doomed []AccessGrant
	for _, g := range k.ListByCID(msg.CID) {
		if msg.GrantID != 0 && g.ID != msg.GrantID {
			continue
		}
		if msg.Grantee != "" && g.Grantee != msg.Grantee {
			continue
		}
		doomed = append(doomed, g)
	}
	if len(doomed) == 0 {
		return 0, fmt.Errorf("no matching grant on cid %s", msg.CID)
	}
	for _, g := range doomed {
		k.S.Delete(grantKey(g.ID))
	}
	return len(doomed), nil
}

// Grants lists all grants in id order.
func (k Keeper) Grants() []AccessGrant {
	out := make([]AccessGrant, 0)
	kv.IterateJSON(k.S, prefixGrant, func(_ []byte, g AccessGrant) bool { out = append(out, g); return true })
	return out
}

func (k Keeper) ListByCID(cid string) []AccessGrant {
	out := make([]AccessGrant, 0)
	for _, g := range k.Grants() {
		if g.CID == cid {
			out = append(out, g)
		}
	}
	return out
}

func MarshalMsg(v any) ([]byte, error) { return json.Marshal(v) }
