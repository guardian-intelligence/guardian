//! `postflight self uninstall`.
//!
//! Removes everything this CLI put on the machine, and reports everything it
//! did not. What it will not do is delete a file another package manager still
//! lists: strip a Homebrew keg and `brew list` goes on naming a formula whose
//! every symlink dangles; remove an npm package directory and the link in
//! `{prefix}/bin` survives indefinitely, with no npm command that notices it or
//! repairs it. Printing the owning manager's command instead is what every CLI
//! shipping through more than one channel has settled on.

use std::env;
use std::fs;
use std::io::{self, IsTerminal, Write};
use std::path::{Path, PathBuf};

use crate::auth::{self, SignOut};
use crate::error::Error;
use crate::receipt;
use crate::scope::{self, Installation, Owner};

pub struct Options {
    pub keep_credentials: bool,
    pub assume_yes: bool,
}

pub fn run(options: &Options) -> Result<bool, Error> {
    let dir = auth::config_dir()?;
    let receipt = receipt::load(&dir).unwrap_or(None);
    let cargo_home = scope::cargo_home();
    let home = env::var_os("HOME").map(PathBuf::from);

    // A failure to locate the running binary is not fatal here: the sweep has
    // other ways to find installations, and refusing to uninstall because the
    // OS would not name our own path would be a strange place to stop.
    let running = scope::running_binary().ok();
    let (ours, theirs): (Vec<_>, Vec<_>) = scope::installations(
        running.as_deref(),
        env::var_os("PATH").as_deref(),
        home.as_deref(),
        cargo_home.as_deref(),
        receipt.as_ref(),
    )
    .into_iter()
    .partition(|install| matches!(install.owner, Owner::Installed));

    if ours.is_empty() {
        return decline(&theirs, &dir, options);
    }
    if !confirm(&ours, &dir, options)? {
        println!("Nothing was removed.");
        return Ok(false);
    }

    // Credentials go first. If removing a binary fails afterwards the user
    // still has a working CLI and merely has to sign in again; the reverse
    // leaves a token behind with nothing left to clear it.
    let mut removed: Vec<String> = Vec::new();
    let mut left: Vec<String> = Vec::new();
    let mut kept_a_binary = false;
    clear_credentials(&dir, options, &mut removed, &mut left)?;

    if receipt::remove(&dir)? {
        removed.push(receipt::receipt_path(&dir).display().to_string());
    }

    for install in &ours {
        // Unlinking a running executable is fine on Unix: the inode outlives
        // the name, so the rest of this function still runs.
        match fs::remove_file(&install.path) {
            Ok(()) => removed.push(install.path.display().to_string()),
            Err(err) => {
                kept_a_binary = true;
                left.push(format!("{} ({err})", install.path.display()));
            }
        }
    }

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
    elsewhere(&theirs);
    // A binary that would not go means the thing that was asked for did not
    // happen, whatever else went right.
    Ok(!kept_a_binary)
}

