package access

import (
	"encoding/json"
	"fmt"
)

const (
	ModuleName          = "access"
	MsgTypeGrantAccess  = "access/GrantAccess"
	CapabilityRead      = "read"
	CapabilityWrite     = "write"
)

type AccessGrant struct {
	ID         uint64 `json:"id"`
	CID        string `json:"cid"`
	Owner      string `json:"owner"`
	Grantee    string `json:"grantee"`
	Capability string `json:"capability"`
	ExpiryH    int64  `json:"expiry_height"`
}

type State struct {
	Grants     map[uint64]AccessGrant `json:"grants"`
	NextGrantID uint64                `json:"next_grant_id"`
}

func NewState() *State {
	return &State{
		Grants:      make(map[uint64]AccessGrant),
		NextGrantID: 1,
	}
}

type MsgGrantAccess struct {
	Type       string `json:"type"`
	From       string `json:"from"`
	CID        string `json:"cid"`
	Grantee    string `json:"grantee"`
	Capability string `json:"capability"`
	ExpiryH    int64  `json:"expiry_height"`
}

func (s *State) Grant(msg MsgGrantAccess, height int64) error {
	if msg.From == "" || msg.CID == "" || msg.Grantee == "" {
		return fmt.Errorf("invalid grant access fields")
	}
	if msg.Capability != CapabilityRead && msg.Capability != CapabilityWrite {
		return fmt.Errorf("capability must be read or write")
	}
	if msg.ExpiryH > 0 && msg.ExpiryH <= height {
		return fmt.Errorf("expiry must be in the future")
	}
	id := s.NextGrantID
	s.NextGrantID++
	s.Grants[id] = AccessGrant{
		ID:         id,
		CID:        msg.CID,
		Owner:      msg.From,
		Grantee:    msg.Grantee,
		Capability: msg.Capability,
		ExpiryH:    msg.ExpiryH,
	}
	return nil
}

func (s *State) ListByCID(cid string) []AccessGrant {
	out := make([]AccessGrant, 0)
	for _, g := range s.Grants {
		if g.CID == cid {
			out = append(out, g)
		}
	}
	return out
}

func MarshalMsg(v any) ([]byte, error) { return json.Marshal(v) }
