//! Which copy of the CLI is running, and who is entitled to change it.
//!
//! `self update` and `self uninstall` both have to answer this before they
//! touch anything, and they have to answer it the same way. One place, one
//! answer: a second copy of these rules is how an updater ends up quietly
//! doing nothing to a Homebrew install while its own notice keeps
//! recommending the command that will never work.

use std::env;
use std::fs;
use std::path::{Path, PathBuf};

use crate::error::Error;
use crate::receipt::{self, Receipt};

/// A package manager that installs the CLI and keeps its own record of having
/// done so.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Manager {
    Homebrew,
    Npm,
    Pnpm,
    Bun,
    Yarn,
    /// Not an installation. `npx` unpacks a package into a cache and runs it
    /// from there, so there is nothing to upgrade and nothing to remove.
    Npx,
    Cargo,
}

impl Manager {
    pub const fn name(self) -> &'static str {
        match self {
            Manager::Homebrew => "Homebrew",
            Manager::Npm => "npm",
            Manager::Pnpm => "pnpm",
            Manager::Bun => "bun",
            Manager::Yarn => "Yarn",
            Manager::Npx => "npx",
            Manager::Cargo => "cargo",
        }
    }

    /// The command that removes this copy.
    pub const fn uninstall_command(self) -> Option<&'static str> {
        match self {
            Manager::Homebrew => Some("brew uninstall postflight"),
            Manager::Npm => Some("npm uninstall -g @guardian-intelligence/postflight"),
            Manager::Pnpm => Some("pnpm remove -g @guardian-intelligence/postflight"),
            Manager::Bun => Some("bun remove -g @guardian-intelligence/postflight"),
            Manager::Yarn => Some("yarn global remove @guardian-intelligence/postflight"),
            Manager::Npx => None,
            Manager::Cargo => Some("cargo uninstall postflight"),
        }
    }
}

/// Who is entitled to change a copy of the binary.
#[derive(Debug)]
pub enum Owner {
    /// The installer put it here and left a receipt saying so.
    Installed,
    /// A package manager owns the file. Replacing or deleting it behind the
    /// manager's back desyncs its manifest and leaves the user worse off than
    /// before they asked.
    Managed(Manager),
    /// No receipt, no recognisable manager. Could be `make install`, a copy,
    /// or a build output — none of which are ours to guess at.
    Unclaimed,
}

/// The path of the running binary, with the two ways it lies corrected.
///
/// Linux answers `/proc/self/exe`, which for an unlinked binary reads
/// `<path> (deleted)`; Rust neither strips that nor errors
/// (rust-lang/rust#69343), and it is exactly what replacing a binary in place
/// leaves for the process that did the replacing. macOS answers
/// `_NSGetExecutablePath`, which per `dyld(3)` may hand back a symbolic link
/// rather than the real file — so a Homebrew install on an Intel Mac arrives
/// as `/usr/local/bin/postflight` and looks like nothing in particular until
/// it is resolved.
pub fn running_binary() -> Result<PathBuf, Error> {
    let raw = env::current_exe()
        .map_err(|err| Error::Environment(format!("could not locate the running binary: {err}")))?;
    Ok(resolve(&undeleted(raw)))
}

/// Strips the marker Linux appends to the name of an unlinked executable, and
/// only when nothing is there under the full name: a file may legitimately be
/// called that.
fn undeleted(path: PathBuf) -> PathBuf {
    const DELETED: &str = " (deleted)";
    if path.exists() {
        return path;
    }
    match path.to_str().and_then(|text| text.strip_suffix(DELETED)) {
        Some(trimmed) => PathBuf::from(trimmed),
        None => path,
    }
}

/// Symlinks resolved where the filesystem allows it. A path that cannot be
/// resolved — already unlinked, or behind a directory we cannot traverse — is
/// still the best evidence available, so it comes back as it went in.
pub fn resolve(path: &Path) -> PathBuf {
    fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
}

/// `cargo_home` is passed in rather than read here: the process environment is
/// shared by every test thread, so ownership rules are decided from a value the
/// caller supplies.
pub fn owner_of(binary: &Path, receipt: Option<Receipt>, cargo_home: Option<&Path>) -> Owner {
    // A manager's claim outranks our own receipt. The two can only disagree
    // when a manager has installed over a path our installer once owned, and
    // in that case the file is now theirs — acting on the stale receipt would
    // delete a file some other manifest still lists.
    if let Some(manager) = manager_for(binary, cargo_home) {
        return Owner::Managed(manager);
    }
    match receipt {
        Some(receipt) if receipt::describes(&receipt, binary) => Owner::Installed,
        _ => Owner::Unclaimed,
    }
}

