package xchain

import (
	"testing"

	"github.com/luxfi/migrate"
)

func TestNewImporter(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	if importer == nil {
		t.Fatal("importer should not be nil")
	}
}

func TestImporterNotInitialized(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Should fail because not initialized
	err = importer.ImportBlock(&migrate.BlockData{})
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestImporterClose(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	err = importer.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestImportConfigNoOp(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// ImportConfig is a no-op for X-Chain
	err = importer.ImportConfig(&migrate.Config{})
	if err != nil {
		t.Errorf("ImportConfig should return nil, got: %v", err)
	}
}

func TestImporterPendingTxs(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Initially no pending txs
	txs := importer.GetPendingTxs()
	if len(txs) != 0 {
		t.Errorf("expected 0 pending txs, got: %d", len(txs))
	}

	// Clear should work even when empty
	importer.ClearPendingTxs()
	txs = importer.GetPendingTxs()
	if len(txs) != 0 {
		t.Errorf("expected 0 pending txs after clear, got: %d", len(txs))
	}
}

func TestImportBlocksEmpty(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Manually set initialized to test empty blocks import
	importer.initialized = true

	// Empty blocks should succeed
	err = importer.ImportBlocks([]*migrate.BlockData{})
	if err != nil {
		t.Errorf("ImportBlocks with empty slice should succeed, got: %v", err)
	}
}

func TestImportStateNotInitialized(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Should fail because not initialized
	err = importer.ImportState([]*migrate.Account{}, 0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestVerifyImportNotInitialized(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Should fail because not initialized
	err = importer.VerifyImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestFinalizeImportNotInitialized(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Should fail because not initialized
	err = importer.FinalizeImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestExecuteBlockCallsImportBlock(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	// Both should return the same error when not initialized
	err1 := importer.ImportBlock(&migrate.BlockData{})
	err2 := importer.ExecuteBlock(&migrate.BlockData{})

	if err1 != err2 {
		t.Errorf("ImportBlock and ExecuteBlock should return same error: %v vs %v", err1, err2)
	}
}
