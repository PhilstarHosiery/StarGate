@echo off
:: Generate Go code from proto/stargate.proto into gen/
::
:: Prerequisites:
::   protoc          https://github.com/protocolbuffers/protobuf/releases
::   protoc-gen-go       go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
::   protoc-gen-go-grpc  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

if not exist gen mkdir gen

protoc ^
  --proto_path=..\proto ^
  --go_out=gen ^
  --go_opt=paths=source_relative ^
  --go-grpc_out=gen ^
  --go-grpc_opt=paths=source_relative ^
  ..\proto\stargate.proto

if %ERRORLEVEL% neq 0 (
    echo.
    echo ERROR: protoc failed. Make sure protoc and the Go plugins are installed and on PATH.
    exit /b 1
)

:: Remove the stub file now that real generated code exists.
if exist gen\stub.go del gen\stub.go

echo Done. Generated files are in gen\
