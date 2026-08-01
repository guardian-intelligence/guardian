use std::{
    ffi::OsStr,
    io::{self, Read, Write},
    process::{Child, Command, ExitStatus, Stdio},
    sync::mpsc::{self, Receiver, RecvTimeoutError, TryRecvError},
    thread,
    time::{Duration, Instant},
};

const MAX_HOST_BYTES: usize = 320;
const MAX_DIRECTORY_BYTES: usize = 4096;
const MAX_CAPTURE_BYTES: usize = 256 * 1024;
const WAIT_POLL_INTERVAL: Duration = Duration::from_millis(10);

const REMOTE_PROBE: &str = include_str!("remote_probe.sh");

const SSH_OPTIONS: &[&str] = &[
    "BatchMode=yes",
    "NumberOfPasswordPrompts=0",
    "PreferredAuthentications=publickey",
    "PubkeyAuthentication=yes",
    "PasswordAuthentication=no",
    "KbdInteractiveAuthentication=no",
    "HostbasedAuthentication=no",
    "GSSAPIAuthentication=no",
    "GSSAPIDelegateCredentials=no",
    "RequestTTY=no",
    "ClearAllForwardings=yes",
    "ForwardAgent=no",
    "ForwardX11=no",
    "PermitLocalCommand=no",
    "Tunnel=no",
    "AddKeysToAgent=no",
    "StrictHostKeyChecking=yes",
    "NoHostAuthenticationForLocalhost=no",
    "NoHostAuthenticationForProxyCommand=no",
    "UpdateHostKeys=no",
    "ForkAfterAuthentication=no",
    "ControlMaster=no",
    "ControlPath=none",
    "RemoteCommand=none",
    "EscapeChar=none",
    "SessionType=default",
    "StdinNull=no",
];

pub(crate) fn check(host: &str, directory: &str, timeout_seconds: u8) -> Result<(), String> {
    let stdout = io::stdout();
    let stderr = io::stderr();
    execute(
        OsStr::new("ssh"),
        host,
        directory,
        timeout_seconds,
        &mut stdout.lock(),
        &mut stderr.lock(),
    )
}

fn execute(
    ssh_program: &OsStr,
    host: &str,
    directory: &str,
    timeout_seconds: u8,
    stdout: &mut impl Write,
    stderr: &mut impl Write,
) -> Result<(), String> {
    validate_host(host)?;
    validate_directory(directory)?;
    if !(1..=60).contains(&timeout_seconds) {
        return Err("--timeout must be between 1 and 60 seconds".to_owned());
    }

    let timeout = Duration::from_secs(u64::from(timeout_seconds));
    let deadline = Instant::now() + timeout;
    let mut command = ssh_command(ssh_program, host, directory, timeout_seconds);
    let mut child = command.spawn().map_err(|error| match error.kind() {
        io::ErrorKind::NotFound => "ssh executable was not found on PATH".to_owned(),
        _ => format!("could not start ssh: {error}"),
    })?;

    let receiver = start_io_workers(&mut child).map_err(|error| {
        let _ = terminate(&mut child);
        format!("could not start bounded SSH I/O: {error}")
    })?;

    let status = match wait_until(&mut child, deadline) {
        Ok(WaitOutcome::Exited(status)) => status,
        Ok(WaitOutcome::TimedOut) => return Err(timeout_error(timeout_seconds)),
        Err(error) => {
            let _ = terminate(&mut child);
            return Err(format!("could not wait for SSH: {error}"));
        }
    };

    let Some(output) = collect_io(&receiver, deadline)? else {
        return Err(timeout_error(timeout_seconds));
    };
    emit_capture(stdout, &output.stdout, "standard output")?;
    emit_capture(stderr, &output.stderr, "standard error")?;

    match status.code() {
        Some(0) => output
            .stdin
            .map_err(|error| format!("could not send the fixed remote probe: {error}")),
        Some(3) => Err("the explicitly selected remote directory does not exist".to_owned()),
        Some(4) => Err("the explicitly selected remote path is not a directory".to_owned()),
        _ => Err(format!(
            "SSH status check failed with {status}; verify that the configured host is reachable with non-interactive public-key authentication"
        )),
    }
}

