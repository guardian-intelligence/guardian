#![forbid(unsafe_code)]
#![warn(clippy::pedantic)]

mod auth;
mod device;
mod error;
#[cfg(test)]
mod testing;

use std::process::ExitCode;

use clap::{Args, Parser, Subcommand};

const VERSION: &str = match option_env!("CARGO_PKG_VERSION") {
    Some(version) => version,
    None => "0.0.0-dev",
};

const DEFAULT_ISSUER: &str = "https://guardianintelligence.org/realms/guardianintelligence.org";
const DEFAULT_CLIENT_ID: &str = "postflight-cli";
const DEFAULT_DEVICE_URL: &str = "https://guardianintelligence.org/postflight/device";

#[derive(Parser)]
#[command(
    name = "postflight",
    version = VERSION,
    about = "Postflight — fast CI for GitHub, by Guardian Intelligence"
)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Print the CLI version.
    Version,
    /// Authenticate with your Guardian account.
    #[command(subcommand)]
    Auth(AuthCommand),
}

#[derive(Subcommand)]
enum AuthCommand {
    /// Sign in from this terminal by approving the request in a browser.
    Login(LoginArgs),
    /// Show the account you are signed in as.
    Status,
    /// Remove credentials stored on this machine.
    Logout,
}

#[derive(Args)]
struct LoginArgs {
    /// OIDC issuer to authenticate against.
    #[arg(long, env = "POSTFLIGHT_ISSUER", default_value = DEFAULT_ISSUER, hide = true)]
    issuer: String,

    /// OAuth client id presented to the issuer.
    #[arg(long, env = "POSTFLIGHT_CLIENT_ID", default_value = DEFAULT_CLIENT_ID, hide = true)]
    client_id: String,

    /// Page where the sign-in request is approved.
    #[arg(long, env = "POSTFLIGHT_DEVICE_URL", default_value = DEFAULT_DEVICE_URL, hide = true)]
    device_url: String,
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    let outcome = match cli.command {
        Command::Version => {
            println!("postflight version {VERSION}");
            Ok(true)
        }
        Command::Auth(AuthCommand::Login(args)) => auth::login(&auth::LoginOptions {
            issuer: args.issuer,
            client_id: args.client_id,
            device_url: args.device_url,
        })
        .map(|()| true),
        Command::Auth(AuthCommand::Status) => auth::status(),
        Command::Auth(AuthCommand::Logout) => auth::logout().map(|()| true),
    };
    match outcome {
        Ok(true) => ExitCode::SUCCESS,
        Ok(false) => ExitCode::FAILURE,
        Err(err) => {
            eprintln!("postflight: {err}");
            ExitCode::FAILURE
        }
    }
}
