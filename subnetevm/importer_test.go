package subnetevm

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/migrate"
)

func TestImporterInit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-importer-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("Failed to create importer: %v", err)
	}

	if err := imp.Init(config); err != nil {
		t.Fatalf("Failed to init importer: %v", err)
	}

	// Verify initialized state
	if !imp.initialized {
		t.Error("Importer should be initialized")
	}

	// Try double init - should fail
	if err := imp.Init(config); err != migrate.ErrAlreadyInitialized {
		t.Errorf("Expected ErrAlreadyInitialized, got: %v", err)
	}

	if err := imp.Close(); err != nil {
		t.Fatalf("Failed to close importer: %v", err)
	}
}

func TestImporterImportBlock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-import-block-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("Failed to create importer: %v", err)
	}
	if err := imp.Init(config); err != nil {
		t.Fatalf("Failed to init importer: %v", err)
	}
	defer imp.Close()

	// Create genesis block
	genesisHeader := &types.Header{
		ParentHash:  common.Hash{},
		Root:        common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Number:      big.NewInt(0),
		GasLimit:    8000000,
		Time:        0,
		Difficulty:  big.NewInt(1),
		Coinbase:    common.Address{},
	}

	genesisBlock := &migrate.BlockData{
		Number:           0,
		Hash:             genesisHeader.Hash(),
		ParentHash:       common.Hash{},
		Timestamp:        0,
		StateRoot:        genesisHeader.Root,
		TransactionsRoot: types.EmptyTxsHash,
		ReceiptsRoot:     types.EmptyReceiptsHash,
		GasLimit:         8000000,
		GasUsed:          0,
		Difficulty:       big.NewInt(1),
		Coinbase:         common.Address{},
	}

	// Import genesis block
	if err := imp.ImportBlock(genesisBlock); err != nil {
		t.Fatalf("Failed to import genesis block: %v", err)
	}

	// Verify import
	if imp.lastImportedBlock != 0 {
		t.Errorf("Expected last imported block 0, got %d", imp.lastImportedBlock)
	}
	if imp.lastImportedHash != genesisBlock.Hash {
		t.Errorf("Expected last imported hash %s, got %s", genesisBlock.Hash.Hex(), imp.lastImportedHash.Hex())
	}

	// Create block 1
	block1Header := &types.Header{
		ParentHash:  genesisBlock.Hash,
		Root:        common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b422"),
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Number:      big.NewInt(1),
		GasLimit:    8000000,
		Time:        10,
		Difficulty:  big.NewInt(2),
		Coinbase:    common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	block1 := &migrate.BlockData{
		Number:           1,
		Hash:             block1Header.Hash(),
		ParentHash:       genesisBlock.Hash,
		Timestamp:        10,
		StateRoot:        block1Header.Root,
		TransactionsRoot: types.EmptyTxsHash,
		ReceiptsRoot:     types.EmptyReceiptsHash,
		GasLimit:         8000000,
		GasUsed:          0,
		Difficulty:       big.NewInt(2),
		Coinbase:         block1Header.Coinbase,
	}

	// Import block 1
	if err := imp.ImportBlock(block1); err != nil {
		t.Fatalf("Failed to import block 1: %v", err)
	}

	// Verify import
	if imp.lastImportedBlock != 1 {
		t.Errorf("Expected last imported block 1, got %d", imp.lastImportedBlock)
	}

	// Finalize import
	if err := imp.FinalizeImport(1); err != nil {
		t.Fatalf("Failed to finalize import: %v", err)
	}

	// Verify import via VerifyImport
	if err := imp.VerifyImport(0); err != nil {
		t.Errorf("Failed to verify genesis block: %v", err)
	}
	if err := imp.VerifyImport(1); err != nil {
		t.Errorf("Failed to verify block 1: %v", err)
	}
}

