module github.com/Shaik-Sirajuddin/memory

go 1.26.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/Shaik-Sirajuddin/memory/mcp v0.0.0
	github.com/Shaik-Sirajuddin/memory/pkg/log v0.0.0
	github.com/Shaik-Sirajuddin/memory/svc/ptydaemon v0.0.0
	github.com/adrg/xdg v0.5.3
	github.com/creack/pty v1.1.24
	github.com/go-viper/mapstructure/v2 v2.5.0
	github.com/google/jsonschema-go v0.4.3
	github.com/google/uuid v1.6.0
	github.com/invopop/jsonschema v0.14.0
	github.com/knadh/koanf/parsers/json v1.0.0
	github.com/knadh/koanf/providers/posflag v1.0.1
	github.com/knadh/koanf/providers/rawbytes v1.0.0
	github.com/knadh/koanf/v2 v2.3.4
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/sys v0.45.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.50.1
)

require (
	github.com/Shaik-Sirajuddin/memory/pkg/sockpath v0.0.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/mark3labs/mcp-go v0.54.1 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/otelslog v0.18.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.19.0 // indirect
	go.opentelemetry.io/otel/log v0.19.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.19.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/grpc v1.80.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

require (
	github.com/Shaik-Sirajuddin/memory/svc/hook-operator v0.0.0
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/term v0.42.0
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/Shaik-Sirajuddin/memory/mcp => ../tunnel_mcp

replace github.com/Shaik-Sirajuddin/memory/pkg/log => ../pkg/log

replace github.com/Shaik-Sirajuddin/memory/pkg/sockpath => ../pkg/sockpath

replace github.com/Shaik-Sirajuddin/memory/svc/ptydaemon => ../svc/ptydaemon

replace github.com/Shaik-Sirajuddin/memory/svc/hook-operator => ../svc/hook-operator