fn clear_credentials(
    dir: &Path,
    options: &Options,
    removed: &mut Vec<String>,
    left: &mut Vec<String>,
) -> Result<(), Error> {
    let credentials = dir.join("credentials.json");
    if options.keep_credentials {
        left.push(format!("{} (--keep-credentials)", credentials.display()));
        return Ok(());
    }
    match auth::sign_out()? {
        SignOut::NothingStored => {}
        SignOut::Revoked => removed.push(format!("{} (session ended)", credentials.display())),
        SignOut::LocalOnly { reason } => {
            removed.push(credentials.display().to_string());
            left.push(format!(
                "the session at the sign-in service — it could not be ended ({reason}), and now expires on its own"
            ));
        }
    }
    Ok(())
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

/// What each copy is, and the command that removes it.
fn describe(install: &Installation, indent: &str) -> String {
    match install.owner {
        Owner::Managed(manager) => match manager.uninstall_command() {
            Some(command) => format!(
                "{indent}{} — installed by {}\n{indent}    {command}",
                install.path.display(),
                manager.name()
            ),
            // npx unpacked a package to run the CLI once. There is no install
            // to undo, and naming the cache would be advice to delete something
            // npm expires on its own.
            None => format!(
                "{indent}{} — unpacked by npx to run the CLI once, not installed",
                install.path.display()
            ),
        },
        _ => format!(
            "{indent}{} — no install receipt describes it\n{indent}    \
             built from source with `make install`?  make uninstall\n{indent}    \
             anything else?                          rm {}",
            install.path.display(),
            install.path.display()
        ),
    }
}

/// The copies this run was not entitled to touch.
///
/// Reported even when everything else succeeded: a second copy still ahead on
/// `PATH` after an uninstall is how someone ends up certain the CLI is still
/// installed — because from where they are standing, it is.
fn elsewhere(theirs: &[Installation]) {
    if theirs.is_empty() {
        return;
    }
    println!();
    println!("Still on this machine, installed by something else:");
    for install in theirs {
        println!("{}", describe(install, "  "));
    }
}

fn decline(theirs: &[Installation], dir: &Path, options: &Options) -> Result<bool, Error> {
    if theirs.is_empty() {
        eprintln!("postflight: no installation of postflight was found on this machine.");
    } else {
        eprintln!(
            "postflight: nothing here was put in place by the postflight installer, so there is \
             nothing for it to remove. What is here, and what removes it:\n"
        );
        for install in theirs {
            eprintln!("{}", describe(install, "  "));
        }
        eprintln!();
    }

    // Credentials are ours whoever owns the binary, and someone uninstalling
    // wants them gone either way.
    if options.keep_credentials {
        return Ok(false);
    }
    let credentials = dir.join("credentials.json");
    match auth::sign_out()? {
        SignOut::NothingStored => {}
        SignOut::Revoked => eprintln!(
            "postflight: removed the credentials at {} and ended the session.",
            credentials.display()
        ),
        SignOut::LocalOnly { reason } => eprintln!(
            "postflight: removed the credentials at {}. The session could not be ended \
             ({reason}) and expires on its own.",
            credentials.display()
        ),
    }
    Ok(false)
}

fn confirm(ours: &[Installation], dir: &Path, options: &Options) -> Result<bool, Error> {
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
    for install in ours {
        println!("  {}", install.path.display());
    }
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
    use crate::receipt::{Receipt, SCHEMA};
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

    fn describes_a(install: &Installation) -> String {
        describe(install, "")
    }

    #[test]
    fn every_copy_names_the_command_that_removes_it() {
        for (path, expected) in [
            (
                "/opt/homebrew/Cellar/postflight/0.2.0/bin/postflight",
                "brew uninstall postflight",
            ),
            (
                "/usr/lib/node_modules/@guardian-intelligence/postflight/bin/postflight",
                "npm uninstall -g @guardian-intelligence/postflight",
            ),
            (
                "/home/x/.bun/install/global/node_modules/@guardian-intelligence/postflight/bin/postflight",
                "bun remove -g @guardian-intelligence/postflight",
            ),
            ("/opt/elsewhere/postflight", "rm /opt/elsewhere/postflight"),
        ] {
            let install = Installation {
                path: PathBuf::from(path),
                owner: scope::owner_of(Path::new(path), None, None),
            };
            let described = describes_a(&install);
            assert!(
                described.contains(expected),
                "{path} should be reported with `{expected}`, got:\n{described}"
            );
        }
    }

    /// npx leaves nothing behind, so naming a command would be advice to delete
    /// a cache npm manages itself.
    #[test]
    fn an_npx_run_is_reported_without_a_command() {
        let path = "/home/x/.npm/_npx/a1b2c3d4e5f60718/node_modules/@guardian-intelligence/postflight/bin/postflight";
        let install = Installation {
            path: PathBuf::from(path),
            owner: scope::owner_of(Path::new(path), None, None),
        };
        let described = describes_a(&install);
        assert!(described.contains("not installed"), "got:\n{described}");
        assert!(!described.contains("rm "), "got:\n{described}");
    }

    /// The point of the sweep: the copy the receipt names is ours, a Homebrew
    /// copy shadowing it on PATH is not, and both have to be seen. Homebrew's
    /// bin comes first, so a search that stopped at the first hit would answer
    /// about the wrong one.
    #[test]
    fn the_sweep_separates_ours_from_a_copy_shadowing_it() {
        let dir = scratch_dir("uninstall-sweep");
        let mine = dir.join("local").join("bin");
        let keg = dir
            .join("brew")
            .join("Cellar")
            .join("postflight")
            .join("0.2.0")
            .join("bin");
        let brew_bin = dir.join("brew").join("bin");
        for path in [&mine, &keg, &brew_bin] {
            fs::create_dir_all(path).unwrap();
        }
        fs::write(mine.join("postflight"), b"ours").unwrap();
        fs::write(keg.join("postflight"), b"brewed").unwrap();
        std::os::unix::fs::symlink(keg.join("postflight"), brew_bin.join("postflight")).unwrap();

        let receipt = receipt_for(&mine.join("postflight"));
        let path_var = env::join_paths([&brew_bin, &mine]).unwrap();
        let found = scope::installations(None, Some(&path_var), None, None, Some(&receipt));

        assert_eq!(found.len(), 2, "both copies should be found: {found:?}");
        let brewed = found
            .iter()
            .find(|install| install.path.starts_with(&brew_bin))
            .expect("the Homebrew copy should be found");
        assert!(matches!(
            brewed.owner,
            Owner::Managed(scope::Manager::Homebrew)
        ));
        let ours = found
            .iter()
            .find(|install| install.path.starts_with(&mine))
            .expect("our copy should be found");
        assert!(matches!(ours.owner, Owner::Installed));
    }

    /// One file reached by two names is one installation. Counting it twice
    /// would report a duplicate that does not exist and offer to remove the
    /// same inode again — which is what a Homebrew keg and its `bin` link are.
    #[test]
    fn one_file_under_two_names_is_one_installation() {
        let dir = scratch_dir("uninstall-dedupe");
        let real = dir.join("real");
        let link = dir.join("link");
        for path in [&real, &link] {
            fs::create_dir_all(path).unwrap();
        }
        fs::write(real.join("postflight"), b"ours").unwrap();
        std::os::unix::fs::symlink(real.join("postflight"), link.join("postflight")).unwrap();

        let path_var = env::join_paths([&real, &link]).unwrap();
        let found = scope::installations(None, Some(&path_var), None, None, None);
        assert_eq!(found.len(), 1, "a symlink to a copy is not a second copy");
    }

    /// A copy in a directory the user has since dropped from PATH is invisible
    /// to `which` and still very much installed.
    #[test]
    fn a_copy_off_the_path_is_still_found() {
        let dir = scratch_dir("uninstall-offpath");
        let bin = dir.join(".local").join("bin");
        fs::create_dir_all(&bin).unwrap();
        fs::write(bin.join("postflight"), b"ours").unwrap();

        let found = scope::installations(None, None, Some(&dir), None, None);
        assert_eq!(found.len(), 1, "a copy in ~/.local/bin should be found");
    }
}
