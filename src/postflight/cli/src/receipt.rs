//! The record `install.sh` leaves behind describing how a copy of the binary
//! got where it is.
//!
//! Nothing inside the binary can answer that. It carries its crate version and
//! nothing else, deliberately, so identical sources rebuild byte-identically
//! and one signed artifact rides edge, nightly and rc unrebuilt — which means
//! the release tag, the channel, and the install method have to be recorded
//! beside it.

use std::fs;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::error::Error;

pub const RECEIPT_FILE: &str = "install-receipt.json";

#[derive(Debug, Serialize, Deserialize)]
pub struct Receipt {
    /// Bumped when a field changes meaning, so a newer receipt read by an
    /// older binary is ignored rather than misread.
    pub schema: u32,
    pub method: String,
    /// The file this receipt is evidence about. A second install elsewhere
    /// leaves this one in place, so it is only ever evidence about this path.
    pub binary_path: String,
    #[serde(default)]
    pub channel: Option<String>,
    #[serde(default)]
    pub tag: Option<String>,
    #[serde(default)]
    pub version: Option<String>,
    #[serde(default)]
    pub target: Option<String>,
    #[serde(default)]
    pub binary_sha256: Option<String>,
    #[serde(default)]
    pub installed_at: Option<String>,
}

pub const SCHEMA: u32 = 1;

impl Receipt {
    /// Empty strings reach here from the shell that writes the receipt, which
    /// has no way to express "absent" in a fixed JSON shape.
    pub fn field(value: Option<&String>) -> Option<&str> {
        value.map(String::as_str).filter(|v| !v.is_empty())
    }
}

pub fn receipt_path(dir: &Path) -> PathBuf {
    dir.join(RECEIPT_FILE)
}

pub fn load(dir: &Path) -> Result<Option<Receipt>, Error> {
    let bytes = match fs::read(receipt_path(dir)) {
        Ok(bytes) => bytes,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(err.into()),
    };
    let receipt: Receipt = serde_json::from_slice(&bytes).map_err(std::io::Error::other)?;
    if receipt.schema > SCHEMA {
        return Ok(None);
    }
    Ok(Some(receipt))
}

pub fn remove(dir: &Path) -> Result<bool, Error> {
    match fs::remove_file(receipt_path(dir)) {
        Ok(()) => Ok(true),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(err) => Err(err.into()),
    }
}

/// Whether this receipt is evidence about `binary`.
///
/// Symlinks are resolved on both sides: `~/.local/bin/postflight` may well be
/// a link, and a receipt that named the link while the caller asks about the
/// target describes the same installation.
pub fn describes(receipt: &Receipt, binary: &Path) -> bool {
    let recorded = Path::new(&receipt.binary_path);
    match (fs::canonicalize(recorded), fs::canonicalize(binary)) {
        (Ok(left), Ok(right)) => left == right,
        _ => recorded == binary,
    }
}

/// The receipt for the running binary, or `None` when there is no evidence
/// this copy was installed by the installer.
///
/// Never fails: a missing home directory, an unreadable file, or a receipt
/// left behind by a different installation are all simply "no provenance",
/// and `postflight version` must print something regardless.
pub fn for_current_exe() -> Option<Receipt> {
    let binary = std::env::current_exe().ok()?;
    let dir = crate::auth::config_dir().ok()?;
    let receipt = load(&dir).ok().flatten()?;
    describes(&receipt, &binary).then_some(receipt)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "postflight-receipt-{name}-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn write(dir: &Path, body: &str) {
        fs::write(receipt_path(dir), body).unwrap();
    }

    #[test]
    fn absent_receipt_is_not_an_error() {
        let dir = scratch("absent");
        assert!(load(&dir).unwrap().is_none());
        assert!(!remove(&dir).unwrap());
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn roundtrips_and_removes() {
        let dir = scratch("roundtrip");
        write(
            &dir,
            r#"{"schema":1,"method":"install.sh","binary_path":"/opt/postflight",
                "channel":"nightly","tag":"postflight-cli/nightly-20260726",
                "version":"0.3.0-pre","target":"x86_64-unknown-linux-musl",
                "binary_sha256":"abc","installed_at":"2026-07-26T05:00:00Z"}"#,
        );
        let receipt = load(&dir).unwrap().expect("receipt should load");
        assert_eq!(receipt.method, "install.sh");
        assert_eq!(
            Receipt::field(receipt.tag.as_ref()),
            Some("postflight-cli/nightly-20260726")
        );
        assert!(remove(&dir).unwrap());
        assert!(load(&dir).unwrap().is_none());
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn empty_strings_read_as_absent() {
        let dir = scratch("empty");
        write(
            &dir,
            r#"{"schema":1,"method":"install.sh","binary_path":"/opt/postflight","channel":""}"#,
        );
        let receipt = load(&dir).unwrap().unwrap();
        assert_eq!(Receipt::field(receipt.channel.as_ref()), None);
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn a_newer_schema_is_ignored_rather_than_misread() {
        let dir = scratch("schema");
        write(
            &dir,
            r#"{"schema":2,"method":"install.sh","binary_path":"/opt/postflight"}"#,
        );
        assert!(load(&dir).unwrap().is_none());
        fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn a_receipt_for_another_path_describes_nothing() {
        let dir = scratch("describes");
        let mine = dir.join("postflight");
        let theirs = dir.join("postflight-elsewhere");
        fs::write(&mine, b"binary").unwrap();
        fs::write(&theirs, b"binary").unwrap();

        let receipt = Receipt {
            schema: SCHEMA,
            method: String::from("install.sh"),
            binary_path: mine.to_string_lossy().into_owned(),
            channel: None,
            tag: None,
            version: None,
            target: None,
            binary_sha256: None,
            installed_at: None,
        };
        assert!(describes(&receipt, &mine));
        assert!(!describes(&receipt, &theirs));
        fs::remove_dir_all(&dir).ok();
    }
}
