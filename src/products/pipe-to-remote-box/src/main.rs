#![forbid(unsafe_code)]
#![warn(clippy::pedantic)]

use std::process::ExitCode;

use clap::Parser;

mod ssh;

const VERSION: &str = match option_env!("CARGO_PKG_VERSION") {
    Some(version) => version,
    None => "0.0.0-dev",
};

#[derive(Parser, Debug)]
#[command(
    name = "pipe-to-remote-box",
    version = VERSION,
    about = "Read-only status for an explicitly selected remote directory over SSH"
)]
struct Cli {
    /// Existing SSH host alias or hostname to check.
    #[arg(long)]
    host: String,

    /// Absolute remote directory to inspect.
    #[arg(long)]
    directory: String,

    /// End-to-end SSH and remote-probe deadline in seconds (1-60).
    #[arg(long, default_value_t = 10, value_parser = clap::value_parser!(u8).range(1..=60))]
    timeout: u8,
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    match ssh::check(&cli.host, &cli.directory, cli.timeout) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("pipe-to-remote-box: {error}");
            ExitCode::FAILURE
        }
    }
}
