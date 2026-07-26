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
use crate::session::Session;

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
    let credentials = StoredCredentials::new(&tokens, issuer, &options.client_id);
    store_credentials(&config_dir()?, &credentials)?;
    println!(
        "{}",
        signed_in_line(username_for(tokens.id_token.as_deref())?.as_deref())
    );
    Ok(())
}

pub fn status() -> Result<bool, Error> {
    match resolve_session(&http_agent(), &config_dir()?)? {
        SessionState::Anonymous => {
            println!("Not signed in. Run `postflight auth login`.");
            Ok(false)
        }
        SessionState::Live { username } => {
            println!("{}", signed_in_line(username.as_deref()));
            Ok(true)
        }
        SessionState::Ended => {
            println!("Your session has ended. Run `postflight auth login`.");
            Ok(false)
        }
    }
}

/// What the issuer says about the credentials on this machine.
pub enum SessionState {
    Anonymous,
    Live { username: Option<String> },
    Ended,
}

/// Ask the issuer whether the stored credentials still name a session, which is
/// the only answer worth printing: the file on disk outlives the session it was
/// minted for, so anything read out of it says what was true at sign-in, not
/// what is true now. A rejected access token is retried once behind a refresh,
/// and credentials the issuer has disowned are removed on the way out. Failing
/// to *reach* the issuer is an error that leaves the file alone — unreachable
/// is not signed out.
pub fn resolve_session(agent: &ureq::Agent, dir: &Path) -> Result<SessionState, Error> {
    let Some(credentials) = load_credentials(dir)? else {
        return Ok(SessionState::Anonymous);
    };
    let session = Session::new(agent, credentials.issuer(), credentials.client_id());
    if let Some(info) = session.user_info(&credentials.access_token)? {
        return Ok(SessionState::Live {
            username: info.preferred_username,
        });
    }

    let Some(refresh_token) = credentials.refresh_token.as_deref() else {
        clear_credentials(dir)?;
        return Ok(SessionState::Ended);
    };
    let Some(tokens) = session.refresh(refresh_token)? else {
        clear_credentials(dir)?;
        return Ok(SessionState::Ended);
    };

    // The issuer rotates the refresh token, so the set that came back is the
    // only one that will still work: it lands on disk before it is used.
    let refreshed = StoredCredentials::new(&tokens, credentials.issuer(), credentials.client_id());
    store_credentials(dir, &refreshed)?;
    match session.user_info(&refreshed.access_token)? {
        Some(info) => Ok(SessionState::Live {
            username: info.preferred_username,
        }),
        None => Err(Error::RefreshedTokenRejected),
    }
}

/// What signing out managed to accomplish.
pub enum SignOut {
    NothingStored,
    /// The session was ended at the issuer as well as locally.
    Revoked,
    /// The local copy is gone but the issuer still holds the session.
    LocalOnly {
        reason: String,
    },
}

/// Removes stored credentials, ending the session at the issuer first.
///
/// Deleting the file alone ends nothing: the session lives at the issuer until
/// it idles out, so a "signed out" machine would have its next sign-in waved
/// through against a session the user believed they had closed.
pub fn sign_out() -> Result<SignOut, Error> {
    let dir = config_dir()?;
    end_session(&http_agent(), &dir)
}

pub fn end_session(agent: &ureq::Agent, dir: &Path) -> Result<SignOut, Error> {
    let Some(credentials) = load_credentials(dir)? else {
        return Ok(SignOut::NothingStored);
    };

    let outcome = match credentials.refresh_token.as_deref() {
        Some(refresh_token) => {
            match Session::new(agent, credentials.issuer(), credentials.client_id())
                .end(refresh_token)
            {
                Ok(()) => SignOut::Revoked,
                Err(err) => SignOut::LocalOnly {
                    reason: err.to_string(),
                },
            }
        }
        // The issuer identifies the session by its refresh token, so a
        // credential without one cannot name the session it belongs to.
        None => SignOut::LocalOnly {
            reason: String::from("no refresh token was stored"),
        },
    };

    // Whatever the issuer said, the local copy goes. A revocation that could
    // not be delivered is no reason to leave a token sitting on disk.
    clear_credentials(dir)?;
    Ok(outcome)
}