fn manager_for(binary: &Path, cargo_home: Option<&Path>) -> Option<Manager> {
    if keg_of(binary).is_some() {
        return Some(Manager::Homebrew);
    }
    if let Some(manager) = node_manager(binary) {
        return Some(manager);
    }
    if cargo_home.is_some_and(|home| binary.starts_with(home.join("bin"))) {
        return Some(Manager::Cargo);
    }
    None
}

/// The keg a path lives in — `<prefix>/Cellar/<formula>/<version>`.
///
/// Matching the keg rather than the prefix is what makes this work on an Intel
/// Mac, where the prefix is the thoroughly unremarkable `/usr/local`, and on a
/// relocated or Linuxbrew prefix, which share nothing else. Every Homebrew
/// install lives in a keg and is linked into `<prefix>/bin` from there, so a
/// resolved path always passes through one.
fn keg_of(binary: &Path) -> Option<PathBuf> {
    let components: Vec<_> = binary.components().collect();
    let cellar = components
        .iter()
        .position(|component| component.as_os_str() == "Cellar")?;
    let keg = components.get(..cellar + 3)?;
    Some(keg.iter().collect())
}

/// Which Node package manager put a copy under a `node_modules`. They all
/// produce the same directory name and take different removal commands, and
/// `npx` produces one for a package that was never installed at all.
fn node_manager(binary: &Path) -> Option<Manager> {
    if !has_component(binary, "node_modules") {
        return None;
    }
    // Checked before the rest: an npx cache sits under the npm home, so the
    // markers below would otherwise claim it.
    if has_component(binary, "_npx") {
        return Some(Manager::Npx);
    }
    for (marker, manager) in [
        (".bun", Manager::Bun),
        (".pnpm", Manager::Pnpm),
        ("pnpm", Manager::Pnpm),
        (".yarn", Manager::Yarn),
        ("yarn", Manager::Yarn),
    ] {
        if has_component(binary, marker) {
            return Some(manager);
        }
    }
    Some(Manager::Npm)
}

/// Whole path components, never substrings: `~/src/homebrew/postflight` is
/// somebody's checkout, not a Homebrew prefix.
fn has_component(path: &Path, name: &str) -> bool {
    path.components()
        .any(|component| component.as_os_str() == name)
}

