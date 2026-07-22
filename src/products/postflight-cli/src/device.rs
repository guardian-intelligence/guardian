//! OAuth 2.0 Device Authorization Grant (RFC 8628) against an OIDC issuer.

use std::time::Duration;

use serde::Deserialize;

use crate::error::Error;

pub const DEVICE_GRANT_TYPE: &str = "urn:ietf:params:oauth:grant-type:device_code";

/// The issuer's `verification_uri` is intentionally not parsed: the CLI
/// always sends users to the product's own approval page.
#[derive(Debug, Deserialize)]
pub struct DeviceAuthorization {
    pub device_code: String,
    pub user_code: String,
    pub expires_in: u64,
    #[serde(default = "default_interval")]
    pub interval: u64,
}

fn default_interval() -> u64 {
    5
}

// Field names are fixed by the OAuth token-response wire format.
#[allow(clippy::struct_field_names)]
#[derive(Debug, Deserialize)]
pub struct TokenSet {
    pub access_token: String,
    #[serde(default)]
    pub refresh_token: Option<String>,
    #[serde(default)]
    pub id_token: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OAuthErrorBody {
    error: String,
}

#[derive(Debug)]
pub enum Poll {
    Pending,
    SlowDown,
    Ready(Box<TokenSet>),
}

pub struct DeviceFlow<'a> {
    agent: &'a ureq::Agent,
    issuer: &'a str,
    client_id: &'a str,
}

impl<'a> DeviceFlow<'a> {
    pub fn new(agent: &'a ureq::Agent, issuer: &'a str, client_id: &'a str) -> Self {
        Self {
            agent,
            issuer,
            client_id,
        }
    }

    pub fn start(&self) -> Result<DeviceAuthorization, Error> {
        let url = format!("{}/protocol/openid-connect/auth/device", self.issuer);
        let mut response = self
            .agent
            .post(&url)
            .send_form([("client_id", self.client_id), ("scope", "openid")])?;
        let status = response.status().as_u16();
        if status != 200 {
            return Err(unexpected_status(status, &mut response));
        }
        Ok(response.body_mut().read_json()?)
    }

    pub fn poll_once(&self, device_code: &str) -> Result<Poll, Error> {
        let url = format!("{}/protocol/openid-connect/token", self.issuer);
        let mut response = self.agent.post(&url).send_form([
            ("grant_type", DEVICE_GRANT_TYPE),
            ("device_code", device_code),
            ("client_id", self.client_id),
        ])?;
        let status = response.status().as_u16();
        if status == 200 {
            return Ok(Poll::Ready(Box::new(response.body_mut().read_json()?)));
        }
        let Ok(body) = response.body_mut().read_json::<OAuthErrorBody>() else {
            return Err(unexpected_status(status, &mut response));
        };
        match body.error.as_str() {
            "authorization_pending" => Ok(Poll::Pending),
            "slow_down" => Ok(Poll::SlowDown),
            "access_denied" => Err(Error::AccessDenied),
            "expired_token" => Err(Error::Expired),
            other => Err(Error::OAuth {
                error: other.to_owned(),
            }),
        }
    }

    /// Poll the token endpoint until the user approves, honoring the
    /// server-directed interval and `slow_down` backoff. `sleep` is
    /// injected so tests can observe the cadence instead of waiting it out.
    pub fn wait_for_approval(
        &self,
        authorization: &DeviceAuthorization,
        sleep: &mut dyn FnMut(Duration),
    ) -> Result<TokenSet, Error> {
        let mut interval = authorization.interval.max(1);
        let mut waited: u64 = 0;
        loop {
            if waited >= authorization.expires_in {
                return Err(Error::Expired);
            }
            sleep(Duration::from_secs(interval));
            waited += interval;
            match self.poll_once(&authorization.device_code)? {
                Poll::Ready(tokens) => return Ok(*tokens),
                Poll::Pending => {}
                Poll::SlowDown => interval += 5,
            }
        }
    }
}

