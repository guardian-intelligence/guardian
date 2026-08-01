#![forbid(unsafe_code)]
#![warn(clippy::pedantic)]

use std::{io, process::{Command, ExitCode}};

use clap::Parser;

const VERSION: &str = match option_env!("CARGO_PKG_VERSION") { Some(version) => version, None => "0.0.0-dev" };
const REMOTE_CHECK: &str = r#"set -u
dir=$1
printf 'directory='; printf '%s\n' "$dir"
if [ ! -d "$dir" ]; then echo 'status=missing'; exit 3; fi
echo 'status=present'
printf 'modified='; stat -c '%y' "$dir" 2>/dev/null || stat -f '%Sm' "$dir" 2>/dev/null || echo unavailable
printf 'entries='; find "$dir" -mindepth 1 -maxdepth 1 -print 2>/dev/null | wc -l
echo 'recent_changes='
find "$dir" -mindepth 1 -maxdepth 2 -type f -mtime -7 -print 2>/dev/null | head -n 20
if [ -d "$dir/.git" ] && command -v git >/dev/null 2>&1; then
  echo 'git_status='
  git -C "$dir" status --short 2>/dev/null || true
  echo 'git_history='
  git -C "$dir" log -1 --format='%h %ad %s' --date=iso-strict 2>/dev/null || true
fi
"#;

#[derive(Parser, Debug)]
#[command(name = "pipe-to-remote-box", version = VERSION, about = "Read-only status for a remote directory over SSH")]
struct Cli {
    /// Existing SSH host alias or hostname to check.
    #[arg(long)]
    host: String,
    /// Absolute remote directory to inspect.
    #[arg(long)]
    directory: String,
    /// Connection and remote-command deadline in seconds (1-60).
    #[arg(long, default_value_t = 10, value_parser = clap::value_parser!(u8).range(1..=60))]
    timeout: u8,
}

fn valid_host(host: &str) -> bool {
    !host.is_empty() && host.len() <= 253 && !host.starts_with('-') && !host.chars().any(char::is_whitespace) && !host.chars().any(char::is_control)
}

fn valid_directory(directory: &str) -> bool {
    directory.starts_with('/') && directory.len() <= 1024 && !directory.split('/').any(|part| part == "..") && directory.chars().all(|character| character.is_ascii_alphanumeric() || matches!(character, '/' | '.' | '_' | '-'))
}

fn ssh_command(host: &str, directory: &str, timeout: u8) -> Command {
    let mut command = Command::new("ssh");
    let remote_command = format!("sh -s -- {directory}");
    command.args(["-o", "BatchMode=yes", "-o", "NumberOfPasswordPrompts=0", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "RequestTTY=no", "-o", "ClearAllForwardings=yes", "-o", &format!("ConnectTimeout={timeout}"), "--", host, &remote_command]);
    command.stdin(std::process::Stdio::piped());
    command
}

fn run(cli: &Cli) -> Result<(), String> {
    if !valid_host(&cli.host) { return Err("--host must be a non-option SSH alias or hostname without whitespace".into()); }
    if !valid_directory(&cli.directory) { return Err("--directory must be an absolute path containing only letters, digits, /, ., _, and -".into()); }
    let mut command = ssh_command(&cli.host, &cli.directory, cli.timeout);
    let mut child = command.spawn().map_err(|error| match error.kind() {
        io::ErrorKind::NotFound => "ssh executable was not found on PATH".to_owned(),
        _ => format!("could not start ssh: {error}"),
    })?;
    use std::io::Write;
    child.stdin.take().ok_or("could not open SSH input")?.write_all(REMOTE_CHECK.as_bytes()).map_err(|error| format!("could not send remote check: {error}"))?;
    let status = child.wait().map_err(|error| format!("could not wait for SSH: {error}"))?;
    if status.success() { Ok(()) } else { Err(format!("SSH status check failed with {status}; verify the configured host is reachable with non-interactive public-key authentication")) }
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    match run(&cli) { Ok(()) => ExitCode::SUCCESS, Err(error) => { eprintln!("pipe-to-remote-box: {error}"); ExitCode::FAILURE } }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn accepts_safe_arguments() { assert!(valid_host("remote-box")); assert!(valid_directory("/srv/writing")); }
    #[test]
    fn rejects_unsafe_arguments() { assert!(!valid_host("-oProxyCommand=x")); assert!(!valid_host("host name")); assert!(!valid_directory("relative")); assert!(!valid_directory("/srv/../secrets")); assert!(!valid_directory("/tmp/$(x)")); }
    #[test]
    fn ssh_is_non_interactive_and_remote_check_has_no_mutation_verbs() {
        let command = format!("{:?}", ssh_command("host", "/srv/work", 10));
        assert!(command.contains("BatchMode=yes")); assert!(command.contains("RequestTTY=no"));
        for forbidden in [" apply ", " delete ", " rm ", " scale ", " rollout restart ", "kubectl", "docker", "directus"] { assert!(!REMOTE_CHECK.contains(forbidden)); }
    }
}
