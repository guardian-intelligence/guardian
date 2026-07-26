//! `postflight self uninstall`.

use std::fs;
use std::io::{self, IsTerminal, Write};
use std::path::Path;

use crate::auth::{self, SignOut};
use crate::error::Error;
use crate::receipt;
use crate::scope::{self, Owner};

pub struct Options {
    pub keep_credentials: bool,
    pub assume_yes: bool,
}

pub fn run(options: &Options) -> Result<bool, Error> {
    let binary = scope::running_binary()?;
    let dir = auth::config_dir()?;
    let owner = scope::owner_of(
        &binary,
        receipt::load(&dir).unwrap_or(None),
        scope::cargo_home().as_deref(),
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
        Owner::Managed(manager) => match manager.uninstall_command() {
            Some(command) => {
                let by = manager.name();
                eprintln!(
                    "postflight: this copy is managed by {by}, so removing it here would leave {by} \
                     believing it is still installed.\n\n  {command}\n"
                );
            }
            // npx unpacked a package to run the CLI once. There is no install
            // to undo, and pointing someone at the cache would be advice to
            // delete something npm expires on its own.
            None => eprintln!(
                "postflight: this copy is a package npx unpacked to run the CLI once — nothing \
                 was installed here, so there is nothing to remove.\n"
            ),
        },
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::scratch_dir;

    #[test]
    fn remaining_distinguishes_gone_empty_and_occupied() {
        let dir = scratch_dir("uninstall-remaining");
        assert!(matches!(remaining(&dir), Remaining::Gone));
        fs::create_dir_all(&*dir).unwrap();
        assert!(matches!(remaining(&dir), Remaining::Empty));
        fs::write(dir.join("something"), b"x").unwrap();
        match remaining(&dir) {
            Remaining::Files(names) => assert_eq!(names, vec![String::from("something")]),
            _ => panic!("a directory with a file in it is occupied"),
        }
    }
}
