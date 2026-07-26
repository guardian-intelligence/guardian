//! The OIDC session behind stored credentials: what the issuer will still
//! honour, and how to end it.

use serde::Deserialize;

use crate::device::TokenSet;
use crate::error::Error;

#[derive(Debug, Deserialize)]
struct OAuthErrorBody {
    error: String,
}

#[derive(Debug, Deserialize)]
pub struct UserInfo {
    #[serde(default)]
    pub preferred_username: Option<String>,
}

pub struct Session<'a> {
    agent: &'a ureq::Agent,
    issuer: String,
    client_id: &'a str,
}

impl<'a> Session<'a> {
    pub fn new(agent: &'a ureq::Agent, issuer: &'a str, client_id: &'a str) -> Self {
        Self {
            agent,
            issuer: issuer.trim_end_matches('/').to_owned(),
            client_id,
        }
    }

    /// Ask the issuer who this access token belongs to. `None` means the
    /// issuer refused it — expired, revoked, or riding a session that no longer
    /// exists. Every other failure is an error, because "we could not reach the
    /// issuer" must never read as "you are signed out".
    pub fn user_info(&self, access_token: &str) -> Result<Option<UserInfo>, Error> {
        let url = format!("{}/protocol/openid-connect/userinfo", self.issuer);
        let mut response = self
            .agent
            .get(&url)
            .header("Authorization", &format!("Bearer {access_token}"))
            .call()?;
        match response.status().as_u16() {
            200 => Ok(Some(response.body_mut().read_json()?)),
            401 | 403 => Ok(None),
            status => Err(unexpected_status(status, &mut response)),
        }
    }

    /// Trade a refresh token for a fresh set. `None` means the issuer has
    /// disowned the grant, which is the durable form of "signed out".
    pub fn refresh(&self, refresh_token: &str) -> Result<Option<TokenSet>, Error> {
        let url = format!("{}/protocol/openid-connect/token", self.issuer);
        let mut response = self.agent.post(&url).send_form([
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token),
            ("client_id", self.client_id),
        ])?;
        let status = response.status().as_u16();
        if status == 200 {
            return Ok(Some(response.body_mut().read_json()?));
        }
        match oauth_error(status, &mut response) {
            InvalidGrant::Yes => Ok(None),
            InvalidGrant::No(err) => Err(err),
        }
    }

    /// End the session this refresh token belongs to. A grant the issuer has
    /// already disowned is the outcome the caller asked for, not a failure to
    /// report.
    pub fn end(&self, refresh_token: &str) -> Result<(), Error> {
        let url = format!("{}/protocol/openid-connect/logout", self.issuer);
        let mut response = self.agent.post(&url).send_form([
            ("client_id", self.client_id),
            ("refresh_token", refresh_token),
        ])?;
        let status = response.status().as_u16();
        if (200..300).contains(&status) {
            return Ok(());
        }
        match oauth_error(status, &mut response) {
            InvalidGrant::Yes => Ok(()),
            InvalidGrant::No(err) => Err(err),
        }
    }
}

enum InvalidGrant {
    Yes,
    No(Error),
}

/// Classify a non-success response from a token-family endpoint: an
/// `invalid_grant` is the issuer saying the credential is dead, and anything
/// else is a fault the caller should surface rather than act on.
fn oauth_error(status: u16, response: &mut ureq::http::Response<ureq::Body>) -> InvalidGrant {
    let Ok(body) = response.body_mut().read_json::<OAuthErrorBody>() else {
        return InvalidGrant::No(unexpected_status(status, response));
    };
    if body.error == "invalid_grant" {
        return InvalidGrant::Yes;
    }
    InvalidGrant::No(Error::OAuth { error: body.error })
}

