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

    #[error("the sign-in service returned OAuth error \"{error}\"")]
    OAuth { error: String },

    #[error("could not read the identity token: {0}")]
    Claims(String),

    #[error("could not access stored credentials: {0}")]
    Storage(#[from] std::io::Error),

    #[error("{0}")]
    Environment(String),
}

impl From<ureq::Error> for Error {
    fn from(err: ureq::Error) -> Self {
        Error::Http(Box::new(err))
    }
}