fn validate_host(host: &str) -> Result<(), String> {
    let error = || {
        "--host must be a non-option SSH alias or [user@]host containing only letters, digits, ., _, -, :, %, and IPv6 brackets"
            .to_owned()
    };
    if host.is_empty()
        || host.len() > MAX_HOST_BYTES
        || host.starts_with('-')
        || host.matches('@').count() > 1
        || !host.chars().all(|character| {
            character.is_ascii_alphanumeric()
                || matches!(character, '.' | '_' | '-' | '@' | ':' | '%' | '[' | ']')
        })
    {
        return Err(error());
    }

    let (user, destination) = host
        .split_once('@')
        .map_or((None, host), |(user, destination)| {
            (Some(user), destination)
        });
    if user.is_some_and(str::is_empty)
        || destination.is_empty()
        || destination.starts_with('-')
        || !destination
            .chars()
            .any(|character| character.is_ascii_alphanumeric())
    {
        return Err(error());
    }
    Ok(())
}

fn validate_directory(directory: &str) -> Result<(), String> {
    if !directory.starts_with('/')
        || directory.len() > MAX_DIRECTORY_BYTES
        || directory.chars().any(char::is_control)
        || directory.split('/').any(|component| component == "..")
    {
        return Err(
            "--directory must be an absolute path of at most 4096 bytes without control characters or parent-directory components"
                .to_owned(),
        );
    }
    Ok(())
}

fn quote_posix_shell_argument(argument: &str) -> String {
    let mut quoted = String::with_capacity(argument.len() + 2);
    quoted.push('\'');
    for character in argument.chars() {
        if character == '\'' {
            quoted.push_str("'\\''");
        } else {
            quoted.push(character);
        }
    }
    quoted.push('\'');
    quoted
}

fn ssh_command(program: &OsStr, host: &str, directory: &str, timeout_seconds: u8) -> Command {
    let mut command = Command::new(program);
    for option in SSH_OPTIONS {
        command.args(["-o", option]);
    }
    command.args(["-o", &format!("ConnectTimeout={timeout_seconds}")]);
    command.args([
        "--",
        host,
        &format!("sh -s -- {}", quote_posix_shell_argument(directory)),
    ]);
    command
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    command
}

enum WorkerMessage {
    Stdin(io::Result<()>),
    Stdout(io::Result<Capture>),
    Stderr(io::Result<Capture>),
}

struct IoOutput {
    stdin: io::Result<()>,
    stdout: Capture,
    stderr: Capture,
}

#[derive(Default)]
struct Capture {
    bytes: Vec<u8>,
    truncated: bool,
}

fn start_io_workers(child: &mut Child) -> io::Result<Receiver<WorkerMessage>> {
    let mut stdin = child
        .stdin
        .take()
        .ok_or_else(|| io::Error::other("SSH stdin was not piped"))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| io::Error::other("SSH stdout was not piped"))?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| io::Error::other("SSH stderr was not piped"))?;
    let (sender, receiver) = mpsc::channel();

    let stdin_sender = sender.clone();
    thread::Builder::new()
        .name("pipe-to-remote-box-stdin".to_owned())
        .spawn(move || {
            let result = stdin.write_all(REMOTE_PROBE.as_bytes());
            let _ = stdin_sender.send(WorkerMessage::Stdin(result));
        })?;

    let stdout_sender = sender.clone();
    thread::Builder::new()
        .name("pipe-to-remote-box-stdout".to_owned())
        .spawn(move || {
            let result = read_bounded(stdout);
            let _ = stdout_sender.send(WorkerMessage::Stdout(result));
        })?;

    thread::Builder::new()
        .name("pipe-to-remote-box-stderr".to_owned())
        .spawn(move || {
            let result = read_bounded(stderr);
            let _ = sender.send(WorkerMessage::Stderr(result));
        })?;

    Ok(receiver)
}

