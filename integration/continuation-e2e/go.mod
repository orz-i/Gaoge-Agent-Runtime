module example.com/agent-runtime-continuation-e2e

go 1.26.6

require (
	github.com/go-redis/redis/v8 v8.11.5
	github.com/google/uuid v1.6.0
	github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime v0.1.0-beta.22
	github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres v0.1.0-beta.22
	github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis v0.1.0-beta.22
	gorm.io/driver/postgres v1.6.0
	gorm.io/gorm v1.31.1
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime => ../../go/agent-runtime

replace github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres => ../../go/agent-runtime-postgres

replace github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis => ../../go/agent-runtime-redis
