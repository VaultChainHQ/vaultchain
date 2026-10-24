package access

import (
	"encoding/json"
	"fmt"

	"github.com/vaultchain/vaultchain/kv"
)

const (
	ModuleName         = "access"
	MsgTypeGrantAccess = "access/GrantAccess"
	CapabilityRead     = "read"
	CapabilityWrite    = "write"
)

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
}

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
}

// OwnerResolver reports who owns a CID (e.g. the owner of its deal/manifest).
type OwnerResolver func(cid string) (owner string, ok bool)

// Grant records an access grant. Only the owner of the CID may grant access to it.
func (k Keeper) Grant(msg MsgGrantAccess, height int64, ownerOf OwnerResolver) error {
	if msg.From == "" || msg.CID == "" || msg.Grantee == "" {
		return fmt.Errorf("invalid grant access fields")
	}
	if msg.Capability != CapabilityRead && msg.Capability != CapabilityWrite {
		return fmt.Errorf("capability must be read or write")
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
	id := kv.GetU64(k.S, keySeq) + 1
	kv.SetU64(k.S, keySeq, id)
	g := AccessGrant{
		ID:         id,
		CID:        msg.CID,
		Owner:      msg.From,
		Grantee:    msg.Grantee,
		Capability: msg.Capability,
		ExpiryH:    msg.ExpiryH,
	}
	kv.SetJSON(k.S, append(append([]byte(nil), prefixGrant...), kv.U64(id)...), g)
	return nil
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
