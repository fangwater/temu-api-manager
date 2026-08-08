package migrations

import _ "embed"

// InitSQL creates the Temu order and shipment ledger.
//
//go:embed 001_init.sql
var InitSQL string
