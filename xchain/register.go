// Package xchain provides X-Chain migration functionality registration.
package xchain

import (
	"github.com/luxfi/migrate"
)

func init() {
	// Register X-Chain exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypeXChain, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})

	// Register X-Chain importer factory
	migrate.RegisterImporterFactory(migrate.VMTypeXChain, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		return NewImporter(config)
	})
}
