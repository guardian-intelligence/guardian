#![forbid(unsafe_code)]
#![warn(clippy::pedantic)]

use std::{io, process::{Command, ExitCode}};

use clap::Parser;

const VERSION: &str = match option_env!("CARGO_PKG_VERSION") { Some(version) => version, None => "0.0.0-dev" };
const REMOTE_CHECK: &str = r#"set -u
echo 'directus-migration-monitor: remote status'
printf 'directus_systemd_units='; systemctl list-units --all --no-pager '*directus*' 2>/dev/null | tail -n +2 | wc -l || true
printf 'directus_containers='; (docker ps -a --format '{{.Names}} {{.Image}} {{.Status}}' 2>/dev/null; podman ps -a --format '{{.Names}} {{.Image}} {{.Status}}' 2>/dev/null) | grep -ic directus || true
printf 'directus_processes='; ps -axo command= 2>/dev/null | grep -i '[d]irectus' | wc -l || true
printf 'directus_kubernetes_objects='; if command -v kubectl >/dev/null 2>&1; then kubectl get deploy,statefulset,pod -A --no-headers 2>/dev/null | grep -ic directus || true; else echo unavailable; fi
"#;

#[derive(Parser, Debug)]
#[command(name = "directus-migration-monitor", version = VERSION, about = "Read-only Directus/blog migration status over SSH")]
struct Cli {
    /// Existing SSH host alias or hostname to check.
    #[arg(long)]
    host: String,
    /// Connection and remote-command deadline in seconds (1-60).
    #[arg(long, default_value_t = 10, value_parser = clap::value_parser!(u8).range(1..=60))]
    timeout: u8,
}

fn valid_host(host: &str) -> bool {
    !host.is_empty() && host.len() <= 253 && !host.starts_with('-') && !host.chars().any(char::is_whitespace) && !host.chars().any(char::is_control)
}

fn ssh_command(host: &str, timeout: u8) -> Command {
    let mut command = Command::new("ssh");
    command.args(["-o", "BatchMode=yes", "-o", "NumberOfPasswordPrompts=0", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "RequestTTY=no", "-o", "ClearAllForwardings=yes", "-o", &format!("ConnectTimeout={timeout}"), "--", host, REMOTE_CHECK]);
    command
}

fn run(cli: &Cli) -> Result<(), String> {
    if !valid_host(&cli.host) { return Err("--host must be a non-option SSH alias or hostname without whitespace".into()); }
    let status = ssh_command(&cli.host, cli.timeout).status().map_err(|error| match error.kind() {
        io::ErrorKind::NotFound => "ssh executable was not found on PATH".to_owned(),
        _ => format!("could not start ssh: {error}"),
    })?;
    if status.success() { Ok(()) } else { Err(format!("SSH status check failed with {status}; verify the configured host is reachable with non-interactive public-key authentication")) }
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    match run(&cli) { Ok(()) => ExitCode::SUCCESS, Err(error) => { eprintln!("directus-migration-monitor: {error}"); ExitCode::FAILURE } }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn accepts_safe_host_names() { assert!(valid_host("migration-host")); assert!(valid_host("ops@example.org")); }
    #[test]
    fn rejects_option_and_whitespace_in_host() { assert!(!valid_host("-oProxyCommand=x")); assert!(!valid_host("host name")); assert!(!valid_host("")); }
    #[test]
    fn ssh_is_non_interactive_and_remote_check_has_no_mutation_verbs() {
        let command = format!("{:?}", ssh_command("host", 10));
        assert!(command.contains("BatchMode=yes")); assert!(command.contains("RequestTTY=no"));
        for forbidden in [" apply ", " delete ", " rm ", " scale ", " rollout restart "] { assert!(!REMOTE_CHECK.contains(forbidden)); }
    }
}
