module github.com/machinarchy/machinarchy-agent

go 1.27.0

toolchain go1.27.4

require (
	github.com/machinarchy/machinarchy-agent/protocol v0.0.0
	google.golang.org/grpc v1.85.0
)

require (
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261110000943-09e21b237f2c // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/machinarchy/machinarchy-agent/protocol => ./protocol
