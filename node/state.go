package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bolt "go.etcd.io/bbolt"

	"github.com/vaultchain/vaultchain/x/access"
	"github.com/vaultchain/vaultchain/x/bank"
	"github.com/vaultchain/vaultchain/x/storage"
)

var (
	bucketMeta  = []byte("meta")
	bucketState = []byte("state")
	keyChain    = []byte("chain_state")
)

type GenesisAccount struct {
	Address string    `json:"address"`
	Coins   sdk.Coins `json:"coins"`
}

type Genesis struct {
	ChainID     string           `json:"chain_id"`
	GenesisTime time.Time        `json:"genesis_time"`
	Accounts    []GenesisAccount `json:"accounts"`
	Storage     storage.Params   `json:"storage_params"`
}

type BlockHeader struct {
	Height  int64     `json:"height"`
	Time    time.Time `json:"time"`
	TxCount int       `json:"tx_count"`
	AppHash string    `json:"app_hash"`
	ChainID string    `json:"chain_id"`
}

type TxRecord struct {
	Hash   string          `json:"hash"`
	Height int64           `json:"height"`
	Raw    json.RawMessage `json:"raw"`
	Result string          `json:"result"`
}

type ChainState struct {
	Height  int64          `json:"height"`
	ChainID string         `json:"chain_id"`
	Bank    *bank.State    `json:"bank"`
	Storage *storage.State `json:"storage"`
	Access  *access.State  `json:"access"`
	Blocks  []BlockHeader  `json:"blocks"`
	Txs     []TxRecord     `json:"txs"`
	// Accounts holds the next expected nonce per signer address.
	Accounts map[string]uint64 `json:"accounts"`
}

type Store struct {
	mu    sync.RWMutex
	db    *bolt.DB
	path  string
	State *ChainState
}

func OpenStore(home string) (*Store, error) {
	dataDir := filepath.Join(home, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "vaultchain.db")
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: dbPath}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketMeta); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketState)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.load(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) load() error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketState).Get(keyChain)
		if b == nil {
			s.State = &ChainState{
				Height:   0,
				Bank:     bank.NewState(),
				Storage:  storage.NewState(),
				Access:   access.NewState(),
				Blocks:   nil,
				Txs:      nil,
				Accounts: map[string]uint64{},
			}
			return nil
		}
		var st ChainState
		if err := json.Unmarshal(b, &st); err != nil {
			return err
		}
		if st.Bank == nil {
			st.Bank = bank.NewState()
		}
		if st.Storage == nil {
			st.Storage = storage.NewState()
		}
		if st.Access == nil {
			st.Access = access.NewState()
		}
		if st.Accounts == nil {
			st.Accounts = map[string]uint64{}
		}
		if st.Bank.Balances == nil {
			st.Bank.Balances = map[string]sdk.Coins{}
		}
		s.State = &st
		return nil
	})
}

func (s *Store) Persist() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, err := json.Marshal(s.State)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketState).Put(keyChain, raw)
	})
}

func LoadGenesis(home string) (*Genesis, error) {
	path := filepath.Join(home, "config", "genesis.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Genesis
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func WriteGenesis(home string, g *Genesis) error {
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "genesis.json"), b, 0o644)
}

func (s *Store) InitFromGenesis(g *Genesis) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.State.Height > 0 {
		return fmt.Errorf("state already initialized at height %d", s.State.Height)
	}
	s.State.ChainID = g.ChainID
	s.State.Bank = bank.NewState()
	s.State.Storage = storage.NewState()
	s.State.Storage.Params = g.Storage
	s.State.Access = access.NewState()
	for _, acc := range g.Accounts {
		if err := s.State.Bank.AddCoins(acc.Address, acc.Coins); err != nil {
			return err
		}
	}
	return s.PersistUnlocked()
}

func (s *Store) PersistUnlocked() error {
	raw, err := json.Marshal(s.State)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketState).Put(keyChain, raw)
	})
}
