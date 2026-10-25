package node

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vcrypto "github.com/VaultChainHQ/vaultchain/crypto"
	"github.com/VaultChainHQ/vaultchain/x/storage"
)

// NetworkOptions describes an N-validator network to generate.
type NetworkOptions struct {
	Validators int
	ChainID    string
	// Out receives node1..nodeN (one home each) and persistent_peers.txt.
	Out string
	// HostPattern is the peer hostname; %d is replaced by the validator number.
	HostPattern string
	P2PPort     int
	// P2PPortStep is added to P2PPort per further validator (nodes sharing a host).
	P2PPortStep int
	// Funds is the genesis balance of every validator account.
	Funds   sdk.Coins
	LabMode bool
}

// GenerateNetwork writes one home per validator: the shared CometBFT genesis
// (with the vault genesis as app_state), that node's consensus and p2p keys, a
// funded "validator" account in its keyring, and Out/persistent_peers.txt. The
// result contains private keys.
func GenerateNetwork(o NetworkOptions) (peers string, err error) {
	if o.Validators < 1 {
		return "", fmt.Errorf("need at least 1 validator")
	}
	var gvs []cmttypes.GenesisValidator
	var vals []GenesisValidator
	var accounts []GenesisAccount
	var peerList []string
	for i := 1; i <= o.Validators; i++ {
		home := filepath.Join(o.Out, fmt.Sprintf("node%d", i))
		host := o.HostPattern
		if strings.Contains(host, "%d") {
			host = fmt.Sprintf(host, i)
		}
		for _, d := range []string{"config", cometDataDir} {
			if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
				return "", err
			}
		}
		pv := privval.GenFilePV(filepath.Join(home, CometPrivValKeyFile), filepath.Join(home, cometPrivValStateFile))
		pv.Save()
		nk, err := p2p.LoadOrGenNodeKey(filepath.Join(home, CometNodeKeyFile))
		if err != nil {
			return "", err
		}
		kr, err := vcrypto.NewKeyring(home)
		if err != nil {
			return "", err
		}
		acct, err := kr.Add("validator")
		if err != nil {
			return "", err
		}
		pub := pv.Key.PubKey
		name := fmt.Sprintf("val-%d", i)
		gvs = append(gvs, cmttypes.GenesisValidator{Address: pub.Address(), PubKey: pub, Power: 10, Name: name})
		vals = append(vals, GenesisValidator{
			Name: name, PubKey: base64.StdEncoding.EncodeToString(pub.Bytes()), Power: 10, Account: acct.Address,
		})
		accounts = append(accounts, GenesisAccount{Address: acct.Address, Coins: o.Funds})
		peerList = append(peerList, fmt.Sprintf("%s@%s:%d", nk.ID(), host, o.P2PPort+(i-1)*o.P2PPortStep))
	}
	g := Genesis{
		ChainID:    o.ChainID,
		Accounts:   accounts,
		Storage:    storage.DefaultParams(),
		LabMode:    o.LabMode,
		Validators: vals,
	}
	appState, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	doc := &cmttypes.GenesisDoc{
		ChainID:         o.ChainID,
		GenesisTime:     time.Now().UTC().Truncate(time.Second),
		ConsensusParams: cmttypes.DefaultConsensusParams(),
		Validators:      gvs,
		AppState:        appState,
	}
	for i := 1; i <= o.Validators; i++ {
		if err := WriteCometGenesis(filepath.Join(o.Out, fmt.Sprintf("node%d", i)), doc); err != nil {
			return "", err
		}
	}
	peers = strings.Join(peerList, ",")
	// Each node also gets the list without itself (what its --persistent-peers wants).
	for i := 1; i <= o.Validators; i++ {
		var others []string
		for j, p := range peerList {
			if j != i-1 {
				others = append(others, p)
			}
		}
		f := filepath.Join(o.Out, fmt.Sprintf("node%d", i), "persistent_peers.txt")
		if err := os.WriteFile(f, []byte(strings.Join(others, ",")+"\n"), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(o.Out, "persistent_peers.txt"), []byte(peers+"\n"), 0o644); err != nil {
		return "", err
	}
	return peers, nil
}
