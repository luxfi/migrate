package zool2

import (
	"testing"

	"github.com/luxfi/migrate"
)

func TestExporterImplementsInterface(t *testing.T) {
	var _ migrate.Exporter = (*Exporter)(nil)
}

func TestImporterImplementsInterface(t *testing.T) {
	var _ migrate.Importer = (*Importer)(nil)
}

func TestNewExporter(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType:       migrate.VMTypeZooL2,
		DatabasePath: "/tmp/nonexistent-test-db",
	}

	exp, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}
	if exp == nil {
		t.Fatal("NewExporter returned nil exporter")
	}

	// Should not be initialized yet
	_, err = exp.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestNewImporter(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType:       migrate.VMTypeZooL2,
		DatabasePath: "/tmp/nonexistent-test-db",
	}

	imp, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}
	if imp == nil {
		t.Fatal("NewImporter returned nil importer")
	}

	// Should not be initialized yet
	err = imp.VerifyImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestExporterDoubleInit(t *testing.T) {
	exp := &Exporter{initialized: true}
	err := exp.Init(migrate.ExporterConfig{})
	if err != migrate.ErrAlreadyInitialized {
		t.Errorf("Expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestImporterDoubleInit(t *testing.T) {
	imp := &Importer{initialized: true}
	err := imp.Init(migrate.ImporterConfig{})
	if err != migrate.ErrAlreadyInitialized {
		t.Errorf("Expected ErrAlreadyInitialized, got %v", err)
	}
}