fn read_bounded(mut reader: impl Read) -> io::Result<Capture> {
    let mut capture = Capture::default();
    let mut chunk = [0_u8; 8192];
    loop {
        let read = reader.read(&mut chunk)?;
        if read == 0 {
            return Ok(capture);
        }
        let remaining = MAX_CAPTURE_BYTES.saturating_sub(capture.bytes.len());
        let retained = remaining.min(read);
        capture.bytes.extend_from_slice(&chunk[..retained]);
        capture.truncated |= retained < read;
    }
}

fn collect_io(
    receiver: &Receiver<WorkerMessage>,
    deadline: Instant,
) -> Result<Option<IoOutput>, String> {
    let mut stdin = None;
    let mut stdout = None;
    let mut stderr = None;

    while stdin.is_none() || stdout.is_none() || stderr.is_none() {
        let message = match receive_until(receiver, deadline) {
            Ok(Some(message)) => message,
            Ok(None) => return Ok(None),
            Err(error) => return Err(error),
        };
        match message {
            WorkerMessage::Stdin(result) => stdin = Some(result),
            WorkerMessage::Stdout(result) => stdout = Some(result),
            WorkerMessage::Stderr(result) => stderr = Some(result),
        }
    }

    Ok(Some(IoOutput {
        stdin: stdin.expect("stdin result is present"),
        stdout: stdout
            .expect("stdout result is present")
            .map_err(|error| format!("could not read SSH standard output: {error}"))?,
        stderr: stderr
            .expect("stderr result is present")
            .map_err(|error| format!("could not read SSH standard error: {error}"))?,
    }))
}

fn receive_until(
    receiver: &Receiver<WorkerMessage>,
    deadline: Instant,
) -> Result<Option<WorkerMessage>, String> {
    let now = Instant::now();
    if now >= deadline {
        return match receiver.try_recv() {
            Ok(message) => Ok(Some(message)),
            Err(TryRecvError::Empty) => Ok(None),
            Err(TryRecvError::Disconnected) => {
                Err("an SSH I/O worker stopped unexpectedly".to_owned())
            }
        };
    }

    match receiver.recv_timeout(deadline - now) {
        Ok(message) => Ok(Some(message)),
        Err(RecvTimeoutError::Timeout) => Ok(None),
        Err(RecvTimeoutError::Disconnected) => {
            Err("an SSH I/O worker stopped unexpectedly".to_owned())
        }
    }
}

enum WaitOutcome {
    Exited(ExitStatus),
    TimedOut,
}

fn wait_until(child: &mut Child, deadline: Instant) -> io::Result<WaitOutcome> {
    loop {
        if let Some(status) = child.try_wait()? {
            return Ok(WaitOutcome::Exited(status));
        }
        let now = Instant::now();
        if now >= deadline {
            terminate(child)?;
            return Ok(WaitOutcome::TimedOut);
        }
        thread::sleep(WAIT_POLL_INTERVAL.min(deadline - now));
    }
}

fn terminate(child: &mut Child) -> io::Result<()> {
    if child.try_wait()?.is_some() {
        return Ok(());
    }
    if let Err(kill_error) = child.kill()
        && child.try_wait()?.is_none()
    {
        return Err(kill_error);
    }
    child.wait().map(|_| ())
}

fn emit_capture(
    writer: &mut impl Write,
    capture: &Capture,
    stream_name: &str,
) -> Result<(), String> {
    writer
        .write_all(sanitize(&capture.bytes).as_bytes())
        .map_err(|error| format!("could not write SSH {stream_name}: {error}"))?;
    if capture.truncated {
        writeln!(
            writer,
            "\n[pipe-to-remote-box: {stream_name} truncated after {MAX_CAPTURE_BYTES} bytes]"
        )
        .map_err(|error| format!("could not write SSH {stream_name}: {error}"))?;
    }
    Ok(())
}

