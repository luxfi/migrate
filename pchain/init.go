// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package pchain

import "github.com/luxfi/migrate"

func init() {
	// Register P-Chain exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypePChain, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})

	// Register P-Chain importer factory
	migrate.RegisterImporterFactory(migrate.VMTypePChain, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		return NewImporter(config)
	})
}
