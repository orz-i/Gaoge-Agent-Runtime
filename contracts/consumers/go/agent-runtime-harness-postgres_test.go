package consumer

import (
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/gorm"
)

var (
	_ func(*gorm.DB) error                           = harnesspostgres.Migrate
	_ func(*gorm.DB) (*harnesspostgres.Store, error) = harnesspostgres.New
	_ harness.Store                                  = (*harnesspostgres.Store)(nil)
)