fn sanitize(bytes: &[u8]) -> String {
    let decoded = String::from_utf8_lossy(bytes);
    let mut sanitized = String::with_capacity(decoded.len());
    for character in decoded.chars() {
        if character == '\n' || !is_terminal_control(character) {
            sanitized.push(character);
        } else {
            sanitized.extend(character.escape_default());
        }
    }
    sanitized
}

fn is_terminal_control(character: char) -> bool {
    character.is_control()
        || matches!(
            character,
            '\u{061c}'
                | '\u{200b}'..='\u{200f}'
                | '\u{2028}'..='\u{202e}'
                | '\u{2060}'..='\u{206f}'
                | '\u{feff}'
        )
}

fn timeout_error(timeout_seconds: u8) -> String {
    format!(
        "SSH status check exceeded the {timeout_seconds}-second end-to-end deadline and was terminated"
    )
}

#[cfg(test)]
mod tests {
    use std::{
        collections::BTreeMap,
        env, fs,
        io::Cursor,
        os::unix::fs::{PermissionsExt, symlink},
        path::{Path, PathBuf},
        process::{self, Output},
        sync::atomic::{AtomicU64, Ordering},
        time::SystemTime,
    };

    use super::*;

    static NEXT_TEMP_DIRECTORY: AtomicU64 = AtomicU64::new(0);

    struct TestDirectory(PathBuf);

    impl TestDirectory {
        fn new(name: &str) -> Self {
            let unique = NEXT_TEMP_DIRECTORY.fetch_add(1, Ordering::Relaxed);
            let path = env::temp_dir().join(format!(
                "pipe-to-remote-box-test-{}-{unique}-{name}",
                process::id()
            ));
            fs::create_dir(&path).expect("create test directory");
            Self(path)
        }

        fn path(&self) -> &Path {
            &self.0
        }
    }

    impl Drop for TestDirectory {
        fn drop(&mut self) {
            fs::remove_dir_all(&self.0).expect("remove test directory");
        }
    }

    fn write_executable(path: &Path, contents: &str) {
        fs::write(path, contents).expect("write executable");
        fs::set_permissions(path, fs::Permissions::from_mode(0o700)).expect("make executable");
    }

    fn run_probe(directory: &Path, path: Option<&OsStr>) -> Output {
        let mut command = Command::new("sh");
        command
            .args(["-s", "--"])
            .arg(directory)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if let Some(path) = path {
            command.env("PATH", path);
        }
        let mut child = command.spawn().expect("start local probe");
        child
            .stdin
            .take()
            .expect("probe stdin")
            .write_all(REMOTE_PROBE.as_bytes())
            .expect("write probe");
        child.wait_with_output().expect("wait for local probe")
    }

    fn hex_path(path: &Path) -> String {
        use std::fmt::Write as _;

        let bytes = path.as_os_str().as_encoded_bytes();
        let mut encoded = String::with_capacity(bytes.len() * 2);
        for byte in bytes {
            write!(&mut encoded, "{byte:02x}").expect("write path hex");
        }
        encoded
    }

    fn fake_ssh_that_executes_probe(test_directory: &TestDirectory) -> PathBuf {
        let path = test_directory.path().join("ssh");
        write_executable(
            &path,
            "#!/bin/sh\nset -eu\nremote_command=\nfor argument in \"$@\"; do remote_command=$argument; done\nexec sh -c \"$remote_command\"\n",
        );
        path
    }

    #[derive(Debug, PartialEq, Eq)]
    struct SnapshotEntry {
        kind: &'static str,
        contents: Vec<u8>,
        modified: Option<SystemTime>,
    }

