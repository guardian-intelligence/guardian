#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("request to the sign-in service failed: {0}")]
    Http(Box<ureq::Error>),

    #[error("the sign-in service returned HTTP {status}: {body}")]
    UnexpectedStatus { status: u16, body: String },

    #[error("sign-in was declined")]
    AccessDenied,

    #[error(
        "the sign-in request expired before it was approved; run `postflight auth login` again"
    )]
    Expired,

    #[error(
        "this sign-in request is no longer valid — it may already have been completed elsewhere; run `postflight auth login` again"
    )]
    RequestInvalidated,

    #[error(
        "the sign-in service did not recognize this CLI (OAuth error \"{error}\"); update the postflight CLI and try again"
    )]
    ClientNotRecognized { error: String },

    #[error("the sign-in service returned OAuth error \"{error}\"{}", .description.as_deref().map(|d| format!(": {d}")).unwrap_or_default())]
    OAuth {
        error: String,
        description: Option<String>,
    },

    #[error(
        "the sign-in service renewed this session and then rejected the renewed token; run `postflight auth login` again"
    )]
    RefreshedTokenRejected,

    #[error("could not read the identity token: {0}")]
    Claims(String),

    #[error("could not access stored credentials: {0}")]
    Storage(#[from] std::io::Error),

    #[error("the stored credentials are unreadable ({0}); run `postflight auth login` again")]
    UnreadableCredentials(String),

    #[error("{0}")]
    Environment(String),
}

impl From<ureq::Error> for Error {
    fn from(err: ureq::Error) -> Self {
        Error::Http(Box::new(err))
    }
}
