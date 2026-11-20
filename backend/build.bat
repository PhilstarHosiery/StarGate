@echo off
:: Build the StarGate backend server binary.
cargo build --release
if %ERRORLEVEL% neq 0 (
    echo Build failed.
    exit /b 1
)
copy /Y target\release\stargate-server.exe stargate-server.exe >nul
echo Built: stargate-server.exe
