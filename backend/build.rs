// Compiles ../proto/stargate.proto into Rust gRPC server code at build time.
// protox is a pure-Rust protobuf compiler, so no `protoc` binary is required.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let proto = "../proto/stargate.proto";
    println!("cargo:rerun-if-changed={proto}");

    let fds = protox::compile([proto], ["../proto"])?;
    tonic_prost_build::configure()
        .build_client(false)
        .compile_fds(fds)?;
    Ok(())
}