    fn snapshot(root: &Path) -> BTreeMap<PathBuf, SnapshotEntry> {
        fn walk(root: &Path, path: &Path, entries: &mut BTreeMap<PathBuf, SnapshotEntry>) {
            let mut children = fs::read_dir(path)
                .expect("read snapshot directory")
                .map(|entry| entry.expect("read snapshot entry").path())
                .collect::<Vec<_>>();
            children.sort();
            for child in children {
                let metadata = fs::symlink_metadata(&child).expect("snapshot metadata");
                let file_type = metadata.file_type();
                let relative = child
                    .strip_prefix(root)
                    .expect("relative path")
                    .to_path_buf();
                let (kind, contents) = if file_type.is_dir() {
                    ("directory", Vec::new())
                } else if file_type.is_symlink() {
                    (
                        "symlink",
                        fs::read_link(&child)
                            .expect("read symlink")
                            .as_os_str()
                            .as_encoded_bytes()
                            .to_vec(),
                    )
                } else {
                    ("file", fs::read(&child).expect("read snapshot file"))
                };
                entries.insert(
                    relative,
                    SnapshotEntry {
                        kind,
                        contents,
                        modified: metadata.modified().ok(),
                    },
                );
                if file_type.is_dir() {
                    walk(root, &child, entries);
                }
            }
        }

        let mut entries = BTreeMap::new();
        walk(root, root, &mut entries);
        entries
    }

    #[test]
    fn validates_explicit_ssh_destinations_and_absolute_directories() {
        for host in [
            "remote-box",
            "deploy@example.internal",
            "user@[fe80::1%en0]",
        ] {
            assert!(validate_host(host).is_ok(), "{host}");
        }
        for host in [
            "",
            "-oProxyCommand=evil",
            "host name",
            "host;command",
            "user@@host",
            "user@",
            "@host",
        ] {
            assert!(validate_host(host).is_err(), "{host}");
        }

        for directory in [
            "/srv/writing",
            "/srv/cloud sync/project's $(draft); [v1]",
            "/srv/.../child",
        ] {
            assert!(validate_directory(directory).is_ok(), "{directory}");
        }
        for directory in [
            "relative/path",
            "/srv/../secrets",
            "/srv/child/../../secrets",
            "/srv/control\u{1b}sequence",
        ] {
            assert!(validate_directory(directory).is_err(), "{directory}");
        }
    }

    #[test]
    fn ssh_arguments_are_fixed_non_interactive_and_option_terminated() {
        let directory = "/srv/cloud sync/project's $(draft); [v1]";
        let command = ssh_command(OsStr::new("ssh"), "deploy@remote-box", directory, 7);
        let arguments = command
            .get_args()
            .map(|argument| argument.to_string_lossy().into_owned())
            .collect::<Vec<_>>();

        for option in [
            "BatchMode=yes",
            "PreferredAuthentications=publickey",
            "PubkeyAuthentication=yes",
            "HostbasedAuthentication=no",
            "GSSAPIAuthentication=no",
            "GSSAPIDelegateCredentials=no",
            "RequestTTY=no",
            "ClearAllForwardings=yes",
            "ForwardAgent=no",
            "ForwardX11=no",
            "StrictHostKeyChecking=yes",
            "NoHostAuthenticationForLocalhost=no",
            "NoHostAuthenticationForProxyCommand=no",
            "UpdateHostKeys=no",
            "ForkAfterAuthentication=no",
            "ControlPath=none",
            "RemoteCommand=none",
            "SessionType=default",
            "StdinNull=no",
            "ConnectTimeout=7",
        ] {
            assert!(
                arguments.windows(2).any(|pair| pair == ["-o", option]),
                "missing {option}: {arguments:?}"
            );
        }

        let terminator = arguments
            .iter()
            .position(|argument| argument == "--")
            .unwrap();
        assert_eq!(
            &arguments[terminator..],
            &[
                "--",
                "deploy@remote-box",
                "sh -s -- '/srv/cloud sync/project'\\''s $(draft); [v1]'",
            ]
        );
    }

    #[test]
    fn shell_argument_transport_round_trips_injection_shaped_directory() {
        let directory = "/tmp/missing'; touch '/tmp/not-allowed'; # $(command)";
        let output = Command::new("sh")
            .args([
                "-c",
                &format!(
                    "set -- {}; printf '%s' \"$1\"",
                    quote_posix_shell_argument(directory)
                ),
            ])
            .output()
            .expect("run shell round trip");
        assert!(output.status.success());
        assert_eq!(output.stdout, directory.as_bytes());
    }

