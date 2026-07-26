#![forbid(unsafe_code)]
#![warn(clippy::pedantic)]

mod auth;
mod device;
mod error;
mod receipt;
#[cfg(test)]
mod testing;
mod uninstall;

use std::process::ExitCode;

use clap::{Args, Parser, Subcommand};

const VERSION: &str = match option_env!("CARGO_PKG_VERSION") {
    Some(version) => version,
    None => "0.0.0-dev",
};

pub const DEFAULT_ISSUER: &str = "https://guardianintelligence.org/realms/guardianintelligence.org";
pub const DEFAULT_CLIENT_ID: &str = "postflight-cli";
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
    /// Print the CLI version and where this copy came from.
    Version(VersionArgs),
    /// Authenticate with your Guardian account.
    #[command(subcommand)]
    Auth(AuthCommand),
    /// Manage this installation of the CLI.
    #[command(name = "self", subcommand)]
    Manage(ManageCommand),
}

#[derive(Args)]
struct VersionArgs {
    /// Print the bare version and nothing else.
    #[arg(long)]
    short: bool,
}

#[derive(Subcommand)]
enum ManageCommand {
    /// Remove this installation, its credentials, and its receipt.
    Uninstall(UninstallArgs),
}

#[derive(Args)]
struct UninstallArgs {
    /// Leave stored credentials in place.
    #[arg(long)]
    keep_credentials: bool,

    /// Do not ask for confirmation.
    #[arg(long, short = 'y')]
    yes: bool,
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

/// The first line is fixed: release tooling reads it to learn what a binary
/// claims to be, and provenance is added below it rather than woven into it.
fn print_version(short: bool) {
    if short {
        println!("{VERSION}");
        return;
    }
    println!("postflight version {VERSION}");
    if let Some(receipt) = receipt::for_current_exe() {
        if let Some(tag) = receipt::Receipt::field(receipt.tag.as_ref()) {
            println!("  release   {tag}");
        }
        if let Some(channel) = receipt::Receipt::field(receipt.channel.as_ref()) {
            println!("  channel   {channel}");
        }
        match receipt::Receipt::field(receipt.installed_at.as_ref()) {
            Some(at) => println!("  installed {at} via {}", receipt.method),
            None => println!("  installed via {}", receipt.method),
        }
    }
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    let outcome = match cli.command {
        Command::Version(args) => {
            print_version(args.short);
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
        Command::Manage(ManageCommand::Uninstall(args)) => uninstall::run(&uninstall::Options {
            keep_credentials: args.keep_credentials,
            assume_yes: args.yes,
        }),
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
