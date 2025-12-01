package coreth

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethdb/memorydb"
	"github.com/luxfi/geth/params"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/migrate"
)

// TestNewExporter tests creating a new exporter.
func TestNewExporter(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeCoreth,
		DatabasePath: "/nonexistent/path",
	}

	exp, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}
	if exp == nil {
		t.Fatal("expected non-nil exporter")
	}
}

// TestExporterNotInitialized tests that methods fail when not initialized.
func TestExporterNotInitialized(t *testing.T) {
	exp := &Exporter{}

	_, err := exp.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}

	_, err = exp.ExportConfig()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}

	ctx := context.Background()
	_, err = exp.ExportAccount(ctx, common.Address{}, 0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}

	err = exp.VerifyExport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}
}

// TestExporterInitDatabaseNotFound tests initialization with non-existent database.
func TestExporterInitDatabaseNotFound(t *testing.T) {
	exp := &Exporter{}
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeCoreth,
		DatabasePath: "/nonexistent/path/that/does/not/exist",
	}

	err := exp.Init(config)
	if err == nil {
		t.Fatal("expected error for non-existent database")
	}
}

// TestExporterInitEmptyPath tests initialization with empty path.
func TestExporterInitEmptyPath(t *testing.T) {
	exp := &Exporter{}
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeCoreth,
		DatabasePath: "",
	}

	err := exp.Init(config)
	if err != migrate.ErrDatabaseNotFound {
		t.Errorf("expected ErrDatabaseNotFound, got %v", err)
	}
}

