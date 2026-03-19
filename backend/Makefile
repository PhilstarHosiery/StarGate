# StarGate Backend
#
# Prerequisites for 'gen' target:
#   protoc              https://github.com/protocolbuffers/protobuf/releases
#   protoc-gen-go       go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   protoc-gen-go-grpc  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

.PHONY: gen build run tidy

gen:
	mkdir -p gen
	protoc \
		--proto_path=../proto \
		--go_out=gen \
		--go_opt=paths=source_relative \
		--go-grpc_out=gen \
		--go-grpc_opt=paths=source_relative \
		../proto/stargate.proto
	rm -f gen/stub.go

build:
	go build -o stargate-server ./cmd/server

run:
	go run ./cmd/server

tidy:
	go mod tidy
