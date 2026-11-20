# StarGate Backend (Rust)
#
# Requires a Rust toolchain (https://rustup.rs). No protoc needed: build.rs
# compiles ../proto/stargate.proto with the pure-Rust protox compiler.

.PHONY: build run test lint clean

build:
	cargo build --release
	@echo "Built: target/release/stargate-server"

run:
	cargo run

test:
	cargo test

lint:
	cargo fmt --check
	cargo clippy --all-targets -- -D warnings

clean:
	cargo clean
