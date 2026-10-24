package node

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/vaultchain/vaultchain/kv"
	vtypes "github.com/vaultchain/vaultchain/types"
	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/ibc"
	"github.com/vaultchain/vaultchain/x/storage"
)

// State layout (see kv): consensus buckets bank, storage, access, auth, ibc
// are covered by the app hash; _meta, _blocks, _txs, _txidx are bookkeeping.
var (
	keyChainID   = []byte("_meta/chain_id")
	keyHeight    = []byte("_meta/height")
	keyBlockTime = []byte("_meta/last_block_time")
	keyGenesis   = []byte("_meta/genesis_time")
	prefixBlock  = []byte("_blocks/")
	prefixTx     = []byte("_txs/")
	prefixTxIdx  = []byte("_txidx/")
)

// DefaultTxRetention is how many blocks of per-tx records (raw tx + result)
// are kept. Older records are pruned; block headers are kept.
const DefaultTxRetention = 1000

type AppConfig struct {
	// TxRetention in blocks; <= 0 means DefaultTxRetention.
	TxRetention int64
}

// BlockInput is everything block execution may depend on. Execution reads
// nothing else (no wall clock, no randomness), so replaying the same inputs
// on the same genesis reproduces the same state and app hash.
type BlockInput struct {
	Height   int64
	Time     time.Time
	Proposer string
	Txs      [][]byte
}

type BlockHeader struct {
	Height      int64     `json:"height"`
	Time        time.Time `json:"time"`
	ChainID     string    `json:"chain_id"`
	Proposer    string    `json:"proposer"`
	TxCount     int       `json:"tx_count"`
	OKCount     int       `json:"ok_count"`
	TxsHash     string    `json:"txs_hash"`
	PrevAppHash string    `json:"prev_app_hash"`
	AppHash     string    `json:"app_hash"`
}

type TxResult struct {
	Hash string `json:"hash"`
	OK   bool   `json:"ok"`
	Log  string `json:"log"`
}

type BlockResult struct {
	Header  BlockHeader
	Results []TxResult
}

type TxRecord struct {
	Hash   string `json:"hash"`
	Height int64  `json:"height"`
	Index  int    `json:"index"`
	OK     bool   `json:"ok"`
	Result string `json:"result"`
	Raw    string `json:"raw"`
}

// App is the deterministic state machine over a kv.DB.
type App struct {
	db  kv.DB
	cfg AppConfig
	mu  sync.Mutex // serialises writers (InitChain, ExecuteBlock)
}

func NewApp(db kv.DB, cfg AppConfig) *App {
	if cfg.TxRetention <= 0 {
		cfg.TxRetention = DefaultTxRetention
	}
	return &App{db: db, cfg: cfg}
}

func (a *App) DB() kv.DB { return a.db }

func normTime(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

func putTime(s kv.Store, key []byte, t time.Time) {
	s.Set(key, []byte(t.UTC().Format(time.RFC3339Nano)))
}

func getTime(r kv.Reader, key []byte) time.Time {
	b := r.Get(key)
	if b == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, string(b))
	if err != nil {
		panic(fmt.Sprintf("corrupt time at %s: %v", key, err))
	}
	return t
}

func (a *App) Initialized() bool { return a.db.Get(keyChainID) != nil }

func (a *App) ChainID() string { return string(a.db.Get(keyChainID)) }

func (a *App) Height() int64 { return int64(kv.GetU64(a.db, keyHeight)) }

func (a *App) LastBlockTime() time.Time { return getTime(a.db, keyBlockTime) }

func (a *App) AppHash() string { return hex.EncodeToString(a.db.AppHash()) }

// View runs fn against one consistent read-only snapshot of committed state.
func (a *App) View(fn func(s kv.Store) error) error {
	return a.db.View(func(r kv.Reader) error { return fn(kv.ReadOnly(r)) })
}

// InitChain writes genesis state. It refuses to run twice.
func (a *App) InitChain(g *Genesis) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Initialized() {
		return fmt.Errorf("state already initialized (chain_id %s, height %d)", a.ChainID(), a.Height())
	}
	if g.ChainID == "" {
		return fmt.Errorf("genesis: chain_id required")
	}
	c := kv.NewCache(a.db)
	bk := bank.NewKeeper(c)
	for _, acc := range g.Accounts {
		if err := vtypes.ValidateAddress(acc.Address); err != nil {
			return fmt.Errorf("genesis account %q: %w", acc.Address, err)
		}
		if err := bk.AddCoins(acc.Address, acc.Coins); err != nil {
			return fmt.Errorf("genesis account %s: %w", acc.Address, err)
		}
	}
	storage.NewKeeper(c).SetParams(g.Storage)
	ibc.NewKeeper(c).SetLabMode(g.LabMode)
	c.Set(keyChainID, []byte(g.ChainID))
	kv.SetU64(c, keyHeight, 0)
	gt := normTime(g.GenesisTime)
	putTime(c, keyGenesis, gt)
	putTime(c, keyBlockTime, gt)
	return a.db.Commit(c.Ops())
}

