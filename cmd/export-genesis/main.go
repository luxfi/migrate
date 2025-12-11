package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/cockroachdb/pebble"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/geth/trie"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/core/rawdb"
)

// JSONLBlock represents a block in JSONL format with state
type JSONLBlock struct {
	Number       uint64                   `json:"number"`
	Hash         string                   `json:"hash"`
	HeaderRLP    string                   `json:"header_rlp"`
	BodyRLP      string                   `json:"body_rlp"`
	ReceiptsRLP  string                   `json:"receipts_rlp"`
	StateChanges map[string]*JSONLAccount `json:"state_changes,omitempty"`
}

// JSONLAccount represents an account in JSONL format
type JSONLAccount struct {
	Balance  string            `json:"balance"`
	Nonce    uint64            `json:"nonce"`
	Code     string            `json:"code,omitempty"`
	CodeHash string            `json:"code_hash,omitempty"`
	Storage  map[string]string `json:"storage,omitempty"`
}

// pebbleDB wraps pebble.DB to implement ethdb.Database
type pebbleDB struct {
	db *pebble.DB
}

func (p *pebbleDB) Get(key []byte) ([]byte, error) {
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	return append([]byte{}, val...), nil
}

func (p *pebbleDB) Has(key []byte) (bool, error) {
	val, err := p.Get(key)
	return val != nil, err
}

func (p *pebbleDB) Put(key []byte, value []byte) error {
	return fmt.Errorf("read-only database")
}

func (p *pebbleDB) Delete(key []byte) error {
	return fmt.Errorf("read-only database")
}

func (p *pebbleDB) NewBatch() ethdb.Batch {
	return nil
}

func (p *pebbleDB) NewBatchWithSize(size int) ethdb.Batch {
	return nil
}

func (p *pebbleDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	opts := &pebble.IterOptions{}
	if len(prefix) > 0 {
		opts.LowerBound = prefix
		opts.UpperBound = func(b []byte) []byte {
			end := make([]byte, len(b))
			copy(end, b)
			for i := len(end) - 1; i >= 0; i-- {
				end[i] = end[i] + 1
				if end[i] != 0 {
					return end
				}
			}
			return nil
		}(prefix)
	}
	return &pebbleIterator{iter: p.db.NewIter(opts)}
}

func (p *pebbleDB) NewSnapshot() (ethdb.Snapshot, error) {
	return nil, fmt.Errorf("snapshots not supported")
}

func (p *pebbleDB) Stat(property string) (string, error) {
	return "", fmt.Errorf("stats not supported")
}

func (p *pebbleDB) Compact(start []byte, limit []byte) error {
	return fmt.Errorf("read-only database")
}

func (p *pebbleDB) Close() error {
	return p.db.Close()
}

// pebbleIterator wraps pebble.Iterator to implement ethdb.Iterator
type pebbleIterator struct {
	iter *pebble.Iterator
}

func (it *pebbleIterator) Next() bool {
	return it.iter.Next()
}

func (it *pebbleIterator) Error() error {
	return it.iter.Error()
}

func (it *pebbleIterator) Key() []byte {
	return it.iter.Key()
}

func (it *pebbleIterator) Value() []byte {
	return it.iter.Value()
}

func (it *pebbleIterator) Release() {
	it.iter.Close()
}

