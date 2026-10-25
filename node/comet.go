package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	nm "github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/rpc/client/local"
	cmttypes "github.com/cometbft/cometbft/types"
)

// Consensus engines.
const (
	// ConsensusTimer is the single-node engine: one process cuts a block per
	// interval from its own mempool. No validators, no p2p. The default.
	ConsensusTimer = "timer"
	// ConsensusComet runs CometBFT in-process with ABCIApp as the application.
	ConsensusComet = "comet"
)

// CometOptions configures the CometBFT node (Consensus == ConsensusComet).
type CometOptions struct {
	Moniker string
	// P2PAddr is the p2p listen address, e.g. tcp://0.0.0.0:26656.
	P2PAddr string
	// RPCAddr is CometBFT's own RPC listen address (default tcp://127.0.0.1:26657).
	RPCAddr string
	// PersistentPeers is a comma separated list of id@host:port.
	PersistentPeers string
	// ExternalAddr is the address advertised to peers (optional).
	ExternalAddr string
	// LogLevel is a CometBFT log level (debug, info, warn, error).
	LogLevel string
}

// Files CometBFT uses, relative to the node home. They are kept apart from the
// timer engine's layout: data/state.db is our bbolt app state, cometdata/ holds
// CometBFT's block and state stores.
const (
	CometGenesisFile      = "config/cometgenesis.json"
	CometPrivValKeyFile   = "config/priv_validator_key.json"
	CometNodeKeyFile      = "config/node_key.json"
	cometDataDir          = "cometdata"
	cometPrivValStateFile = "cometdata/priv_validator_state.json"
)

// CometConfig builds the CometBFT config for home from options.
func CometConfig(home string, blockInterval time.Duration, o CometOptions) *cmtcfg.Config {
	c := cmtcfg.DefaultConfig()
	c.SetRoot(home)
	c.Genesis = CometGenesisFile
	c.PrivValidatorKey = CometPrivValKeyFile
	c.NodeKey = CometNodeKeyFile
	c.PrivValidatorState = cometPrivValStateFile
	c.DBPath = cometDataDir
	c.DBBackend = "goleveldb"
	if o.Moniker != "" {
		c.Moniker = o.Moniker
	}
	if o.P2PAddr != "" {
		c.P2P.ListenAddress = o.P2PAddr
	}
	c.RPC.ListenAddress = "tcp://127.0.0.1:26657"
	if o.RPCAddr != "" {
		c.RPC.ListenAddress = o.RPCAddr
	}
	c.P2P.PersistentPeers = o.PersistentPeers
	c.P2P.ExternalAddress = o.ExternalAddr
	// Validators live on private networks (docker bridge, Fly 6PN).
	c.P2P.AddrBookStrict = false
	c.P2P.AllowDuplicateIP = true
	c.Consensus.TimeoutCommit = blockInterval
	c.TxIndex.Indexer = "null"
	c.Instrumentation.Prometheus = false
	return c
}

func cometLogger(level string) (cmtlog.Logger, error) {
	if level == "" {
		level = "error"
	}
	opt, err := cmtlog.AllowLevel(level)
	if err != nil {
		return nil, err
	}
	return cmtlog.NewFilter(cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stderr)), opt), nil
}

// cometState is the running CometBFT node.
type cometState struct {
	node   *nm.Node
	client *local.Local
}

func (e *Engine) cometConfig() *cmtcfg.Config {
	return CometConfig(e.cfg.Home, e.cfg.BlockInterval, e.cfg.Comet)
}