type execCtx struct {
	chainID  string
	height   int64
	time     time.Time
	proposer string
}

// feeRecipient: fees go to the block proposer if it is a valid account
// address, otherwise to the fee collector module account.
func (c execCtx) feeRecipient() string {
	if vtypes.ValidateAddress(c.proposer) == nil {
		return c.proposer
	}
	return bank.FeeCollector
}

func nonceKey(addr string) []byte { return kv.K("auth", "nonce", addr) }

// Nonce is the next expected nonce for addr.
func Nonce(r kv.Reader, addr string) uint64 { return kv.GetU64(r, nonceKey(addr)) }

func idxKey(height int64, i int) []byte {
	return append(append(append([]byte(nil), prefixTxIdx...), kv.U64(uint64(height))...),
		byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
}

func txKey(hash string) []byte { return append(append([]byte(nil), prefixTx...), hash...) }

func blockKey(h int64) []byte {
	return append(append([]byte(nil), prefixBlock...), kv.U64(uint64(h))...)
}

// ExecuteBlock runs a block against committed state and commits it atomically
// (one durable write containing only the keys that changed).
//
// Transaction semantics:
//   - A tx that fails stateless/ante checks (bad encoding or signature, wrong
//     chain id, bad nonce, fee unaffordable) changes nothing.
//   - Otherwise the fee is moved to the fee recipient and the signer nonce is
//     bumped; that is committed even if the messages then fail.
//   - Messages run on a cached branch of the block state; if any message
//     errors (or panics) the whole branch is discarded, so a failed tx leaves
//     no partial effects beyond fee and nonce.
func (a *App) ExecuteBlock(in BlockInput) (*BlockResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.Initialized() {
		return nil, fmt.Errorf("chain not initialized")
	}
	chainID, prevHeight := a.ChainID(), a.Height()
	if in.Height != prevHeight+1 {
		return nil, fmt.Errorf("bad block height: got %d want %d", in.Height, prevHeight+1)
	}
	in.Time = normTime(in.Time)
	if last := a.LastBlockTime(); !in.Time.After(last) {
		return nil, fmt.Errorf("block time %s must be after previous %s", in.Time.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
	}
	prevApp := a.AppHash()

	bc := kv.NewCache(a.db)
	ctx := execCtx{chainID: chainID, height: in.Height, time: in.Time, proposer: in.Proposer}
	results := make([]TxResult, 0, len(in.Txs))
	txsHasher := sha256.New()
	ok := 0
	for i, raw := range in.Txs {
		res := deliverTx(bc, ctx, raw)
		results = append(results, res)
		if res.OK {
			ok++
		}
		h, _ := hex.DecodeString(res.Hash)
		txsHasher.Write(h)
		a.recordTx(bc, in.Height, i, raw, res)
	}
	hdr := BlockHeader{
		Height:      in.Height,
		Time:        in.Time,
		ChainID:     chainID,
		Proposer:    in.Proposer,
		TxCount:     len(in.Txs),
		OKCount:     ok,
		TxsHash:     hex.EncodeToString(txsHasher.Sum(nil)),
		PrevAppHash: prevApp,
		// Bookkeeping buckets are not hashed, so the hash over the ops so far
		// is the post-block app hash.
		AppHash: hex.EncodeToString(a.db.AppHashAfter(bc.Ops())),
	}
	kv.SetJSON(bc, blockKey(in.Height), hdr)
	kv.SetU64(bc, keyHeight, uint64(in.Height))
	putTime(bc, keyBlockTime, in.Time)
	a.pruneTxs(bc, in.Height)
	if err := a.db.Commit(bc.Ops()); err != nil {
		return nil, err
	}
	return &BlockResult{Header: hdr, Results: results}, nil
}

func (a *App) recordTx(bc *kv.Cache, height int64, i int, raw []byte, res TxResult) {
	key := txKey(res.Hash)
	if !res.OK {
		// Never let a rejected replay overwrite the record of the original.
		if bc.Get(key) != nil {
			return
		}
	}
	rec := TxRecord{Hash: res.Hash, Height: height, Index: i, OK: res.OK, Result: res.Log, Raw: string(raw)}
	kv.SetJSON(bc, key, rec)
	bc.Set(idxKey(height, i), []byte(res.Hash))
}

// pruneTxs drops the tx records of the block that just fell out of the
// retention window.
func (a *App) pruneTxs(bc *kv.Cache, height int64) {
	old := height - a.cfg.TxRetention
	if old < 1 {
		return
	}
	prefix := append(append([]byte(nil), prefixTxIdx...), kv.U64(uint64(old))...)
	type ent struct{ idx, hash []byte }
	var list []ent
	bc.Iterate(prefix, func(k, v []byte) bool {
		list = append(list, ent{append([]byte(nil), k...), append([]byte(nil), v...)})
		return true
	})
	for _, e := range list {
		bc.Delete(e.idx)
		var rec TxRecord
		if kv.GetJSON(bc, txKey(string(e.hash)), &rec) && rec.Height == old {
			bc.Delete(txKey(string(e.hash)))
		}
	}
}

func deliverTx(bc *kv.Cache, ctx execCtx, raw []byte) TxResult {
	res := TxResult{Hash: HashTx(raw)}
	fail := func(format string, args ...any) TxResult {
		res.Log = fmt.Sprintf(format, args...)
		return res
	}
	dt, err := DecodeTx(raw)
	if err != nil {
		return fail("rejected: %v", err)
	}
	if dt.Tx.ChainID != ctx.chainID {
		return fail("rejected: wrong chain id: got %q want %q", dt.Tx.ChainID, ctx.chainID)
	}
	if want := Nonce(bc, dt.Signer); dt.Tx.Nonce != want {
		return fail("rejected: bad nonce: got %d want %d", dt.Tx.Nonce, want)
	}
	// Ante: fee + nonce are applied directly to the block state. Fee payment is
	// the only thing that can fail here and it fails before any write.
	if !dt.Tx.Fee.IsZero() {
		if err := bank.NewKeeper(bc).SendCoins(dt.Signer, ctx.feeRecipient(), dt.Tx.Fee); err != nil {
			return fail("rejected: fee: %v", err)
		}
	}
	kv.SetU64(bc, nonceKey(dt.Signer), dt.Tx.Nonce+1)

	// Messages: atomic on a branch.
	branch := kv.NewCache(bc)
	if err := runMsgs(branch, ctx, dt.Msgs); err != nil {
		return fail("failed (fee and nonce charged, effects reverted): %v", err)
	}
	branch.WriteTo(bc)
	res.OK = true
	res.Log = "ok"
	return res
}

func runMsgs(s kv.Store, ctx execCtx, msgs []any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	for i, m := range msgs {
		if err := execMsg(s, ctx, m); err != nil {
			return fmt.Errorf("msg %d: %w", i, err)
		}
	}
	return nil
}

func execMsg(s kv.Store, ctx execCtx, m any) error {
	bk := bank.NewKeeper(s)
	sk := storage.NewKeeper(s)
	switch msg := m.(type) {
	case MsgBankSend:
		return bk.SendCoins(msg.From, msg.To, msg.Amount)
	case storage.MsgRegisterProvider:
		return sk.RegisterProvider(msg, ctx.height)
	case storage.MsgCommitManifest:
		return sk.CommitManifest(msg, ctx.height)
	case storage.MsgBuyCapacity:
		return sk.BuyCapacity(msg, ctx.height)
	case storage.MsgOpenDeal:
		return sk.OpenDeal(msg, ctx.height)
	case storage.MsgSubmitProof:
		return sk.SubmitProof(msg, ctx.height)
	case access.MsgGrantAccess:
		return access.NewKeeper(s).Grant(msg, ctx.height, sk.OwnerOfCID)
	case ibc.MsgSimulateIBCDeposit:
		return ibc.NewKeeper(s).SimulateDeposit(msg)
	default:
		return fmt.Errorf("unhandled msg %T", m)
	}
}

// Header returns the committed header at height h.
func (a *App) Header(h int64) (BlockHeader, bool) {
	var hdr BlockHeader
	ok := kv.GetJSON(a.db, blockKey(h), &hdr)
	return hdr, ok
}

// LatestHeader is the newest block header, or a synthetic height-0 header.
func (a *App) LatestHeader() BlockHeader {
	if hdr, ok := a.Header(a.Height()); ok {
		return hdr
	}
	return BlockHeader{ChainID: a.ChainID(), Time: getTime(a.db, keyGenesis), AppHash: a.AppHash()}
}

// TxRecord looks up a (not yet pruned) tx record by hash.
func (a *App) TxRecord(hash string) (TxRecord, bool) {
	var rec TxRecord
	ok := kv.GetJSON(a.db, txKey(hash), &rec)
	return rec, ok
}

// Nonce reads the committed next nonce for addr.
func (a *App) Nonce(addr string) uint64 { return Nonce(a.db, addr) }