func main() {
	sourceDB := flag.String("source-db", "", "Path to source PebbleDB database")
	output := flag.String("output", "", "Output JSONL file path")
	blockNum := flag.Uint64("block", 0, "Block number to export (default: 0 = genesis)")
	flag.Parse()

	if *sourceDB == "" || *output == "" {
		log.Fatal("Usage: export-genesis --source-db=<path> --output=<file> [--block=N]")
	}

	// Open PebbleDB in read-only mode
	opts := &pebble.Options{
		ReadOnly: true,
	}
	db, err := pebble.Open(*sourceDB, opts)
	if err != nil {
		log.Fatalf("Failed to open pebble database: %v", err)
	}
	defer db.Close()

	ethDB := &pebbleDB{db: db}

	log.Printf("=== Export Genesis Configuration ===")
	log.Printf("Source DB: %s", *sourceDB)
	log.Printf("Block Number: %d", *blockNum)
	log.Printf("Output: %s", *output)

	// Read block hash
	hash := rawdb.ReadCanonicalHash(ethDB, *blockNum)
	if hash == (common.Hash{}) {
		log.Fatalf("Block %d not found", *blockNum)
	}
	log.Printf("Block Hash: %s", hash.Hex())

	// Read header
	header := rawdb.ReadHeader(ethDB, hash, *blockNum)
	if header == nil {
		log.Fatalf("Header not found for block %d", *blockNum)
	}
	log.Printf("State Root: %s", header.Root.Hex())

	// Read body
	body := rawdb.ReadBody(ethDB, hash, *blockNum)

	// Encode header to RLP
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		log.Fatalf("Failed to encode header: %v", err)
	}

	// Encode body to RLP
	var bodyRLP []byte
	if body != nil {
		bodyRLP, err = rlp.EncodeToBytes(body)
		if err != nil {
			log.Fatalf("Failed to encode body: %v", err)
		}
	}

	// Read receipts
	receipts := rawdb.ReadReceipts(ethDB, hash, *blockNum, header.Time, nil)
	var receiptsRLP []byte
	if len(receipts) > 0 {
		receiptsRLP, err = rlp.EncodeToBytes(receipts)
		if err != nil {
			log.Fatalf("Failed to encode receipts: %v", err)
		}
	}

	// Build JSONL block
	jsonlBlock := &JSONLBlock{
		Number:       *blockNum,
		Hash:         hash.Hex(),
		HeaderRLP:    common.Bytes2Hex(headerRLP),
		BodyRLP:      common.Bytes2Hex(bodyRLP),
		ReceiptsRLP:  common.Bytes2Hex(receiptsRLP),
		StateChanges: make(map[string]*JSONLAccount),
	}

	// Export state from trie
	log.Printf("\n=== Exporting State ===")
	log.Printf("Reading state trie at root: %s", header.Root.Hex())

	trieDB := trie.NewDatabase(ethDB, nil)
	stateTrie, err := trie.NewStateTrie(trie.StateTrieID(header.Root), trieDB)
	if err != nil {
		log.Fatalf("Failed to open state trie: %v", err)
	}

	// Iterate through all accounts in state
	accountCount := 0
	totalBalance := uint64(0)

	iter, err := stateTrie.NodeIterator(nil)
	if err != nil {
		log.Fatalf("Failed to create iterator: %v", err)
	}

	for iter.Next(true) {
		if iter.Leaf() {
			// Decode account
			var acc types.StateAccount
			if err := rlp.DecodeBytes(iter.LeafBlob(), &acc); err != nil {
				log.Printf("Failed to decode account: %v", err)
				continue
			}

			// Get address from hash (note: this is the hash, not the actual address)
			// We can't recover the exact address without a reverse mapping
			addrHash := common.BytesToHash(iter.LeafKey())

			// Try to use the address hash as the address (works for most cases in genesis)
			addr := common.BytesToAddress(addrHash.Bytes())

			jsonlAcc := &JSONLAccount{
				Balance:  acc.Balance.String(),
				Nonce:    acc.Nonce,
				CodeHash: acc.CodeHash.Hex(),
			}

			// Read code if exists
			if acc.CodeHash != types.EmptyCodeHash && acc.CodeHash != (common.Hash{}) {
				code := rawdb.ReadCode(ethDB, acc.CodeHash)
				if len(code) > 0 {
					jsonlAcc.Code = common.Bytes2Hex(code)
				}
			}

			// Read storage
			if acc.Root != types.EmptyRootHash {
				storageTrie, err := trie.NewStateTrie(trie.StorageTrieID(header.Root, addrHash, acc.Root), trieDB)
				if err == nil {
					storageIter, err := storageTrie.NodeIterator(nil)
					if err == nil {
						jsonlAcc.Storage = make(map[string]string)
						for storageIter.Next(true) {
							if storageIter.Leaf() {
								key := common.BytesToHash(storageIter.LeafKey())
								value := common.BytesToHash(storageIter.LeafBlob())
								jsonlAcc.Storage[key.Hex()] = value.Hex()
							}
						}
					}
				}
			}

			jsonlBlock.StateChanges[addr.Hex()] = jsonlAcc
			accountCount++

			if accountCount%100 == 0 {
				log.Printf("Exported %d accounts...", accountCount)
			}
		}
	}

	log.Printf("Total Accounts: %d", accountCount)
	log.Printf("Total Balance: %d", totalBalance)

	// Write to JSONL file
	file, err := os.Create(*output)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(jsonlBlock); err != nil {
		log.Fatalf("Failed to write JSONL: %v", err)
	}

	log.Printf("\n=== Export Complete ===")
	log.Printf("Output: %s", *output)
	log.Printf("Block: %d", *blockNum)
	log.Printf("Hash: %s", hash.Hex())
	log.Printf("Accounts: %d", len(jsonlBlock.StateChanges))
}