pub fn logout() -> Result<(), Error> {
    match sign_out()? {
        SignOut::NothingStored => println!("No stored credentials."),
        SignOut::Revoked => println!("Signed out."),
        SignOut::LocalOnly { reason } => {
            println!("Signed out on this machine.");
            eprintln!(
                "postflight: the session could not be ended at the sign-in service ({reason}). \
                 It expires on its own."
            );
        }
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

fn signed_in_line(username: Option<&str>) -> String {
    match username {
        Some(username) => format!("Signed in as {username}."),
        None => String::from("Signed in."),
    }
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

#[derive(Debug, Serialize, Deserialize)]
pub struct StoredCredentials {
    pub access_token: String,
    #[serde(default)]
    pub refresh_token: Option<String>,
    /// Recorded so every later command reaches the issuer that minted these
    /// tokens rather than whichever one today's defaults name.
    #[serde(default)]
    pub issuer: Option<String>,
    #[serde(default)]
    pub client_id: Option<String>,
}

impl StoredCredentials {
    pub fn new(tokens: &TokenSet, issuer: &str, client_id: &str) -> Self {
        Self {
            access_token: tokens.access_token.clone(),
            refresh_token: tokens.refresh_token.clone(),
            issuer: Some(issuer.to_owned()),
            client_id: Some(client_id.to_owned()),
        }
    }

    pub fn issuer(&self) -> &str {
        Self::recorded(self.issuer.as_deref()).unwrap_or(crate::DEFAULT_ISSUER)
    }

    pub fn client_id(&self) -> &str {
        Self::recorded(self.client_id.as_deref()).unwrap_or(crate::DEFAULT_CLIENT_ID)
    }

    fn recorded(value: Option<&str>) -> Option<&str> {
        value.filter(|v| !v.is_empty())
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

/// Written through a temporary file and renamed into place: `auth status`
/// rewrites the credential every time it renews one, and a reader that caught a
/// half-written file would be told to sign in again for nothing.
pub fn store_credentials(dir: &Path, credentials: &StoredCredentials) -> Result<(), Error> {
    fs::create_dir_all(dir)?;
    let payload = serde_json::to_vec_pretty(credentials).map_err(std::io::Error::other)?;
    let staged = dir.join(format!(".credentials.json.{}", std::process::id()));
    let mut open_options = fs::OpenOptions::new();
    open_options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        open_options.mode(0o600);
    }
    let write = || -> Result<(), Error> {
        open_options.open(&staged)?.write_all(&payload)?;
        fs::rename(&staged, credentials_path(dir))?;
        Ok(())
    };
    write().inspect_err(|_| {
        fs::remove_file(&staged).ok();
    })
}

pub fn load_credentials(dir: &Path) -> Result<Option<StoredCredentials>, Error> {
    let bytes = match fs::read(credentials_path(dir)) {
        Ok(bytes) => bytes,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(err.into()),
    };
    serde_json::from_slice(&bytes)
        .map(Some)
        .map_err(|err| Error::UnreadableCredentials(err.to_string()))
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
    use crate::testing::{TestServer, agent, json_response, no_content_response, scratch_dir};

    fn id_token_with_claims(claims: &str) -> String {
        let header = URL_SAFE_NO_PAD.encode(r#"{"alg":"RS256"}"#);
        let payload = URL_SAFE_NO_PAD.encode(claims);
        format!("{header}.{payload}.signature")
    }

    fn stored(issuer: &str, access_token: &str, refresh_token: Option<&str>) -> StoredCredentials {
        StoredCredentials {
            access_token: access_token.to_owned(),
            refresh_token: refresh_token.map(ToOwned::to_owned),
            issuer: Some(issuer.to_owned()),
            client_id: Some(String::from("postflight-cli")),
        }
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

    /// Credentials written before the issuer was recorded still have to load,
    /// and still have to reach an issuer: upgrading the CLI must not strand
    /// someone in a state where signing out fails on a file they cannot see.
    #[test]
    fn credentials_without_an_issuer_fall_back_to_the_default() {
        let dir = scratch_dir("credentials-legacy");
        fs::create_dir_all(&dir).unwrap();
        fs::write(
            credentials_path(&dir),
            r#"{"access_token":"at-1","refresh_token":"rt-1"}"#,
        )
        .unwrap();

        let loaded = load_credentials(&dir).unwrap().expect("should load");

        assert_eq!(loaded.access_token, "at-1");
        assert_eq!(loaded.issuer, None);
        assert_eq!(loaded.issuer(), crate::DEFAULT_ISSUER);
        assert_eq!(loaded.client_id(), crate::DEFAULT_CLIENT_ID);
    }

    #[test]
    fn unreadable_credentials_are_named_as_such() {
        let dir = scratch_dir("credentials-unreadable");
        fs::create_dir_all(&dir).unwrap();
        fs::write(credentials_path(&dir), b"{").unwrap();

        assert!(matches!(
            load_credentials(&dir),
            Err(Error::UnreadableCredentials(_))
        ));
    }

    #[test]
    fn credentials_roundtrip_and_clear() {
        let dir = scratch_dir("credentials-roundtrip");

        assert!(load_credentials(&dir).unwrap().is_none());
        store_credentials(
            &dir,
            &stored("https://example.test/realms/r", "at-1", Some("rt-1")),
        )
        .unwrap();
        let loaded = load_credentials(&dir)
            .unwrap()
            .expect("credentials should load");
        assert_eq!(loaded.access_token, "at-1");
        assert_eq!(loaded.refresh_token.as_deref(), Some("rt-1"));
        assert_eq!(loaded.issuer(), "https://example.test/realms/r");

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
    }

    #[test]
    fn storing_credentials_leaves_no_staging_file_behind() {
        let dir = scratch_dir("credentials-staging");
        store_credentials(&dir, &stored("https://idp.example", "at-1", Some("rt-1"))).unwrap();
        store_credentials(&dir, &stored("https://idp.example", "at-2", Some("rt-2"))).unwrap();

        let entries: Vec<_> = fs::read_dir(&dir)
            .unwrap()
            .map(|entry| entry.unwrap().file_name())
            .collect();
        assert_eq!(entries, vec!["credentials.json"]);
        assert_eq!(
            load_credentials(&dir).unwrap().unwrap().access_token,
            "at-2"
        );
    }

    #[test]
    fn status_is_anonymous_without_credentials() {
        let dir = scratch_dir("status-anonymous");

        assert!(matches!(
            resolve_session(&agent(), &dir).unwrap(),
            SessionState::Anonymous
        ));
    }

    #[test]
    fn status_reports_the_user_the_issuer_names() {
        let server = TestServer::serve(vec![json_response(
            200,
            r#"{"preferred_username": "canary-01"}"#,
        )]);
        let dir = scratch_dir("status-live");
        store_credentials(&dir, &stored(&server.url, "at-1", Some("rt-1"))).unwrap();

        let state = resolve_session(&agent(), &dir).unwrap();

        assert!(
            matches!(state, SessionState::Live { username } if username.as_deref() == Some("canary-01"))
        );
        assert!(
            load_credentials(&dir).unwrap().is_some(),
            "a live session keeps its credentials"
        );
        let requests = server.finish();
        assert_eq!(requests.len(), 1);
        assert!(
            requests[0]
                .to_lowercase()
                .contains("authorization: bearer at-1")
        );
    }

    #[test]
    fn status_refreshes_a_stale_access_token_and_stores_the_rotation() {
        let server = TestServer::serve(vec![
            json_response(401, r#"{"error": "invalid_token"}"#),
            json_response(200, r#"{"access_token": "at-2", "refresh_token": "rt-2"}"#),
            json_response(200, r#"{"preferred_username": "canary-01"}"#),
        ]);
        let dir = scratch_dir("status-refresh");
        store_credentials(&dir, &stored(&server.url, "at-1", Some("rt-1"))).unwrap();

        let state = resolve_session(&agent(), &dir).unwrap();

        assert!(matches!(state, SessionState::Live { .. }));
        let loaded = load_credentials(&dir).unwrap().expect("credentials remain");
        assert_eq!(loaded.access_token, "at-2");
        assert_eq!(loaded.refresh_token.as_deref(), Some("rt-2"));
        assert_eq!(loaded.issuer(), server.url, "the rotation keeps its issuer");
        let requests = server.finish();
        assert!(requests[1].contains("grant_type=refresh_token"));
        assert!(
            requests[2]
                .to_lowercase()
                .contains("authorization: bearer at-2")
        );
    }

    #[test]
    fn status_clears_credentials_the_issuer_has_disowned() {
        let server = TestServer::serve(vec![
            json_response(401, r#"{"error": "invalid_token"}"#),
            json_response(400, r#"{"error": "invalid_grant"}"#),
        ]);
        let dir = scratch_dir("status-ended");
        store_credentials(&dir, &stored(&server.url, "at-1", Some("rt-1"))).unwrap();

        assert!(matches!(
            resolve_session(&agent(), &dir).unwrap(),
            SessionState::Ended
        ));
        assert!(
            load_credentials(&dir).unwrap().is_none(),
            "a dead session leaves nothing behind"
        );
        server.finish();
    }

    #[test]
    fn status_keeps_credentials_when_the_issuer_cannot_be_reached() {
        let dir = scratch_dir("status-unreachable");
        store_credentials(
            &dir,
            &stored(&TestServer::unreachable_url(), "at-1", Some("rt-1")),
        )
        .unwrap();

        assert!(
            resolve_session(&agent(), &dir).is_err(),
            "unreachable is not an answer about the session"
        );
        assert!(
            load_credentials(&dir).unwrap().is_some(),
            "a network failure must not sign anyone out"
        );
    }

    #[test]
    fn signing_out_ends_the_session_and_clears_the_file() {
        let server = TestServer::serve(vec![no_content_response()]);
        let dir = scratch_dir("signout-ends");
        store_credentials(&dir, &stored(&server.url, "at-1", Some("rt-1"))).unwrap();

        assert!(matches!(
            end_session(&agent(), &dir).unwrap(),
            SignOut::Revoked
        ));
        assert!(load_credentials(&dir).unwrap().is_none());
        let requests = server.finish();
        assert!(requests[0].starts_with("POST /protocol/openid-connect/logout "));
        assert!(requests[0].contains("refresh_token=rt-1"));
    }

    #[test]
    fn signing_out_of_an_already_dead_session_is_not_a_half_measure() {
        let server = TestServer::serve(vec![json_response(400, r#"{"error": "invalid_grant"}"#)]);
        let dir = scratch_dir("signout-already-dead");
        store_credentials(&dir, &stored(&server.url, "at-1", Some("stale"))).unwrap();

        assert!(matches!(
            end_session(&agent(), &dir).unwrap(),
            SignOut::Revoked
        ));
        server.finish();
    }

    #[test]
    fn signing_out_without_credentials_touches_nothing() {
        let dir = scratch_dir("signout-anonymous");

        assert!(matches!(
            end_session(&agent(), &dir).unwrap(),
            SignOut::NothingStored
        ));
    }

    #[test]
    fn signing_out_clears_the_file_even_when_the_issuer_cannot_be_reached() {
        let dir = scratch_dir("signout-unreachable");
        store_credentials(
            &dir,
            &stored(&TestServer::unreachable_url(), "at-1", Some("rt-1")),
        )
        .unwrap();

        assert!(matches!(
            end_session(&agent(), &dir).unwrap(),
            SignOut::LocalOnly { .. }
        ));
        assert!(
            load_credentials(&dir).unwrap().is_none(),
            "asking to sign out removes the token from this machine either way"
        );
    }
}
