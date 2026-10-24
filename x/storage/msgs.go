package storage

import (
	"encoding/json"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const (
	MsgTypeRegisterProvider = "storage/RegisterProvider"
	MsgTypeBuyCapacity      = "storage/BuyCapacity"
	MsgTypeOpenDeal         = "storage/OpenDeal"
	MsgTypeSubmitProof      = "storage/SubmitProof"
)

type MsgRegisterProvider struct {
	Type       string   `json:"type"`
	From       string   `json:"-"`
	Endpoint   string   `json:"endpoint"`
	Capacity   uint64   `json:"capacity"`
	Collateral sdk.Coin `json:"collateral"`
}

type MsgBuyCapacity struct {
	Type     string `json:"type"`
	From     string `json:"-"`
	Bytes    uint64 `json:"bytes"`
	Duration int64  `json:"duration_blocks"`
	Denom    string `json:"denom"`
}

type MsgOpenDeal struct {
	Type        string `json:"type"`
	From        string `json:"-"`
	Class       string `json:"class"` // market | archive
	CID         string `json:"cid"`
	Size        uint64 `json:"size"`
	Provider    string `json:"provider"`
	Duration    int64  `json:"duration_blocks"` // for archive: years*blocksPerYear approximated by caller
	Denom       string `json:"denom"`
	Replication uint32 `json:"replication"`
}

type MsgSubmitProof struct {
	Type        string `json:"type"`
	From        string `json:"-"`
	DealID      uint64 `json:"deal_id"`
	CID         string `json:"cid"`
	Attestation string `json:"attestation"`
}

func MarshalMsg(v any) ([]byte, error) { return json.Marshal(v) }

func UnmarshalTyped(raw json.RawMessage) (string, json.RawMessage, error) {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", nil, err
	}
	if envelope.Type == "" {
		return "", nil, fmt.Errorf("missing msg type")
	}
	return envelope.Type, raw, nil
}
