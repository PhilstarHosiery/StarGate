@echo off
:: Build the StarGate backend server binary.
go build -o stargate-server.exe ./cmd/server
if %ERRORLEVEL% neq 0 (
    echo Build failed.
    exit /b 1
)
echo Built: stargate-server.exe