/// Read rather than mutated in tests, for the same reason `owner_of` takes it
/// as an argument.
pub fn cargo_home() -> Option<PathBuf> {
    env::var_os("CARGO_HOME")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
        .or_else(|| {
            env::var_os("HOME")
                .filter(|value| !value.is_empty())
                .map(|home| PathBuf::from(home).join(".cargo"))
        })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::receipt::SCHEMA;
    use crate::testing::{ScratchDir, scratch_dir};

    fn scratch(name: &str) -> ScratchDir {
        let dir = scratch_dir(name);
        fs::create_dir_all(&*dir).unwrap();
        dir
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

    #[test]
    fn a_matching_receipt_claims_the_binary() {
        let dir = scratch("scope-installed");
        let binary = dir.join("postflight");
        fs::write(&binary, b"binary").unwrap();

        match owner_of(&binary, Some(receipt_for(&binary)), None) {
            Owner::Installed => {}
            other => panic!("a receipt naming this path should claim it, got {other:?}"),
        }
    }

    #[test]
    fn a_receipt_for_another_path_claims_nothing() {
        let dir = scratch("scope-elsewhere");
        let binary = dir.join("postflight");
        let other = dir.join("somewhere-else");
        fs::write(&binary, b"binary").unwrap();
        fs::write(&other, b"binary").unwrap();

        assert!(matches!(
            owner_of(&binary, Some(receipt_for(&other)), None),
            Owner::Unclaimed
        ));
    }

    /// The Intel-Mac case: the prefix is `/usr/local`, so nothing about the
    /// linked path says Homebrew and only the keg behind it does. macOS does
    /// not resolve that symlink for us, which is why `running_binary` must.
    #[test]
    fn homebrew_is_recognised_by_its_keg_on_every_prefix() {
        for path in [
            "/usr/local/Cellar/postflight/0.2.0/bin/postflight",
            "/opt/homebrew/Cellar/postflight/0.2.0/bin/postflight",
            "/home/linuxbrew/.linuxbrew/Cellar/postflight/0.2.0/bin/postflight",
            "/mnt/relocated/brew/Cellar/postflight/0.2.0/bin/postflight",
        ] {
            match owner_of(Path::new(path), None, None) {
                Owner::Managed(Manager::Homebrew) => {}
                other => panic!("{path} should be Homebrew-owned, got {other:?}"),
            }
        }
    }

    #[test]
    fn a_checkout_named_homebrew_is_not_homebrew() {
        assert!(matches!(
            owner_of(Path::new("/home/x/src/homebrew/postflight"), None, None),
            Owner::Unclaimed
        ));
    }

    #[test]
    fn node_managers_are_told_apart() {
        for (path, expected) in [
            (
                "/home/x/.nvm/versions/node/v22/lib/node_modules/@guardian-intelligence/postflight-linux-x64/bin/postflight",
                Manager::Npm,
            ),
            (
                "/usr/local/lib/node_modules/@guardian-intelligence/postflight/bin/postflight",
                Manager::Npm,
            ),
            (
                "/home/x/.bun/install/global/node_modules/@guardian-intelligence/postflight-linux-x64/bin/postflight",
                Manager::Bun,
            ),
            (
                "/home/x/.local/share/pnpm/global/5/node_modules/@guardian-intelligence/postflight/bin/postflight",
                Manager::Pnpm,
            ),
            (
                "/home/x/.config/yarn/global/node_modules/@guardian-intelligence/postflight/bin/postflight",
                Manager::Yarn,
            ),
            (
                "/home/x/.npm/_npx/a1b2c3d4e5f60718/node_modules/@guardian-intelligence/postflight/bin/postflight",
                Manager::Npx,
            ),
        ] {
            match owner_of(Path::new(path), None, None) {
                Owner::Managed(found) if found == expected => {}
                other => panic!("{path} should be {expected:?}-owned, got {other:?}"),
            }
        }
    }

    /// npx installs nothing, so there is no command to offer for either verb —
    /// and offering one would be advice to remove a cache npm expires itself.
    #[test]
    fn npx_has_no_commands_and_every_other_manager_has_both() {
        assert_eq!(Manager::Npx.uninstall_command(), None);
        for manager in [
            Manager::Homebrew,
            Manager::Npm,
            Manager::Pnpm,
            Manager::Bun,
            Manager::Yarn,
            Manager::Cargo,
        ] {
            assert!(
                manager.uninstall_command().is_some(),
                "{} should name the command that removes it",
                manager.name()
            );
        }
    }

    #[test]
    fn cargo_bin_is_recognised_and_nothing_else_under_home_is() {
        let home = PathBuf::from("/home/x/.cargo");
        match owner_of(&home.join("bin").join("postflight"), None, Some(&home)) {
            Owner::Managed(Manager::Cargo) => {}
            other => panic!("a binary under CARGO_HOME/bin should be cargo-owned, got {other:?}"),
        }
        assert!(matches!(
            owner_of(Path::new("/usr/local/bin/postflight"), None, Some(&home)),
            Owner::Unclaimed
        ));
    }

    /// A manager's claim has to outrank a receipt, or a stale receipt from an
    /// earlier install at the same path authorises deleting the file that
    /// replaced it.
    #[test]
    fn a_manager_outranks_a_stale_receipt() {
        let path = Path::new("/opt/homebrew/Cellar/postflight/0.2.0/bin/postflight");
        assert!(matches!(
            owner_of(path, Some(receipt_for(path)), None),
            Owner::Managed(Manager::Homebrew)
        ));
    }

    #[test]
    fn the_deleted_marker_is_stripped_only_when_nothing_is_there() {
        let dir = scratch("scope-deleted");

        let gone = dir.join("postflight (deleted)");
        assert_eq!(undeleted(gone), dir.join("postflight"));

        // A file genuinely named that keeps its name.
        let real = dir.join("odd (deleted)");
        fs::write(&real, b"binary").unwrap();
        assert_eq!(undeleted(real.clone()), real);
    }

    #[test]
    fn a_path_that_cannot_be_resolved_survives_resolution() {
        let missing = Path::new("/nonexistent/postflight");
        assert_eq!(resolve(missing), missing);
    }
}