func TestImporterNonSequentialFails(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-nonseq-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("Failed to create importer: %v", err)
	}
	if err := imp.Init(config); err != nil {
		t.Fatalf("Failed to init importer: %v", err)
	}
	defer imp.Close()

	// Import genesis block
	genesisBlock := &migrate.BlockData{
		Number:     0,
		Hash:       common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ParentHash: common.Hash{},
		StateRoot:  common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty: big.NewInt(1),
	}
	if err := imp.ImportBlock(genesisBlock); err != nil {
		t.Fatalf("Failed to import genesis: %v", err)
	}

	// Try to import block 2 (skipping block 1) - should fail
	block2 := &migrate.BlockData{
		Number:     2,
		Hash:       common.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
		ParentHash: common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		StateRoot:  common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b423"),
		Difficulty: big.NewInt(3),
	}
	if err := imp.ImportBlock(block2); err == nil {
		t.Error("Expected error for non-sequential block import")
	}
}

func TestImporterImportState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-state-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("Failed to create importer: %v", err)
	}
	if err := imp.Init(config); err != nil {
		t.Fatalf("Failed to init importer: %v", err)
	}
	defer imp.Close()

	// Create test accounts
	accounts := []*migrate.Account{
		{
			Address: common.HexToAddress("0x1111111111111111111111111111111111111111"),
			Nonce:   5,
			Balance: big.NewInt(1000000000000000000),
			Storage: map[common.Hash]common.Hash{
				common.HexToHash("0x01"): common.HexToHash("0xff"),
			},
		},
		{
			Address: common.HexToAddress("0x2222222222222222222222222222222222222222"),
			Nonce:   0,
			Balance: big.NewInt(500000000000000000),
			Code:    []byte{0x60, 0x60, 0x60, 0x40}, // Simple bytecode
		},
	}

	// Import state
	if err := imp.ImportState(accounts, 0); err != nil {
		t.Fatalf("Failed to import state: %v", err)
	}

	t.Log("State import completed successfully")
}

func TestImporterFactoryRegistration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-factory-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	// Use factory function which should trigger init registration
	imp, err := migrate.NewSubnetEVMImporter(config)
	if err != nil {
		t.Fatalf("Factory failed to create importer: %v", err)
	}
	defer imp.Close()

	// Verify it's properly initialized
	simporter, ok := imp.(*Importer)
	if !ok {
		t.Fatal("Factory did not return *Importer")
	}
	if !simporter.initialized {
		t.Error("Factory should have auto-initialized the importer")
	}
}

func TestImporterWithTransactions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subnetevm-tx-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "testdb")

	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: dbPath,
		DatabaseType: "pebble",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("Failed to create importer: %v", err)
	}
	if err := imp.Init(config); err != nil {
		t.Fatalf("Failed to init importer: %v", err)
	}
	defer imp.Close()

	// Create genesis block
	genesisBlock := &migrate.BlockData{
		Number:     0,
		Hash:       common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
		ParentHash: common.Hash{},
		StateRoot:  common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Difficulty: big.NewInt(1),
	}
	if err := imp.ImportBlock(genesisBlock); err != nil {
		t.Fatalf("Failed to import genesis: %v", err)
	}

	// Create block with transactions
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")
	block1 := &migrate.BlockData{
		Number:     1,
		Hash:       common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"),
		ParentHash: genesisBlock.Hash,
		StateRoot:  common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b422"),
		Difficulty: big.NewInt(2),
		GasLimit:   8000000,
		GasUsed:    21000,
		Transactions: []*migrate.Transaction{
			{
				Hash:     common.HexToHash("0xaaaa000000000000000000000000000000000000000000000000000000000001"),
				Nonce:    0,
				From:     common.HexToAddress("0x1111111111111111111111111111111111111111"),
				To:       &to,
				Value:    big.NewInt(1000000000000000000),
				Gas:      21000,
				GasPrice: big.NewInt(1000000000),
				V:        big.NewInt(27),
				R:        big.NewInt(12345),
				S:        big.NewInt(67890),
			},
		},
	}

	if err := imp.ImportBlock(block1); err != nil {
		t.Fatalf("Failed to import block with transactions: %v", err)
	}

	if err := imp.FinalizeImport(1); err != nil {
		t.Fatalf("Failed to finalize: %v", err)
	}

	t.Log("Block with transactions imported successfully")
}
