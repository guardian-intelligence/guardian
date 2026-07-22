use std::env;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::time::Duration;

use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use serde::{Deserialize, Serialize};

use crate::device::{DeviceFlow, TokenSet};
use crate::error::Error;

pub struct LoginOptions {
    pub issuer: String,
    pub client_id: String,
    pub device_url: String,
}

pub fn login(options: &LoginOptions) -> Result<(), Error> {
    let agent = http_agent();
    let issuer = options.issuer.trim_end_matches('/');
    let flow = DeviceFlow::new(&agent, issuer, &options.client_id);
    let authorization = flow.start()?;

    // Deliberately NOT the issuer's verification_uri: the approval page is
    // ours to render and ours to enforce device-flow policy on, and keeping
    // the printed URL constant lets the server side evolve underneath every
    // CLI binary already in the wild.
    println!(
        "First, copy your one-time code: {}",
        authorization.user_code
    );
    println!();
    println!("Then approve this sign-in at: {}", options.device_url);
    println!();
    println!(
        "Waiting for approval... (this request expires in {} minutes)",
        authorization.expires_in / 60
    );

    let tokens = flow.wait_for_approval(&authorization, &mut std::thread::sleep)?;
    store_credentials(&config_dir()?, &StoredCredentials::from(&tokens))?;
    match username_for(tokens.id_token.as_deref())? {
        Some(username) => println!("Signed in as {username}."),
        None => println!("Signed in."),
    }
    Ok(())
}

pub fn status() -> Result<bool, Error> {
    match load_credentials(&config_dir()?)? {
        None => {
            println!("Not signed in. Run `postflight auth login`.");
            Ok(false)
        }
        Some(credentials) => {
            match username_for(credentials.id_token.as_deref())? {
                Some(username) => println!("Signed in as {username}."),
                None => println!("Signed in."),
            }
            Ok(true)
        }
    }
}

pub fn logout() -> Result<(), Error> {
    if clear_credentials(&config_dir()?)? {
        println!("Signed out.");
    } else {
        println!("No stored credentials.");
    }
    Ok(())
}

fn http_agent() -> ureq::Agent {
    ureq::Agent::new_with_config(
        ureq::Agent::config_builder()
            .http_status_as_error(false)
            .timeout_global(Some(Duration::from_secs(30)))
            .build(),
    )
}

fn username_for(id_token: Option<&str>) -> Result<Option<String>, Error> {
    id_token
        .map(preferred_username)
        .transpose()
        .map(Option::flatten)
}

/// Claims are read for display only: the token arrived over TLS directly
/// from the issuer, so local signature verification adds nothing here.
pub fn preferred_username(id_token: &str) -> Result<Option<String>, Error> {
    let payload = id_token
        .split('.')
        .nth(1)
        .ok_or_else(|| Error::Claims(String::from("identity token is not a JWT")))?;
    let bytes = URL_SAFE_NO_PAD
        .decode(payload)
        .map_err(|err| Error::Claims(err.to_string()))?;
    let claims: serde_json::Value =
        serde_json::from_slice(&bytes).map_err(|err| Error::Claims(err.to_string()))?;
    Ok(claims
        .get("preferred_username")
        .and_then(serde_json::Value::as_str)
        .map(ToOwned::to_owned))
}

// Field names are fixed by the OAuth token-response wire format.
#[allow(clippy::struct_field_names)]
#[derive(Debug, Serialize, Deserialize)]
pub struct StoredCredentials {
    pub access_token: String,
    #[serde(default)]
    pub refresh_token: Option<String>,
    #[serde(default)]
    pub id_token: Option<String>,
}

impl From<&TokenSet> for StoredCredentials {
    fn from(tokens: &TokenSet) -> Self {
        Self {
            access_token: tokens.access_token.clone(),
            refresh_token: tokens.refresh_token.clone(),
            id_token: tokens.id_token.clone(),
        }
    }
}

pub fn config_dir() -> Result<PathBuf, Error> {
    if let Some(dir) = env::var_os("XDG_CONFIG_HOME").filter(|v| !v.is_empty()) {
        return Ok(PathBuf::from(dir).join("postflight"));
    }
    let home = env::var_os("HOME")
        .filter(|v| !v.is_empty())
        .ok_or_else(|| {
            Error::Environment(String::from("neither XDG_CONFIG_HOME nor HOME is set"))
        })?;
    Ok(PathBuf::from(home).join(".config").join("postflight"))
}

fn credentials_path(dir: &Path) -> PathBuf {
    dir.join("credentials.json")
}

pub fn store_credentials(dir: &Path, credentials: &StoredCredentials) -> Result<(), Error> {
    fs::create_dir_all(dir)?;
    let payload = serde_json::to_vec_pretty(credentials).map_err(std::io::Error::other)?;
    let mut open_options = fs::OpenOptions::new();
    open_options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        open_options.mode(0o600);
    }
    let mut file = open_options.open(credentials_path(dir))?;
    file.write_all(&payload)?;
    Ok(())
}

pub fn load_credentials(dir: &Path) -> Result<Option<StoredCredentials>, Error> {
    let bytes = match fs::read(credentials_path(dir)) {
        Ok(bytes) => bytes,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(err.into()),
    };
    let credentials = serde_json::from_slice(&bytes).map_err(std::io::Error::other)?;
    Ok(Some(credentials))
}

pub fn clear_credentials(dir: &Path) -> Result<bool, Error> {
    match fs::remove_file(credentials_path(dir)) {
        Ok(()) => Ok(true),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(err) => Err(err.into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn id_token_with_claims(claims: &str) -> String {
        let header = URL_SAFE_NO_PAD.encode(r#"{"alg":"RS256"}"#);
        let payload = URL_SAFE_NO_PAD.encode(claims);
        format!("{header}.{payload}.signature")
    }

    #[test]
    fn preferred_username_reads_the_claim() {
        let token = id_token_with_claims(r#"{"preferred_username": "canary-01"}"#);
        assert_eq!(
            preferred_username(&token).unwrap().as_deref(),
            Some("canary-01")
        );
    }

    #[test]
    fn preferred_username_tolerates_missing_claim() {
        let token = id_token_with_claims(r#"{"sub": "abc"}"#);
        assert_eq!(preferred_username(&token).unwrap(), None);
    }

    #[test]
    fn preferred_username_rejects_garbage() {
        assert!(matches!(
            preferred_username("not-a-jwt"),
            Err(Error::Claims(_))
        ));
        assert!(matches!(
            preferred_username("a.!!!.c"),
            Err(Error::Claims(_))
        ));
    }

    #[test]
    fn credentials_roundtrip_and_clear() {
        let dir = std::env::temp_dir().join(format!(
            "postflight-cli-test-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let stored = StoredCredentials {
            access_token: String::from("at-1"),
            refresh_token: Some(String::from("rt-1")),
            id_token: None,
        };

        assert!(load_credentials(&dir).unwrap().is_none());
        store_credentials(&dir, &stored).unwrap();
        let loaded = load_credentials(&dir)
            .unwrap()
            .expect("credentials should load");
        assert_eq!(loaded.access_token, "at-1");
        assert_eq!(loaded.refresh_token.as_deref(), Some("rt-1"));

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(credentials_path(&dir))
                .unwrap()
                .permissions()
                .mode();
            assert_eq!(mode & 0o777, 0o600, "credentials must be user-only");
        }

        assert!(clear_credentials(&dir).unwrap());
        assert!(!clear_credentials(&dir).unwrap());
        fs::remove_dir_all(&dir).ok();
    }
}