fn unexpected_status(status: u16, response: &mut ureq::http::Response<ureq::Body>) -> Error {
    let body = response
        .body_mut()
        .read_to_string()
        .unwrap_or_else(|_| String::from("<unreadable body>"));
    Error::UnexpectedStatus { status, body }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::{TestServer, json_response};

    fn agent() -> ureq::Agent {
        ureq::Agent::new_with_config(
            ureq::Agent::config_builder()
                .http_status_as_error(false)
                .build(),
        )
    }

    #[test]
    fn start_parses_device_authorization() {
        let server = TestServer::serve(vec![json_response(
            200,
            r#"{
                "device_code": "dev-123",
                "user_code": "WDJB-MJHT",
                "verification_uri": "https://idp.example/device",
                "verification_uri_complete": "https://idp.example/device?user_code=WDJB-MJHT",
                "expires_in": 600,
                "interval": 5
            }"#,
        )]);
        let agent = agent();
        let flow = DeviceFlow::new(&agent, &server.url, "postflight-cli");

        let authorization = flow.start().expect("start should succeed");

        assert_eq!(authorization.device_code, "dev-123");
        assert_eq!(authorization.user_code, "WDJB-MJHT");
        assert_eq!(authorization.expires_in, 600);
        assert_eq!(authorization.interval, 5);
        let requests = server.finish();
        assert!(requests[0].starts_with("POST /protocol/openid-connect/auth/device"));
        assert!(requests[0].contains("client_id=postflight-cli"));
        assert!(requests[0].contains("scope=openid"));
    }

    #[test]
    fn start_surfaces_unexpected_status() {
        let server = TestServer::serve(vec![json_response(502, r#"{"gateway": "sad"}"#)]);
        let agent = agent();
        let flow = DeviceFlow::new(&agent, &server.url, "postflight-cli");

        let err = flow.start().expect_err("start should fail");

        assert!(matches!(err, Error::UnexpectedStatus { status: 502, .. }));
        server.finish();
    }

    #[test]
    fn wait_for_approval_honors_pending_and_slow_down() {
        let server = TestServer::serve(vec![
            json_response(400, r#"{"error": "authorization_pending"}"#),
            json_response(400, r#"{"error": "slow_down"}"#),
            json_response(
                200,
                r#"{"access_token": "at-1", "refresh_token": "rt-1", "id_token": "idt-1"}"#,
            ),
        ]);
        let agent = agent();
        let flow = DeviceFlow::new(&agent, &server.url, "postflight-cli");
        let authorization = DeviceAuthorization {
            device_code: "dev-123".into(),
            user_code: "WDJB-MJHT".into(),
            expires_in: 600,
            interval: 5,
        };
        let mut sleeps = Vec::new();

        let tokens = flow
            .wait_for_approval(&authorization, &mut |d| sleeps.push(d.as_secs()))
            .expect("approval should complete");

        assert_eq!(tokens.access_token, "at-1");
        assert_eq!(tokens.id_token.as_deref(), Some("idt-1"));
        // slow_down bumps the interval by five seconds per RFC 8628 §3.5.
        assert_eq!(sleeps, vec![5, 5, 10]);
        let requests = server.finish();
        assert_eq!(requests.len(), 3);
        assert!(
            requests[0]
                .contains("grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code")
        );
        assert!(requests[0].contains("device_code=dev-123"));
    }

    #[test]
    fn wait_for_approval_maps_access_denied() {
        let server = TestServer::serve(vec![json_response(400, r#"{"error": "access_denied"}"#)]);
        let agent = agent();
        let flow = DeviceFlow::new(&agent, &server.url, "postflight-cli");
        let authorization = DeviceAuthorization {
            device_code: "dev-123".into(),
            user_code: "WDJB-MJHT".into(),
            expires_in: 600,
            interval: 1,
        };

        let err = flow
            .wait_for_approval(&authorization, &mut |_| {})
            .expect_err("denied approval should fail");

        assert!(matches!(err, Error::AccessDenied));
        server.finish();
    }

    #[test]
    fn wait_for_approval_expires_without_polling_a_dead_request() {
        let server = TestServer::serve(vec![]);
        let agent = agent();
        let flow = DeviceFlow::new(&agent, &server.url, "postflight-cli");
        let authorization = DeviceAuthorization {
            device_code: "dev-123".into(),
            user_code: "WDJB-MJHT".into(),
            expires_in: 0,
            interval: 5,
        };

        let err = flow
            .wait_for_approval(&authorization, &mut |_| {})
            .expect_err("expired request should fail");

        assert!(matches!(err, Error::Expired));
        server.finish();
    }
}
