// Package zool2 provides Zoo L2 chain export/import functionality.
package zool2

import "github.com/luxfi/migrate"

func init() {
	// Register Zoo L2 exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypeZooL2, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		exp, err := NewExporter(config)
		if err != nil {
			return nil, err
		}
		if err := exp.Init(config); err != nil {
			return nil, err
		}
		return exp, nil
	})

	// Register Zoo L2 importer factory
	migrate.RegisterImporterFactory(migrate.VMTypeZooL2, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		imp, err := NewImporter(config)
		if err != nil {
			return nil, err
		}
		if err := imp.Init(config); err != nil {
			return nil, err
		}
		return imp, nil
	})
}