pub fn unexpected_status(status: u16, response: &mut ureq::http::Response<ureq::Body>) -> Error {
    let body = response
        .body_mut()
        .read_to_string()
        .unwrap_or_else(|_| String::from("<unreadable body>"));
    Error::UnexpectedStatus { status, body }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::{TestServer, agent, json_response, no_content_response};

    #[test]
    fn user_info_reads_the_username_the_issuer_reports() {
        let server = TestServer::serve(vec![json_response(
            200,
            r#"{"sub": "abc", "preferred_username": "canary-01"}"#,
        )]);
        let agent = agent();
        let session = Session::new(&agent, &server.url, "postflight-cli");

        let info = session
            .user_info("at-1")
            .expect("userinfo should succeed")
            .expect("a live session should be reported");

        assert_eq!(info.preferred_username.as_deref(), Some("canary-01"));
        let requests = server.finish();
        assert!(requests[0].starts_with("GET /protocol/openid-connect/userinfo"));
        assert!(
            requests[0]
                .to_lowercase()
                .contains("authorization: bearer at-1")
        );
    }

    #[test]
    fn user_info_reports_a_refused_token_rather_than_failing() {
        let server = TestServer::serve(vec![json_response(401, r#"{"error": "invalid_token"}"#)]);
        let agent = agent();
        let session = Session::new(&agent, &server.url, "postflight-cli");

        assert!(
            session
                .user_info("at-1")
                .expect("401 is an answer")
                .is_none()
        );
        server.finish();
    }

    #[test]
    fn user_info_surfaces_an_issuer_fault() {
        let server = TestServer::serve(vec![json_response(503, "{}")]);
        let agent = agent();
        let session = Session::new(&agent, &server.url, "postflight-cli");

        assert!(matches!(
            session.user_info("at-1"),
            Err(Error::UnexpectedStatus { status: 503, .. })
        ));
        server.finish();
    }

    #[test]
    fn refresh_returns_the_rotated_set() {
        let server = TestServer::serve(vec![json_response(
            200,
            r#"{"access_token": "at-2", "refresh_token": "rt-2"}"#,
        )]);
        let agent = agent();
        let session = Session::new(&agent, &server.url, "postflight-cli");

        let tokens = session
            .refresh("rt-1")
            .expect("refresh should succeed")
            .expect("a live grant should mint tokens");

        assert_eq!(tokens.access_token, "at-2");
        assert_eq!(tokens.refresh_token.as_deref(), Some("rt-2"));
        let requests = server.finish();
        assert!(requests[0].contains("grant_type=refresh_token"));
        assert!(requests[0].contains("refresh_token=rt-1"));
        assert!(requests[0].contains("client_id=postflight-cli"));
    }

    #[test]
    fn refresh_separates_a_dead_grant_from_a_broken_client() {
        let dead = TestServer::serve(vec![json_response(400, r#"{"error": "invalid_grant"}"#)]);
        let broken = TestServer::serve(vec![json_response(400, r#"{"error": "invalid_client"}"#)]);
        let agent = agent();

        assert!(
            Session::new(&agent, &dead.url, "postflight-cli")
                .refresh("rt-1")
                .expect("invalid_grant is an answer")
                .is_none()
        );
        assert!(matches!(
            Session::new(&agent, &broken.url, "postflight-cli").refresh("rt-1"),
            Err(Error::OAuth { .. })
        ));
        dead.finish();
        broken.finish();
    }

    #[test]
    fn end_sends_the_refresh_token_to_the_logout_endpoint() {
        let server = TestServer::serve(vec![no_content_response()]);
        let agent = agent();
        let session = Session::new(&agent, &server.url, "postflight-cli");

        session.end("rt-1").expect("logout should succeed");

        let requests = server.finish();
        assert!(requests[0].starts_with("POST /protocol/openid-connect/logout "));
        assert!(requests[0].contains("client_id=postflight-cli"));
        assert!(requests[0].contains("refresh_token=rt-1"));
    }

    // A grant the issuer has already forgotten is the state the caller wanted;
    // reporting it as a failed sign-out would send people looking for a session
    // that is not there.
    #[test]
    fn end_treats_an_already_dead_grant_as_done() {
        let server = TestServer::serve(vec![json_response(400, r#"{"error": "invalid_grant"}"#)]);
        let agent = agent();

        Session::new(&agent, &server.url, "postflight-cli")
            .end("stale")
            .expect("a dead grant is already signed out");
        server.finish();
    }

    #[test]
    fn end_reports_a_refusal_that_is_not_about_the_grant() {
        let server = TestServer::serve(vec![json_response(503, "gateway sad")]);
        let agent = agent();

        assert!(matches!(
            Session::new(&agent, &server.url, "postflight-cli").end("rt-1"),
            Err(Error::UnexpectedStatus { status: 503, .. })
        ));
        server.finish();
    }

    #[test]
    fn issuer_trailing_slash_does_not_double_up_the_path() {
        let server = TestServer::serve(vec![json_response(200, "{}")]);
        let agent = agent();
        let issuer = format!("{}/", server.url);
        let session = Session::new(&agent, &issuer, "postflight-cli");

        session.user_info("at-1").expect("userinfo should succeed");

        let requests = server.finish();
        assert!(requests[0].starts_with("GET /protocol/openid-connect/userinfo"));
    }
}