    #[test]
    fn injected_directory_is_data_and_cannot_create_a_file() {
        let test_directory = TestDirectory::new("transport");
        let fake_ssh = fake_ssh_that_executes_probe(&test_directory);
        let sentinel = test_directory.path().join("injected");
        let directory = format!(
            "{}/missing'; touch '{}'; #",
            test_directory.path().display(),
            sentinel.display()
        );
        let mut stdout = Vec::new();
        let mut stderr = Vec::new();

        let error = execute(
            fake_ssh.as_os_str(),
            "remote-box",
            &directory,
            2,
            &mut stdout,
            &mut stderr,
        )
        .expect_err("missing directory must fail");

        assert_eq!(
            error,
            "the explicitly selected remote directory does not exist"
        );
        assert!(!sentinel.exists());
        assert!(
            String::from_utf8(stdout)
                .unwrap()
                .contains(&format!("directory={directory}"))
        );
        assert!(stderr.is_empty());
    }

    #[test]
    fn probe_reports_only_two_levels_and_does_not_follow_symlinks() {
        let test_directory = TestDirectory::new("directory boundary");
        let selected = test_directory.path().join("selected cloud's files");
        let outside = test_directory.path().join("outside");
        let fake_bin = test_directory.path().join("bin");
        let git_invoked = test_directory.path().join("git-invoked");
        fs::create_dir_all(selected.join("one/two")).unwrap();
        fs::create_dir(&outside).unwrap();
        fs::create_dir(&fake_bin).unwrap();
        fs::write(selected.join("top.txt"), b"top").unwrap();
        fs::write(selected.join("one/second.txt"), b"second").unwrap();
        fs::write(selected.join("one/two/third.txt"), b"third").unwrap();
        fs::write(outside.join("secret.txt"), b"secret").unwrap();
        symlink(&outside, selected.join("outside-link")).unwrap();
        symlink(&outside, selected.join(".git")).unwrap();
        write_executable(
            &fake_bin.join("git"),
            &format!("#!/bin/sh\n: > '{}'\n", git_invoked.display()),
        );
        let mut path = fake_bin.into_os_string();
        path.push(":");
        path.push(env::var_os("PATH").unwrap_or_default());

        let output = run_probe(&selected, Some(&path));
        assert!(output.status.success(), "{:?}", output.stderr);
        let stdout = String::from_utf8(output.stdout).unwrap();
        assert!(stdout.contains("status=present"));
        assert!(stdout.contains("entries=4"));
        assert!(stdout.contains(&hex_path(&selected.join("top.txt"))));
        assert!(stdout.contains(&hex_path(&selected.join("one/second.txt"))));
        assert!(!stdout.contains(&hex_path(&selected.join("one/two/third.txt"))));
        assert!(!stdout.contains(&hex_path(&outside.join("secret.txt"))));
        assert!(
            !git_invoked.exists(),
            "a symlinked .git directory was probed"
        );
    }

    #[test]
    fn filename_bytes_cannot_forge_report_records_or_entry_counts() {
        let test_directory = TestDirectory::new("record framing");
        let selected = test_directory.path().join("selected");
        fs::create_dir(&selected).unwrap();
        let shaped = selected.join("ordinary\nstatus=missing\ngit_revision=forged\u{202e}");
        fs::write(&shaped, b"data").unwrap();

        let output = run_probe(&selected, None);
        assert!(output.status.success(), "{:?}", output.stderr);
        let stdout = String::from_utf8(output.stdout).unwrap();

        assert!(stdout.contains("entries=1\n"), "{stdout:?}");
        assert_eq!(stdout.matches("status=").count(), 1, "{stdout:?}");
        assert!(!stdout.contains("git_revision=forged"), "{stdout:?}");
        assert!(!stdout.contains('\u{202e}'), "{stdout:?}");
        assert!(stdout.contains(&hex_path(&shaped)), "{stdout:?}");
    }