// startComet starts the in-process CometBFT node.
func (e *Engine) startComet() error {
	c := e.cometConfig()
	for _, f := range []string{c.GenesisFile(), c.PrivValidatorKeyFile(), c.NodeKeyFile()} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("consensus=comet needs %s (generate a network with `vaultd testnet`): %w", f, err)
		}
	}
	if err := c.ValidateBasic(); err != nil {
		return fmt.Errorf("comet config: %w", err)
	}
	if err := os.MkdirAll(c.DBDir(), 0o700); err != nil {
		return err
	}
	var pv *privval.FilePV
	if _, err := os.Stat(c.PrivValidatorStateFile()); err == nil {
		pv = privval.LoadFilePV(c.PrivValidatorKeyFile(), c.PrivValidatorStateFile())
	} else {
		pv = privval.LoadFilePVEmptyState(c.PrivValidatorKeyFile(), c.PrivValidatorStateFile())
	}
	nk, err := p2p.LoadNodeKey(c.NodeKeyFile())
	if err != nil {
		return err
	}
	logger, err := cometLogger(e.cfg.Comet.LogLevel)
	if err != nil {
		return err
	}
	n, err := nm.NewNode(c, pv, nk, proxy.NewLocalClientCreator(e.abci),
		nm.DefaultGenesisDocProviderFunc(c), cmtcfg.DefaultDBProvider,
		nm.DefaultMetricsProvider(c.Instrumentation), logger)
	if err != nil {
		return fmt.Errorf("create comet node: %w", err)
	}
	if err := n.Start(); err != nil {
		return fmt.Errorf("start comet node: %w", err)
	}
	e.comet = &cometState{node: n, client: local.New(n)}
	return nil
}

func (e *Engine) stopComet() {
	if e.comet != nil {
		_ = e.comet.node.Stop()
		e.comet.node.Wait()
	}
}

func (e *Engine) runComet(ctx context.Context) error {
	if err := e.startComet(); err != nil {
		return err
	}
	defer e.stopComet()
	restErr := make(chan error, 1)
	go func() { restErr <- e.serveREST(ctx) }()
	id := e.comet.node.NodeInfo().ID()
	log.Printf("vaultchain node (cometbft) started chain_id=%s height=%d node_id=%s rest=%s p2p=%s rpc=%s",
		e.app.ChainID(), e.app.Height(), id, e.cfg.RESTAddr, e.cometConfig().P2P.ListenAddress, e.cometConfig().RPC.ListenAddress)
	select {
	case <-ctx.Done():
		return nil
	case err := <-restErr:
		return err
	}
}

// submitComet checks the tx through CometBFT's mempool (ABCIApp.CheckTx) and
// gossips it to the other validators.
func (e *Engine) submitComet(raw json.RawMessage) (string, error) {
	dt, err := DecodeTx(raw)
	if err != nil {
		return "", err
	}
	res, err := e.comet.client.BroadcastTxSync(context.Background(), cmttypes.Tx(raw))
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", errors.New(res.Log)
	}
	return dt.Hash, nil
}

// consensusInfo is exposed in node_info (REST).
func (e *Engine) consensusInfo() map[string]any {
	if e.comet == nil {
		return map[string]any{"engine": ConsensusTimer}
	}
	out := map[string]any{"engine": ConsensusComet}
	ctx := context.Background()
	if st, err := e.comet.client.Status(ctx); err == nil {
		out["node_id"] = string(st.NodeInfo.ID())
		out["catching_up"] = st.SyncInfo.CatchingUp
		out["latest_block_height"] = st.SyncInfo.LatestBlockHeight
		out["validator_address"] = st.ValidatorInfo.Address.String()
		out["voting_power"] = st.ValidatorInfo.VotingPower
	}
	if ni, err := e.comet.client.NetInfo(ctx); err == nil {
		out["peers"] = ni.NPeers
	}
	if vs, err := e.comet.client.Validators(ctx, nil, nil, nil); err == nil {
		out["validators"] = len(vs.Validators)
	}
	return out
}

// WriteCometGenesis writes the CometBFT genesis document for home.
func WriteCometGenesis(home string, doc *cmttypes.GenesisDoc) error {
	if err := doc.ValidateAndComplete(); err != nil {
		return err
	}
	p := filepath.Join(home, filepath.FromSlash(CometGenesisFile))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return doc.SaveAs(p)
}
