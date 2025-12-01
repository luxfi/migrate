// Package qchain provides Q-Chain export/import functionality
// Q-Chain is EVM-compatible and uses RPC-based data access
package qchain

import "github.com/luxfi/migrate"

// Register Q-Chain exporter/importer with migrate package
func init() {
	migrate.RegisterExporterFactory(migrate.VMTypeQChain, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})

	migrate.RegisterImporterFactory(migrate.VMTypeQChain, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		return NewImporter(config)
	})
}

// Ensure interface compliance at compile time
var (
	_ migrate.Exporter = (*Exporter)(nil)
	_ migrate.Importer = (*Importer)(nil)
)