    #[test]
    fn probe_distinguishes_missing_path_and_non_directory() {
        let test_directory = TestDirectory::new("missing");
        let missing = test_directory.path().join("does-not-exist");
        let missing_output = run_probe(&missing, None);
        assert_eq!(missing_output.status.code(), Some(3));
        assert!(
            String::from_utf8(missing_output.stdout)
                .unwrap()
                .contains("status=missing")
        );

        let file = test_directory.path().join("file");
        fs::write(&file, b"not a directory").unwrap();
        let file_output = run_probe(&file, None);
        assert_eq!(file_output.status.code(), Some(4));
        assert!(
            String::from_utf8(file_output.stdout)
                .unwrap()
                .contains("status=not-a-directory")
        );
    }

    #[test]
    fn git_probe_reports_only_validated_revision_metadata_without_status() {
        let test_directory = TestDirectory::new("no mutation");
        let selected = test_directory.path().join("selected");
        let fake_bin = test_directory.path().join("bin");
        let sentinel = selected.join("mutation");
        let log = test_directory.path().join("git-invocations");
        fs::create_dir_all(selected.join(".git")).unwrap();
        fs::create_dir(&fake_bin).unwrap();
        fs::write(selected.join("draft.txt"), b"unchanged").unwrap();

        let fake_git = fake_bin.join("git");
        write_executable(
            &fake_git,
            &format!(
                "#!/bin/sh\nset -eu\nif [ \"${{GIT_OPTIONAL_LOCKS-}}\" != 0 ] || [ \"${{GIT_NO_LAZY_FETCH-}}\" != 1 ] || [ \"${{GIT_CONFIG_NOSYSTEM-}}\" != 1 ] || [ \"${{GIT_CONFIG_GLOBAL-}}\" != /dev/null ]; then : > '{}'; fi\nprintf '%s|%s|%s|%s|%s\\n' \"${{GIT_OPTIONAL_LOCKS-}}\" \"${{GIT_NO_LAZY_FETCH-}}\" \"${{GIT_CONFIG_NOSYSTEM-}}\" \"${{GIT_CONFIG_GLOBAL-}}\" \"$*\" >> '{}'\ncase \" $* \" in\n  *' rev-parse '*) printf '0123456789abcdef0123456789abcdef01234567\\n' ;;\n  *' log '*) printf '1785456000\\n' ;;\nesac\n",
                sentinel.display(),
                log.display()
            ),
        );

        let mut path = fake_bin.into_os_string();
        path.push(":");
        path.push(env::var_os("PATH").unwrap_or_default());
        let before = snapshot(&selected);
        let output = run_probe(&selected, Some(&path));
        let after = snapshot(&selected);

        assert!(output.status.success(), "{:?}", output.stderr);
        assert_eq!(before, after, "the selected directory changed");
        assert!(!sentinel.exists());
        let stdout = String::from_utf8(output.stdout).unwrap();
        assert!(stdout.contains("git_revision=0123456789abcdef0123456789abcdef01234567"));
        assert!(stdout.contains("git_commit_time=1785456000"));
        let invocations = fs::read_to_string(log).unwrap();
        assert_eq!(invocations.lines().count(), 2);
        assert!(
            invocations
                .lines()
                .all(|line| line.starts_with("0|1|1|/dev/null|"))
        );
        assert!(invocations.contains("--no-optional-locks"));
        assert!(invocations.contains("log.showSignature=false"));
        assert!(!invocations.contains(" status "));
    }

