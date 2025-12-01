package xchain

import (
	"context"
	"testing"

	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

func TestNewExporter(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	if exporter == nil {
		t.Fatal("exporter should not be nil")
	}
}

func TestExporterNotInitialized(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	// Should fail because not initialized
	_, err = exporter.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestExporterClose(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	err = exporter.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestExportBlocksInvalidRange(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeXChain,
		RPCURL: "http://localhost:9650",
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	// Invalid range: start > end
	ctx := context.Background()
	blocks, errs := exporter.ExportBlocks(ctx, 100, 50)

	// Drain channels
	go func() {
		for range blocks {
		}
	}()

	err = <-errs
	if err != migrate.ErrInvalidBlockRange {
		t.Errorf("expected ErrInvalidBlockRange, got: %v", err)
	}
}

func TestConvertBlock(t *testing.T) {
	exporter := &Exporter{
		info: &XChainInfo{},
	}

	block := &XChainBlock{
		Height: 100,
		Transactions: []XChainTx{
			{Type: "BaseTx"},
			{Type: "CreateAssetTx"},
		},
	}

	blockData := exporter.convertBlock(block)

	if blockData.Number != 100 {
		t.Errorf("expected block number 100, got: %d", blockData.Number)
	}

	if len(blockData.Transactions) != 2 {
		t.Errorf("expected 2 transactions, got: %d", len(blockData.Transactions))
	}

	vmType, ok := blockData.Extensions["vmType"].(string)
	if !ok || vmType != "x-chain" {
		t.Errorf("expected vmType 'x-chain', got: %v", vmType)
	}
}

func TestGetTxTypeName(t *testing.T) {
	tests := []struct {
		typeID   uint32
		expected string
	}{
		{0, "BaseTx"},
		{1, "CreateAssetTx"},
		{2, "OperationTx"},
		{3, "ImportTx"},
		{4, "ExportTx"},
		{99, "Unknown(99)"},
	}

	for _, tc := range tests {
		result := getTxTypeName(tc.typeID)
		if result != tc.expected {
			t.Errorf("getTxTypeName(%d) = %s, expected %s", tc.typeID, result, tc.expected)
		}
	}
}

func TestUTXOsToAccount(t *testing.T) {
	// Test with empty info - should sum UTXOs with matching empty asset ID
	exporter := &Exporter{
		info: &XChainInfo{},
	}

	utxos := []*UTXO{
		{Amount: 100}, // AssetID is empty, matches empty XAssetID
		{Amount: 200},
		{Amount: 50},
	}

	account := exporter.utxosToAccount("X-lux1abc123", utxos)

	if account == nil {
		t.Fatal("account should not be nil")
	}

	// Balance should be 350 (sum of all UTXOs with empty asset ID matching empty XAssetID)
	expectedBalance := uint64(350)
	if account.Balance.Uint64() != expectedBalance {
		t.Errorf("expected balance %d, got: %d", expectedBalance, account.Balance.Uint64())
	}

	// Nonce should always be 0 for X-Chain
	if account.Nonce != 0 {
		t.Errorf("expected nonce 0, got: %d", account.Nonce)
	}
}

func TestUTXOsToAccountWithDifferentAssets(t *testing.T) {
	// Test with specific asset ID
	exporter := &Exporter{
		info: &XChainInfo{
			XAssetID: ids.ID{1, 2, 3}, // specific asset ID
		},
	}

	utxos := []*UTXO{
		{Amount: 100, AssetID: ids.ID{1, 2, 3}}, // Matches XAssetID
		{Amount: 200, AssetID: ids.ID{4, 5, 6}}, // Different asset
		{Amount: 50, AssetID: ids.ID{1, 2, 3}},  // Matches XAssetID
	}

	account := exporter.utxosToAccount("X-lux1abc123", utxos)

	// Only UTXOs with matching XAssetID should be counted (100 + 50 = 150)
	expectedBalance := uint64(150)
	if account.Balance.Uint64() != expectedBalance {
		t.Errorf("expected balance %d, got: %d", expectedBalance, account.Balance.Uint64())
	}
}