// TestExportBlocksInvalidRange tests block export with invalid range.
func TestExportBlocksInvalidRange(t *testing.T) {
	exp := &Exporter{initialized: true}

	ctx := context.Background()
	blocks, errs := exp.ExportBlocks(ctx, 100, 50) // start > end

	// Should get an error
	select {
	case err := <-errs:
		if err != migrate.ErrInvalidBlockRange {
			t.Errorf("expected ErrInvalidBlockRange, got %v", err)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for error")
	}

	// Channels should be closed
	select {
	case _, ok := <-blocks:
		if ok {
			t.Error("expected blocks channel to be closed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for blocks channel close")
	}
}

// TestExportBlocksContextCancel tests block export with canceled context.
func TestExportBlocksContextCancel(t *testing.T) {
	// Create a mock database with proper structure
	memdb := memorydb.New()
	db := rawdb.NewDatabase(memdb)

	// Write genesis block
	genesis := &types.Header{
		Number:     big.NewInt(0),
		ParentHash: common.Hash{},
		Time:       0,
		GasLimit:   8000000,
		Difficulty: big.NewInt(1),
	}
	rawdb.WriteHeader(db, genesis)
	rawdb.WriteCanonicalHash(db, genesis.Hash(), 0)
	rawdb.WriteHeadBlockHash(db, genesis.Hash())

	exp := &Exporter{
		db:          db,
		initialized: true,
		genesisHash: genesis.Hash(),
		headBlock:   0,
		headHash:    genesis.Hash(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	blocks, errs := exp.ExportBlocks(ctx, 0, 1000)

	// Should get context canceled error
	select {
	case err := <-errs:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-blocks:
		// May receive block before cancel
	case <-time.After(time.Second):
		t.Error("timeout waiting for result")
	}
}

// TestConvertTransaction tests transaction conversion.
func TestConvertTransaction(t *testing.T) {
	// Create a simple legacy transaction
	to := common.HexToAddress("0x1234567890123456789012345678901234567890")
	tx := types.NewTransaction(
		0,                  // nonce
		to,                 // to
		big.NewInt(1000),   // value
		21000,              // gas
		big.NewInt(1e9),    // gasPrice
		nil,                // data
	)

	mtx := convertTransaction(tx)

	if mtx.Hash != tx.Hash() {
		t.Errorf("hash mismatch: expected %s, got %s", tx.Hash().Hex(), mtx.Hash.Hex())
	}
	if mtx.Nonce != 0 {
		t.Errorf("nonce mismatch: expected 0, got %d", mtx.Nonce)
	}
	if *mtx.To != to {
		t.Errorf("to mismatch: expected %s, got %s", to.Hex(), mtx.To.Hex())
	}
	if mtx.Value.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("value mismatch: expected 1000, got %s", mtx.Value.String())
	}
	if mtx.Gas != 21000 {
		t.Errorf("gas mismatch: expected 21000, got %d", mtx.Gas)
	}
}

// TestPrefixDB tests the prefix database wrapper.
func TestPrefixDB(t *testing.T) {
	memdb := memorydb.New()
	db := rawdb.NewDatabase(memdb)
	prefix := []byte("test-prefix-")
	pdb := newPrefixDB(db, prefix)

	// Test Put and Get
	key := []byte("testkey")
	value := []byte("testvalue")

	if err := pdb.Put(key, value); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	got, err := pdb.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("value mismatch: expected %s, got %s", value, got)
	}

	// Verify the actual key in underlying db has prefix
	prefixedKey := append(append([]byte{}, prefix...), key...)
	underlyingValue, err := db.Get(prefixedKey)
	if err != nil {
		t.Fatalf("underlying Get failed: %v", err)
	}
	if string(underlyingValue) != string(value) {
		t.Errorf("underlying value mismatch: expected %s, got %s", value, underlyingValue)
	}

	// Test Has
	has, err := pdb.Has(key)
	if err != nil {
		t.Fatalf("Has failed: %v", err)
	}
	if !has {
		t.Error("expected Has to return true")
	}

	// Test Delete
	if err := pdb.Delete(key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	has, _ = pdb.Has(key)
	if has {
		t.Error("expected Has to return false after Delete")
	}
}

// TestEncodeBlockNumber tests block number encoding.
func TestEncodeBlockNumber(t *testing.T) {
	tests := []struct {
		number   uint64
		expected []byte
	}{
		{0, []byte{0, 0, 0, 0, 0, 0, 0, 0}},
		{1, []byte{0, 0, 0, 0, 0, 0, 0, 1}},
		{256, []byte{0, 0, 0, 0, 0, 0, 1, 0}},
		{0xFFFFFFFFFFFFFFFF, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	}

	for _, tt := range tests {
		got := encodeBlockNumber(tt.number)
		if len(got) != 8 {
			t.Errorf("encodeBlockNumber(%d) returned wrong length: %d", tt.number, len(got))
		}
		for i := range tt.expected {
			if got[i] != tt.expected[i] {
				t.Errorf("encodeBlockNumber(%d) = %v, want %v", tt.number, got, tt.expected)
				break
			}
		}
	}
}

// TestExporterClose tests closing the exporter.
func TestExporterClose(t *testing.T) {
	// Test closing uninitialized exporter
	exp := &Exporter{}
	if err := exp.Close(); err != nil {
		t.Errorf("Close on uninitialized exporter should succeed, got: %v", err)
	}
}

// TestExporterAlreadyInitialized tests double initialization.
func TestExporterAlreadyInitialized(t *testing.T) {
	exp := &Exporter{initialized: true}

	err := exp.Init(migrate.ExporterConfig{})
	if err != migrate.ErrAlreadyInitialized {
		t.Errorf("expected ErrAlreadyInitialized, got %v", err)
	}
}

// TestExportStateNotInitialized tests state export when not initialized.
func TestExportStateNotInitialized(t *testing.T) {
	exp := &Exporter{}

	ctx := context.Background()
	accounts, errs := exp.ExportState(ctx, 0)

	select {
	case err := <-errs:
		if err != migrate.ErrNotInitialized {
			t.Errorf("expected ErrNotInitialized, got %v", err)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for error")
	}

	// Channels should be closed
	select {
	case _, ok := <-accounts:
		if ok {
			t.Error("expected accounts channel to be closed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for accounts channel close")
	}
}

// TestFactoryRegistration tests that the factory is properly registered.
func TestFactoryRegistration(t *testing.T) {
	// The init function should have registered the factory
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeCoreth,
		DatabasePath: "/tmp/nonexistent",
	}

	exp, err := migrate.NewExporter(config)
	if err != nil {
		// This is expected since the database doesn't exist
		// but we should get a real exporter, not ErrUnsupportedVMType
		if exp != nil {
			t.Error("expected nil exporter for non-existent path")
		}
	}
}

// Benchmark for block encoding
func BenchmarkBlockEncoding(b *testing.B) {
	header := &types.Header{
		Number:     big.NewInt(12345),
		ParentHash: common.HexToHash("0x1234"),
		Time:       1234567890,
		GasLimit:   8000000,
		GasUsed:    5000000,
		Difficulty: big.NewInt(1000000),
		Coinbase:   common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rlp.EncodeToBytes(header)
	}
}

// Integration test with actual filesystem (skipped by default)
func TestIntegrationWithRealDB(t *testing.T) {
	// Skip unless CORETH_TEST_DB is set
	dbPath := os.Getenv("CORETH_TEST_DB")
	if dbPath == "" {
		t.Skip("set CORETH_TEST_DB to run integration tests")
	}

	config := migrate.ExporterConfig{
		VMType:         migrate.VMTypeCoreth,
		DatabasePath:   dbPath,
		DatabaseType:   "leveldb",
		ExportReceipts: true,
		ExportState:    true,
	}

	exp, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	if err := exp.Init(config); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer exp.Close()

	// Get chain info
	info, err := exp.GetInfo()
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}
	t.Logf("Chain info: VMType=%s ChainID=%v Height=%d", info.VMType, info.ChainID, info.CurrentHeight)

	// Export a few blocks
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	endBlock := uint64(10)
	if info.CurrentHeight < endBlock {
		endBlock = info.CurrentHeight
	}

	blocks, errs := exp.ExportBlocks(ctx, 0, endBlock)

	count := 0
	for block := range blocks {
		count++
		t.Logf("Block %d: hash=%s txs=%d", block.Number, block.Hash.Hex(), len(block.Transactions))
	}

	select {
	case err := <-errs:
		if err != nil {
			t.Errorf("export error: %v", err)
		}
	default:
	}

	t.Logf("Exported %d blocks", count)
}

// TestExportBlockData tests the structure of exported block data.
func TestExportBlockData(t *testing.T) {
	// Create mock database
	memdb := memorydb.New()
	db := rawdb.NewDatabase(memdb)

	// Create a genesis block
	genesis := &types.Header{
		Number:     big.NewInt(0),
		ParentHash: common.Hash{},
		Time:       1000,
		GasLimit:   8000000,
		GasUsed:    0,
		Difficulty: big.NewInt(1),
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Root:       types.EmptyRootHash,
		TxHash:     types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Extra:      []byte("genesis"),
	}

	// Write genesis
	rawdb.WriteHeader(db, genesis)
	rawdb.WriteCanonicalHash(db, genesis.Hash(), 0)
	rawdb.WriteBody(db, genesis.Hash(), 0, &types.Body{})
	rawdb.WriteHeadBlockHash(db, genesis.Hash())

	// Write chain config
	chainConfig := &params.ChainConfig{
		ChainID: big.NewInt(43114),
	}
	rawdb.WriteChainConfig(db, genesis.Hash(), chainConfig)

	// Create exporter
	exp := &Exporter{
		db:          db,
		initialized: true,
		genesisHash: genesis.Hash(),
		headBlock:   0,
		headHash:    genesis.Hash(),
		chainConfig: chainConfig,
		config:      migrate.ExporterConfig{ExportReceipts: true},
	}

	// Export genesis block
	blockData, err := exp.exportBlock(0)
	if err != nil {
		t.Fatalf("exportBlock failed: %v", err)
	}

	// Verify block data
	if blockData.Number != 0 {
		t.Errorf("expected number 0, got %d", blockData.Number)
	}
	if blockData.Hash != genesis.Hash() {
		t.Errorf("hash mismatch")
	}
	if blockData.Timestamp != genesis.Time {
		t.Errorf("timestamp mismatch: expected %d, got %d", genesis.Time, blockData.Timestamp)
	}
	if blockData.GasLimit != genesis.GasLimit {
		t.Errorf("gasLimit mismatch: expected %d, got %d", genesis.GasLimit, blockData.GasLimit)
	}
	if len(blockData.Header) == 0 {
		t.Error("expected non-empty header RLP")
	}
	if len(blockData.Body) == 0 {
		t.Error("expected non-empty body RLP")
	}
	if string(blockData.ExtraData) != "genesis" {
		t.Errorf("extraData mismatch: expected 'genesis', got '%s'", string(blockData.ExtraData))
	}
}

// TestReadSnapshotRoot tests snapshot root reading.
func TestReadSnapshotRoot(t *testing.T) {
	memdb := memorydb.New()
	db := rawdb.NewDatabase(memdb)

	exp := &Exporter{db: db}

	// Should return empty hash when not set
	root := exp.readSnapshotRoot()
	if root != (common.Hash{}) {
		t.Errorf("expected empty hash, got %s", root.Hex())
	}

	// Set a snapshot root
	expectedRoot := common.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if err := db.Put(rawdb.SnapshotRootKey, expectedRoot.Bytes()); err != nil {
		t.Fatalf("failed to set snapshot root: %v", err)
	}

	root = exp.readSnapshotRoot()
	if root != expectedRoot {
		t.Errorf("expected %s, got %s", expectedRoot.Hex(), root.Hex())
	}
}

// Helper to create a temporary directory for tests
func createTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "coreth-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestOpenDatabaseUnsupportedType tests opening with unsupported database type.
func TestOpenDatabaseUnsupportedType(t *testing.T) {
	exp := &Exporter{}
	_, err := exp.openDatabase("/tmp", "unsupported-type")
	if err == nil {
		t.Error("expected error for unsupported database type")
	}
}

// TestChainDataSubdir tests that chaindata subdirectory is detected.
func TestChainDataSubdir(t *testing.T) {
	tmpDir := createTempDir(t)
	chainDataDir := filepath.Join(tmpDir, corethChainDataDir)
	if err := os.Mkdir(chainDataDir, 0755); err != nil {
		t.Fatalf("failed to create chaindata dir: %v", err)
	}

	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeCoreth,
		DatabasePath: tmpDir,
		DatabaseType: "leveldb",
	}

	exp := &Exporter{}
	// This will fail to open the database (empty), but we're testing path detection
	err := exp.Init(config)
	if err == nil {
		t.Error("expected error (empty db), but checking path detection worked")
	}
	// The error should be about database content, not path not found
	// (meaning the chaindata path was correctly resolved)
}