    #[test]
    fn repository_clean_filters_are_never_executed() {
        let test_directory = TestDirectory::new("hostile git config");
        let selected = test_directory.path().join("selected");
        let sentinel = test_directory.path().join("filter-executed");
        fs::create_dir(&selected).unwrap();

        let git = |arguments: &[&str]| {
            let output = Command::new("git")
                .args(arguments)
                .current_dir(&selected)
                .output()
                .expect("run git fixture command");
            assert!(output.status.success(), "{:?}", output.stderr);
        };
        git(&["init", "-q"]);
        git(&["config", "user.name", "Pipe test"]);
        git(&["config", "user.email", "pipe@example.invalid"]);
        fs::write(selected.join("tracked.txt"), b"original\n").unwrap();
        git(&["add", "tracked.txt"]);
        git(&["commit", "-q", "-m", "fixture"]);
        fs::write(selected.join(".gitattributes"), b"*.txt filter=ptrb\n").unwrap();
        git(&[
            "config",
            "filter.ptrb.clean",
            &format!("touch '{}'; cat", sentinel.display()),
        ]);
        fs::write(selected.join("tracked.txt"), b"modified\n").unwrap();

        let output = run_probe(&selected, None);
        assert!(output.status.success(), "{:?}", output.stderr);
        assert!(
            !sentinel.exists(),
            "the probe executed a repository-defined clean filter"
        );
        let stdout = String::from_utf8(output.stdout).unwrap();
        assert!(stdout.contains("git_revision="));
        assert!(stdout.contains("git_commit_time="));
    }

    #[test]
    fn fixed_probe_contains_no_remote_mutation_commands() {
        for forbidden in [
            "rm ",
            "mv ",
            "cp ",
            "touch ",
            "mkdir ",
            "chmod ",
            "chown ",
            "sed -i",
            "git clean",
            "git reset",
            "git fetch",
            "git pull",
            " status ",
        ] {
            assert!(!REMOTE_PROBE.contains(forbidden), "found {forbidden}");
        }
        assert!(REMOTE_PROBE.contains("GIT_OPTIONAL_LOCKS=0"));
        assert!(REMOTE_PROBE.contains("GIT_NO_LAZY_FETCH=1"));
        assert!(REMOTE_PROBE.contains("GIT_CONFIG_NOSYSTEM=1"));
        assert!(REMOTE_PROBE.contains("GIT_CONFIG_GLOBAL=/dev/null"));
    }

    #[test]
    fn end_to_end_timeout_terminates_connected_hung_ssh() {
        let test_directory = TestDirectory::new("timeout");
        let fake_ssh = test_directory.path().join("ssh");
        let connected = test_directory.path().join("connected");
        write_executable(
            &fake_ssh,
            &format!(
                "#!/bin/sh\nset -eu\nwhile IFS= read -r line; do :; done\n: > '{}'\nexec sleep 30\n",
                connected.display()
            ),
        );
        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        let started = Instant::now();

        let error = execute(
            fake_ssh.as_os_str(),
            "remote-box",
            "/srv/writing",
            1,
            &mut stdout,
            &mut stderr,
        )
        .expect_err("hung SSH must time out");
        let elapsed = started.elapsed();

        assert!(
            connected.exists(),
            "fake SSH did not consume the probe first"
        );
        assert!(error.contains("1-second end-to-end deadline"), "{error}");
        assert!(elapsed >= Duration::from_millis(900), "{elapsed:?}");
        assert!(elapsed < Duration::from_secs(5), "{elapsed:?}");
    }

    #[test]
    fn remote_output_is_bounded_and_terminal_controls_are_escaped() {
        assert_eq!(
            sanitize("safe\n\u{1b}[2Jdanger\r\n\u{009b}31m\u{202e}bidi".as_bytes()),
            "safe\n\\u{1b}[2Jdanger\\r\n\\u{9b}31m\\u{202e}bidi"
        );

        let input = vec![b'x'; MAX_CAPTURE_BYTES + 17];
        let mut reader = Cursor::new(input);
        let capture = read_bounded(&mut reader).unwrap();
        assert_eq!(capture.bytes.len(), MAX_CAPTURE_BYTES);
        assert!(capture.truncated);
        assert_eq!(
            reader.position(),
            u64::try_from(MAX_CAPTURE_BYTES + 17).unwrap()
        );
        let mut output = Vec::new();
        emit_capture(&mut output, &capture, "standard output").unwrap();
        assert!(output.len() > MAX_CAPTURE_BYTES);
        assert!(
            String::from_utf8(output)
                .unwrap()
                .ends_with("standard output truncated after 262144 bytes]\n")
        );
    }
}
