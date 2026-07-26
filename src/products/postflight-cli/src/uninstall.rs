//! `postflight self uninstall`.

use std::env;
use std::fs;
use std::io::{self, IsTerminal, Write};
use std::path::{Path, PathBuf};

use crate::auth::{self, SignOut};
use crate::error::Error;
use crate::receipt::{self, Receipt};

pub struct Options {
    pub keep_credentials: bool,
    pub assume_yes: bool,
}

/// Who is entitled to delete this copy of the binary.
enum Owner {
    /// The installer put it here and left a receipt saying so.
    Installed,
    /// A package manager owns the file. Deleting it behind the manager's back
    /// desyncs its manifest and leaves the user worse off than before they
    /// asked to uninstall.
    Managed { by: &'static str, command: String },
    /// No receipt, no recognisable manager. Could be `make install`, a copy, or
    /// a build output — none of which are ours to guess at.
    Unclaimed,
}

pub fn run(options: &Options) -> Result<bool, Error> {
    let binary = env::current_exe()
        .map_err(|err| Error::Environment(format!("could not locate the running binary: {err}")))?;
    let dir = auth::config_dir()?;
    let owner = owner_of(
        &binary,
        receipt::load(&dir).unwrap_or(None),
        cargo_home().as_deref(),
    );

    if !matches!(owner, Owner::Installed) {
        return decline(&binary, &owner, &dir, options);
    }

    if !confirm(&binary, &dir, options)? {
        println!("Nothing was removed.");
        return Ok(false);
    }

    // Credentials go first. If removing the binary fails afterwards the user
    // still has a working CLI and merely has to sign in again; the reverse
    // leaves a token behind with nothing left to clear it.
    let mut removed: Vec<String> = Vec::new();
    let mut left: Vec<String> = Vec::new();

    if options.keep_credentials {
        left.push(format!(
            "{} (--keep-credentials)",
            dir.join("credentials.json").display()
        ));
    } else {
        match auth::sign_out()? {
            SignOut::NothingStored => {}
            SignOut::Revoked => {
                removed.push(format!(
                    "{} (session ended)",
                    dir.join("credentials.json").display()
                ));
            }
            SignOut::LocalOnly { reason } => {
                removed.push(format!("{}", dir.join("credentials.json").display()));
                left.push(format!(
                    "the session at the sign-in service — it could not be ended ({reason}), and now expires on its own"
                ));
            }
        }
    }

    if receipt::remove(&dir)? {
        removed.push(receipt::receipt_path(&dir).display().to_string());
    }

    // Unlinking a running executable is fine on Unix: the inode outlives the
    // name, so the rest of this function still runs.
    fs::remove_file(&binary).map_err(|err| {
        Error::Environment(format!("could not remove {}: {err}", binary.display()))
    })?;
    removed.push(binary.display().to_string());

    match remaining(&dir) {
        Remaining::Gone => {}
        Remaining::Empty => {
            if fs::remove_dir(&dir).is_ok() {
                removed.push(dir.display().to_string());
            }
        }
        Remaining::Files(names) => {
            left.push(format!("{} (holds {})", dir.display(), names.join(", ")));
        }
    }

    report(&removed, &left);
    Ok(true)
}

fn report(removed: &[String], left: &[String]) {
    println!("Removed:");
    for entry in removed {
        println!("  {entry}");
    }
    if !left.is_empty() {
        println!();
        println!("Left in place:");
        for entry in left {
            println!("  {entry}");
        }
    }
}

fn decline(binary: &Path, owner: &Owner, dir: &Path, options: &Options) -> Result<bool, Error> {
    match owner {
        Owner::Managed { by, command } => {
            eprintln!(
                "postflight: this copy is managed by {by}, so removing it here would leave {by} \
                 believing it is still installed.\n\n  {command}\n"
            );
        }
        _ => {
            eprintln!(
                "postflight: no install receipt describes {}, so this copy was not put here by \
                 the installer and postflight will not guess at removing it.\n\n  \
                 built from source with `make install`?  make uninstall\n  \
                 anything else?                          rm {}\n",
                binary.display(),
                binary.display()
            );
        }
    }

    // Credentials are ours whoever owns the binary, and someone uninstalling
    // wants them gone either way.
    if options.keep_credentials {
        return Ok(false);
    }
    match auth::sign_out()? {
        SignOut::NothingStored => {}
        SignOut::Revoked => {
            eprintln!(
                "postflight: removed the credentials at {} and ended the session.",
                dir.join("credentials.json").display()
            );
        }
        SignOut::LocalOnly { reason } => {
            eprintln!(
                "postflight: removed the credentials at {}. The session could not be ended \
                 ({reason}) and expires on its own.",
                dir.join("credentials.json").display()
            );
        }
    }
    Ok(false)
}

fn confirm(binary: &Path, dir: &Path, options: &Options) -> Result<bool, Error> {
    if options.assume_yes {
        return Ok(true);
    }
    // Piped in with nothing to answer the prompt: refusing beats treating
    // silence as consent.
    if !io::stdin().is_terminal() {
        return Err(Error::Environment(String::from(
            "uninstalling needs confirmation, and stdin is not a terminal — rerun with --yes",
        )));
    }
    println!("This will remove:");
    println!("  {}", binary.display());
    if !options.keep_credentials {
        println!("  {}", dir.display());
    }
    print!("Continue? [y/N] ");
    io::stdout().flush()?;
    let mut answer = String::new();
    io::stdin().read_line(&mut answer)?;
    Ok(matches!(answer.trim(), "y" | "Y" | "yes" | "Yes"))
}

enum Remaining {
    Gone,
    Empty,
    Files(Vec<String>),
}

fn remaining(dir: &Path) -> Remaining {
    let Ok(entries) = fs::read_dir(dir) else {
        return Remaining::Gone;
    };
    let names: Vec<String> = entries
        .filter_map(Result::ok)
        .map(|entry| entry.file_name().to_string_lossy().into_owned())
        .collect();
    if names.is_empty() {
        Remaining::Empty
    } else {
        Remaining::Files(names)
    }
}

fn owner_of(binary: &Path, receipt: Option<Receipt>, cargo_home: Option<&Path>) -> Owner {
    if receipt.is_some_and(|receipt| receipt::describes(&receipt, binary)) {
        return Owner::Installed;
    }
    if let Some(manager) = manager_for(binary, cargo_home) {
        return manager;
    }
    Owner::Unclaimed
}

fn manager_for(binary: &Path, cargo_home: Option<&Path>) -> Option<Owner> {
    let path = binary.to_string_lossy();
    if path.contains("/Cellar/") || path.contains("/homebrew/") || path.contains("/linuxbrew/") {
        return Some(Owner::Managed {
            by: "Homebrew",
            command: String::from("brew uninstall postflight"),
        });
    }
    if path.contains("/node_modules/") {
        return Some(Owner::Managed {
            by: "npm",
            command: String::from("npm uninstall -g @guardian-intelligence/postflight"),
        });
    }
    if cargo_home.is_some_and(|home| binary.starts_with(home.join("bin"))) {
        return Some(Owner::Managed {
            by: "cargo",
            command: String::from("cargo uninstall postflight"),
        });
    }
    None
}

/// Read rather than mutated in tests: the process environment is shared by
/// every test thread, so ownership rules are decided from a value passed in.
fn cargo_home() -> Option<PathBuf> {
    env::var_os("CARGO_HOME")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .or_else(|| {
            env::var_os("HOME")
                .filter(|v| !v.is_empty())
                .map(|home| PathBuf::from(home).join(".cargo"))
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::receipt::SCHEMA;

    fn receipt_for(path: &Path) -> Receipt {
        Receipt {
            schema: SCHEMA,
            method: String::from("install.sh"),
            binary_path: path.to_string_lossy().into_owned(),
            channel: None,
            tag: None,
            version: None,
            target: None,
            binary_sha256: None,
            installed_at: None,
        }
    }

    #[test]
    fn a_matching_receipt_claims_the_binary() {
        let dir = std::env::temp_dir().join(format!("postflight-owner-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let binary = dir.join("postflight");
        fs::write(&binary, b"binary").unwrap();

        assert!(matches!(
            owner_of(&binary, Some(receipt_for(&binary)), None),
            Owner::Installed
        ));
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn package_manager_paths_are_refused() {
        for (path, by) in [
            ("/opt/homebrew/bin/postflight", "Homebrew"),
            (
                "/usr/local/Cellar/postflight/0.2.0/bin/postflight",
                "Homebrew",
            ),
            (
                "/home/x/.nvm/versions/node/v22/lib/node_modules/@guardian-intelligence/postflight/bin/postflight",
                "npm",
            ),
        ] {
            match owner_of(Path::new(path), None, None) {
                Owner::Managed { by: found, .. } => assert_eq!(found, by, "for {path}"),
                _ => panic!("{path} should be recognised as {by}-owned"),
            }
        }
    }

    #[test]
    fn cargo_bin_is_refused() {
        let home = PathBuf::from("/home/x/.cargo");
        match owner_of(&home.join("bin").join("postflight"), None, Some(&home)) {
            Owner::Managed { by, .. } => assert_eq!(by, "cargo"),
            _ => panic!("a binary under CARGO_HOME/bin should be cargo-owned"),
        }
        assert!(matches!(
            owner_of(Path::new("/usr/local/bin/postflight"), None, Some(&home)),
            Owner::Unclaimed
        ));
    }

    #[test]
    fn a_receipt_for_another_path_does_not_claim_this_one() {
        let dir = std::env::temp_dir().join(format!("postflight-owner2-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let binary = dir.join("postflight");
        let other = dir.join("somewhere-else");
        fs::write(&binary, b"binary").unwrap();
        fs::write(&other, b"binary").unwrap();

        assert!(matches!(
            owner_of(&binary, Some(receipt_for(&other)), None),
            Owner::Unclaimed
        ));
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn remaining_distinguishes_gone_empty_and_occupied() {
        let dir = std::env::temp_dir().join(format!("postflight-remaining-{}", std::process::id()));
        fs::remove_dir_all(&dir).ok();
        assert!(matches!(remaining(&dir), Remaining::Gone));
        fs::create_dir_all(&dir).unwrap();
        assert!(matches!(remaining(&dir), Remaining::Empty));
        fs::write(dir.join("something"), b"x").unwrap();
        match remaining(&dir) {
            Remaining::Files(names) => assert_eq!(names, vec![String::from("something")]),
            _ => panic!("a directory with a file in it is occupied"),
        }
        fs::remove_dir_all(&dir).ok();
    }
}
