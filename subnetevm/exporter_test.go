package subnetevm

import (
	"context"
	"testing"

	"github.com/luxfi/migrate"
)

func TestNewExporter(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: "/tmp/nonexistent",
		DatabaseType: "pebble",
	}

	exp, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}
	if exp == nil {
		t.Fatal("NewExporter returned nil")
	}
}

func TestExporterNotInitialized(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: "/tmp/nonexistent",
		DatabaseType: "pebble",
	}

	exp, _ := NewExporter(config)

	// Test GetInfo without Init
	_, err := exp.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("GetInfo expected ErrNotInitialized, got %v", err)
	}

	// Test ExportBlocks without Init
	ctx := context.Background()
	blocks, errs := exp.ExportBlocks(ctx, 0, 10)

	// Drain channels
	for range blocks {
	}
	for err := range errs {
		if err != migrate.ErrNotInitialized {
			t.Errorf("ExportBlocks expected ErrNotInitialized, got %v", err)
		}
	}

	// Test ExportConfig without Init
	_, err = exp.ExportConfig()
	if err != migrate.ErrNotInitialized {
		t.Errorf("ExportConfig expected ErrNotInitialized, got %v", err)
	}
}

func TestExporterInvalidBlockRange(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: "/tmp/nonexistent",
		DatabaseType: "pebble",
	}

	exp, _ := NewExporter(config)

	// Test invalid block range (start > end)
	ctx := context.Background()
	blocks, errs := exp.ExportBlocks(ctx, 100, 10)

	// Drain channels
	for range blocks {
	}
	for err := range errs {
		if err != migrate.ErrInvalidBlockRange {
			t.Errorf("ExportBlocks expected ErrInvalidBlockRange, got %v", err)
		}
	}
}

func TestExporterClose(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: "/tmp/nonexistent",
		DatabaseType: "pebble",
	}

	exp, _ := NewExporter(config)

	// Close without init should not error
	err := exp.Close()
	if err != nil {
		t.Errorf("Close without init should not error, got %v", err)
	}
}
