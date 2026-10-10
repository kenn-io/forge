use anyhow::{Context, Result, anyhow, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD as BASE64;
use chrono::{DateTime, Utc};
use portable_pty::{ChildKiller, CommandBuilder, PtySize, native_pty_system};
use rand::Rng;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::env;
#[cfg(windows)]
use std::ffi::OsString;
use std::fs::{self, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::net::Shutdown;
#[cfg(windows)]
use std::net::{TcpListener, TcpStream};
#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};
#[cfg(unix)]
use std::os::unix::net::{UnixListener, UnixStream};
#[cfg(windows)]
use std::os::windows::ffi::OsStrExt;
use std::path::{Path, PathBuf};
#[cfg(windows)]
use std::ptr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};
#[cfg(windows)]
use windows_sys::Win32::Foundation::{CloseHandle, GetLastError, LocalFree};
#[cfg(windows)]
use windows_sys::Win32::Security::Authorization::{
    ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW, SDDL_REVISION_1,
};
#[cfg(windows)]
use windows_sys::Win32::Security::{
    DACL_SECURITY_INFORMATION, GetTokenInformation, PROTECTED_DACL_SECURITY_INFORMATION,
    PSECURITY_DESCRIPTOR, SetFileSecurityW, TOKEN_QUERY, TOKEN_USER, TokenUser,
};
#[cfg(windows)]
use windows_sys::Win32::System::Threading::{GetCurrentProcess, OpenProcessToken};

const MAX_OUTPUT_REPLAY: usize = 64 * 1024;
const MAX_OWNER_FIRST_REQUEST_SIZE: usize = 8 * 1024;
const MAX_OWNER_REQUEST_SIZE: usize = 96 * 1024;
const MAX_OWNER_INPUT_SIZE: usize = 64 * 1024;
const MAX_UNIX_SOCKET_PATH_LEN: usize = 100;
const SUBSCRIBER_CHANNEL_CAPACITY: usize = 64;
const OWNER_EXIT_CODE_GRACE_PERIOD: Duration = Duration::from_millis(200);
const OWNER_POST_EXIT_LINGER: Duration = Duration::from_secs(5);
const OWNER_FIRST_REQUEST_TIMEOUT: Duration = Duration::from_secs(5);
const MAX_OWNER_CONNECTIONS: usize = 32;
const INITIAL_PTY_SIZE: PtySize = PtySize {
    rows: 24,
    cols: 80,
    pixel_width: 80 * 8,
    pixel_height: 24 * 16,
};

#[cfg(windows)]
type OwnerListener = TcpListener;
#[cfg(unix)]
type OwnerListener = UnixListener;
#[cfg(windows)]
type OwnerStream = TcpStream;
#[cfg(unix)]
type OwnerStream = UnixStream;

#[derive(Debug, Default)]
struct Args {
    root: PathBuf,
    session: String,
    cwd: PathBuf,
    command: Vec<String>,
}

#[derive(Debug, Clone)]
struct SessionPaths {
    dir: PathBuf,
    socket: PathBuf,
    socket_dir: Option<PathBuf>,
    state_path: PathBuf,
}

struct OwnerCleanup {
    paths: SessionPaths,
    killer: Option<Box<dyn ChildKiller + Send + Sync>>,
}

struct ActiveAttachment {
    shared: Arc<Mutex<Shared>>,
    subscriber_id: Option<u64>,
}

#[derive(Clone)]
struct Subscriber {
    id: u64,
    tx: mpsc::SyncSender<Vec<u8>>,
}

#[derive(Debug, Serialize)]
struct OwnerState<'a> {
    session: &'a str,
    addr: String,
    token: &'a str,
    cwd: &'a str,
    pid: u32,
    created_at: DateTime<Utc>,
}

#[derive(Debug, Deserialize)]
struct Request {
    #[serde(rename = "type")]
    kind: String,
    #[serde(default)]
    token: String,
    #[serde(default)]
    cols: u16,
    #[serde(default)]
    rows: u16,
    #[serde(default)]
    pixel_width: u16,
    #[serde(default)]
    pixel_height: u16,
    #[serde(default, deserialize_with = "deserialize_base64_bytes")]
    data: Vec<u8>,
}

impl Request {
    fn pty_size(&self) -> PtySize {
        PtySize {
            rows: self.rows,
            cols: self.cols,
            pixel_width: self.pixel_width,
            pixel_height: self.pixel_height,
        }
    }
}

#[derive(Debug, Serialize)]
struct Response<'a> {
    #[serde(rename = "type")]
    kind: &'a str,
    #[serde(skip_serializing_if = "is_false")]
    ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    exit_code: Option<i32>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        serialize_with = "serialize_base64_bytes"
    )]
    output: Vec<u8>,
    #[serde(skip_serializing_if = "Option::is_none")]
    title: Option<String>,
}

#[derive(Default)]
struct Shared {
    output: Vec<u8>,
    title: Option<String>,
    title_parser: TitleParserState,
    subscribers: Vec<Subscriber>,
    next_subscriber_id: u64,
    attached: bool,
    attachment_ended_after_exit: bool,
    child_reaped: bool,
    exited: bool,
    reader_done: bool,
    stopping: bool,
    exit_code: i32,
}

struct AttachRuntime {
    shared: Arc<Mutex<Shared>>,
    writer: Arc<Mutex<Box<dyn Write + Send>>>,
    master: Arc<Mutex<Box<dyn portable_pty::MasterPty + Send>>>,
}

fn main() {
    if let Err(err) = run() {
        eprintln!("{err:#}");
        std::process::exit(1);
    }
}

fn run() -> Result<()> {
    let args = parse_args(env::args().skip(1))?;
    run_owner(args)
}

fn parse_args<I>(args: I) -> Result<Args>
where
    I: IntoIterator<Item = String>,
{
    let mut values = HashMap::new();
    let mut iter = args.into_iter();
    while let Some(arg) = iter.next() {
        let key = match arg.as_str() {
            "-root" | "--root" => "root",
            "-session" | "--session" => "session",
            "-cwd" | "--cwd" => "cwd",
            "-command-json" | "--command-json" => "command-json",
            other => bail!("unknown argument {other}"),
        };
        let value = iter
            .next()
            .ok_or_else(|| anyhow!("argument {arg} requires a value"))?;
        values.insert(key.to_string(), value);
    }

    let root = values
        .remove("root")
        .ok_or_else(|| anyhow!("pty-manager root is required"))?;
    let session = values
        .remove("session")
        .ok_or_else(|| anyhow!("pty-manager session is required"))?;
    let cwd = values
        .remove("cwd")
        .ok_or_else(|| anyhow!("pty-manager cwd is required"))?;
    let command = match values.remove("command-json") {
        Some(raw) if !raw.is_empty() => serde_json::from_str(&raw).context("parse command-json")?,
        _ => default_shell_command(),
    };

    Ok(Args {
        root: PathBuf::from(root),
        session,
        cwd: PathBuf::from(cwd),
        command,
    })
}

fn run_owner(args: Args) -> Result<()> {
    validate_session_name(&args.session)?;
    if args.command.is_empty() {
        bail!("pty-manager command is empty");
    }

    let paths = session_paths(&args.root, &args.session)?;
    create_private_dir(&args.root).context("create pty manager root dir")?;
    create_private_dir(&paths.dir).context("create session state dir")?;
    if let Some(socket_dir) = &paths.socket_dir {
        create_private_socket_dir(socket_dir).context("create fallback socket dir")?;
    }
    let mut cleanup = OwnerCleanup::new(paths.clone());

    let (listener, listener_addr) = bind_owner_listener(&paths)?;

    let pty_system = native_pty_system();
    let pair = pty_system.openpty(INITIAL_PTY_SIZE).context("open pty")?;

    let mut cmd = CommandBuilder::new(&args.command[0]);
    for arg in args.command.iter().skip(1) {
        cmd.arg(arg);
    }
    cmd.cwd(&args.cwd);
    strip_secret_env(&mut cmd);

    let mut child = pair.slave.spawn_command(cmd).context("spawn pty command")?;
    let killer = owner_killer(child.as_ref());
    cleanup.set_killer(killer.clone_killer());
    let mut reader = pair.master.try_clone_reader().context("clone pty reader")?;
    let writer = Arc::new(Mutex::new(
        pair.master.take_writer().context("take pty writer")?,
    ));
    let master = Arc::new(Mutex::new(pair.master));
    drop(pair.slave);

    let token = new_token();
    let cwd_string = args.cwd.to_string_lossy().to_string();
    write_state(
        &paths,
        &OwnerState {
            session: &args.session,
            addr: listener_addr,
            token: &token,
            cwd: &cwd_string,
            pid: std::process::id(),
            created_at: Utc::now(),
        },
    )?;

    let shared = Arc::new(Mutex::new(Shared {
        exit_code: -1,
        ..Shared::default()
    }));

    let reader_shared = Arc::clone(&shared);
    let reader_writer = Arc::clone(&writer);
    thread::spawn(move || {
        let mut buf = [0_u8; 32 * 1024];
        let mut terminal_response_state = TerminalResponseState::default();
        loop {
            match reader.read(&mut buf) {
                Ok(0) | Err(_) => break,
                Ok(n) => {
                    let chunk = &buf[..n];
                    if let Some(response) =
                        terminal_response_for_output(&mut terminal_response_state, chunk)
                        && let Ok(mut writer) = reader_writer.lock()
                    {
                        let _ = writer.write_all(response);
                        let _ = writer.flush();
                    }
                    broadcast(&reader_shared, chunk);
                }
            }
        }
        mark_reader_done(&reader_shared);
    });

    let wait_shared = Arc::clone(&shared);
    #[cfg(unix)]
    {
        let child_pid = child.process_id().context("spawned pty child has no pid")?;
        thread::spawn(move || {
            mark_child_exited(&wait_shared, wait_for_exit_without_reaping(child_pid));
        });
    }
    #[cfg(windows)]
    thread::spawn(move || {
        let exit_code = match child.wait() {
            Ok(status) => status.exit_code() as i32,
            Err(_) => -1,
        };
        mark_child_exited(&wait_shared, exit_code);
    });

    let connection_slots = ConnectionSlots::new(MAX_OWNER_CONNECTIONS);
    let mut completed_at = None;
    loop {
        if owner_should_exit(&shared, &mut completed_at) {
            break;
        }
        let stream = match listener.accept() {
            Ok((stream, _addr)) => stream,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => {
                thread::sleep(Duration::from_millis(25));
                continue;
            }
            Err(err) => return Err(err).context("accept connection"),
        };
        let Some(slot) = connection_slots.try_acquire() else {
            // At capacity: close the connection instead of spawning a thread.
            continue;
        };
        stream.set_nonblocking(false)?;
        let token = token.clone();
        let conn_shared = Arc::clone(&shared);
        let writer = Arc::clone(&writer);
        let master = Arc::clone(&master);
        let mut killer = killer.clone_killer();
        thread::spawn(move || {
            let _slot = slot;
            let _ = handle_conn(stream, &token, conn_shared, writer, master, &mut killer);
        });
    }

    #[cfg(unix)]
    reap_child(&shared, child.as_mut());
    cleanup.disarm_killer();
    Ok(())
}

// Observe the child's exit but leave it as a zombie. Until it is reaped the
// kernel cannot hand its PID, and therefore its process group ID, to another
// process, so stop can still kill descendants left in the group after the
// shell exits without any risk of signalling an unrelated group.
#[cfg(unix)]
fn wait_for_exit_without_reaping(pid: u32) -> i32 {
    // CLD_EXITED has the same value on Linux and macOS.
    const CLD_EXITED: i32 = 1;
    loop {
        // SAFETY: siginfo_t is plain old data, so all-zero bytes are valid.
        let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
        // SAFETY: `info` is a valid, writable siginfo_t for the whole call.
        let rc =
            unsafe { libc::waitid(libc::P_PID, pid, &mut info, libc::WEXITED | libc::WNOWAIT) };
        if rc == 0 {
            if info.si_code == CLD_EXITED {
                // SAFETY: waitid filled a SIGCHLD siginfo, so si_status is set.
                return unsafe { info.si_status() };
            }
            // Killed by a signal; portable-pty reports this as exit code 1.
            return 1;
        }
        if io::Error::last_os_error().kind() != io::ErrorKind::Interrupted {
            return -1;
        }
    }
}

// Reap under the shared lock so stop_owner never signals the group after its
// ID has been released for reuse.
#[cfg(unix)]
fn reap_child(shared: &Arc<Mutex<Shared>>, child: &mut (dyn portable_pty::Child + Send + Sync)) {
    let mut shared = shared.lock().expect("shared poisoned");
    if let Ok(Some(_)) = child.try_wait() {
        shared.child_reaped = true;
    }
}

fn handle_conn(
    stream: OwnerStream,
    token: &str,
    shared: Arc<Mutex<Shared>>,
    writer: Arc<Mutex<Box<dyn Write + Send>>>,
    master: Arc<Mutex<Box<dyn portable_pty::MasterPty + Send>>>,
    killer: &mut Box<dyn ChildKiller + Send + Sync>,
) -> Result<()> {
    let mut response_stream = stream.try_clone()?;
    let mut reader = BufReader::new(stream);
    let first = match read_first_request(&mut reader, OWNER_FIRST_REQUEST_TIMEOUT)? {
        Some(req) => req,
        None => return Ok(()),
    };
    if first.token != token {
        write_response(
            &mut response_stream,
            Response {
                kind: "error",
                ok: false,
                error: Some("invalid pty owner token".to_string()),
                exit_code: None,
                output: Vec::new(),
                title: None,
            },
        )?;
        return Ok(());
    }
    // Authenticated: an attachment may legitimately sit idle between requests.
    reader.get_ref().set_read_timeout(None)?;

    match first.kind.as_str() {
        "status" => {
            let (output, title) = {
                let shared = shared.lock().expect("shared poisoned");
                (shared.output.clone(), shared.title.clone())
            };
            write_response(
                &mut response_stream,
                ok_with_output_and_title(output, title),
            )?;
        }
        "stop" => {
            write_response(&mut response_stream, ok())?;
            stop_owner(&shared, killer);
        }
        "input" => {
            {
                let mut writer = writer.lock().expect("writer poisoned");
                writer.write_all(&first.data)?;
                writer.flush()?;
            }
            write_response(&mut response_stream, ok())?;
        }
        "resize" => {
            resize_pty(&master, first.pty_size());
            write_response(&mut response_stream, ok())?;
        }
        "attach" => handle_attach(
            response_stream,
            reader,
            token,
            first,
            AttachRuntime {
                shared,
                writer,
                master,
            },
            killer,
        )?,
        _ => {
            write_response(
                &mut response_stream,
                Response {
                    kind: "error",
                    ok: false,
                    error: Some("unknown pty owner request".to_string()),
                    exit_code: None,
                    output: Vec::new(),
                    title: None,
                },
            )?;
        }
    }
    Ok(())
}

fn handle_attach(
    mut stream: OwnerStream,
    mut reader: BufReader<OwnerStream>,
    token: &str,
    first: Request,
    runtime: AttachRuntime,
    killer: &mut Box<dyn ChildKiller + Send + Sync>,
) -> Result<()> {
    {
        let mut shared = runtime.shared.lock().expect("shared poisoned");
        if shared.attached {
            drop(shared);
            write_response(
                &mut stream,
                Response {
                    kind: "error",
                    ok: false,
                    error: Some("pty owner already has an active attachment".to_string()),
                    exit_code: None,
                    output: Vec::new(),
                    title: None,
                },
            )?;
            return Ok(());
        }
        shared.attached = true;
    }
    let mut active_attachment = ActiveAttachment {
        shared: Arc::clone(&runtime.shared),
        subscriber_id: None,
    };

    resize_pty(&runtime.master, first.pty_size());
    write_response(&mut stream, ok())?;

    let (tx, rx) = new_subscriber_channel();
    let subscription = {
        let mut shared = runtime.shared.lock().expect("shared poisoned");
        subscribe(&mut shared, tx)
    };
    match subscription {
        Subscription::Live { subscriber_id } => {
            active_attachment.subscriber_id = Some(subscriber_id);
        }
        Subscription::Complete { replay, exit_code } => {
            if !replay.is_empty() {
                write_response(&mut stream, ok_with_output(replay))?;
            }
            write_response(&mut stream, exit(exit_code))?;
            return Ok(());
        }
    }

    let mut output_stream = stream.try_clone()?;
    let shared_for_output = Arc::clone(&runtime.shared);
    thread::spawn(move || {
        for chunk in rx {
            if write_response(&mut output_stream, ok_with_output(chunk)).is_err() {
                return;
            }
        }
        let code = exit_code_after_output_close(&shared_for_output);
        let _ = write_response(&mut output_stream, exit(code));
        // Unblock the request loop so the attachment is released only after
        // the exit frame has been written.
        let _ = output_stream.shutdown(Shutdown::Both);
    });

    while let Some(req) = read_request(&mut reader, MAX_OWNER_REQUEST_SIZE)? {
        if req.token != token {
            return Ok(());
        }
        match req.kind.as_str() {
            "input" => {
                let mut writer = runtime.writer.lock().expect("writer poisoned");
                writer.write_all(&req.data)?;
                writer.flush()?;
            }
            "resize" if req.cols > 0 && req.rows > 0 => {
                resize_pty(&runtime.master, req.pty_size());
            }
            "stop" => {
                stop_owner(&runtime.shared, killer);
            }
            _ => {}
        }
    }
    Ok(())
}

fn resize_pty(master: &Arc<Mutex<Box<dyn portable_pty::MasterPty + Send>>>, mut size: PtySize) {
    if size.cols == 0 || size.rows == 0 {
        return;
    }

    let master = master.lock().expect("master poisoned");
    if (size.pixel_width == 0 || size.pixel_height == 0)
        && let Ok(current) = master.get_size()
    {
        if size.pixel_width == 0 {
            size.pixel_width = scale_pixel_dimension(current.pixel_width, current.cols, size.cols);
        }
        if size.pixel_height == 0 {
            size.pixel_height =
                scale_pixel_dimension(current.pixel_height, current.rows, size.rows);
        }
    }
    let _ = master.resize(size);
}

fn scale_pixel_dimension(pixels: u16, current_cells: u16, new_cells: u16) -> u16 {
    if pixels == 0 || current_cells == 0 {
        return pixels;
    }

    let current_cells = u64::from(current_cells);
    ((u64::from(pixels) * u64::from(new_cells) + current_cells / 2) / current_cells)
        .min(u64::from(u16::MAX)) as u16
}

// Bound how long an unauthenticated connection can hold its thread and slot
// before completing its first request; the Windows listener is reachable by
// any local user. The deadline covers the whole request, so a client cannot
// extend it by trickling bytes.
fn read_first_request(
    reader: &mut BufReader<OwnerStream>,
    timeout: Duration,
) -> Result<Option<Request>> {
    let deadline = Instant::now() + timeout;
    read_request_with(reader, MAX_OWNER_FIRST_REQUEST_SIZE, |reader| {
        let remaining = deadline.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "pty owner first request timed out",
            ));
        }
        reader.get_ref().set_read_timeout(Some(remaining))
    })
}

struct ConnectionSlots {
    in_use: Arc<AtomicUsize>,
    limit: usize,
}

struct ConnectionSlot {
    in_use: Arc<AtomicUsize>,
}

impl ConnectionSlots {
    fn new(limit: usize) -> Self {
        Self {
            in_use: Arc::new(AtomicUsize::new(0)),
            limit,
        }
    }

    fn try_acquire(&self) -> Option<ConnectionSlot> {
        self.in_use
            .try_update(Ordering::AcqRel, Ordering::Acquire, |in_use| {
                (in_use < self.limit).then_some(in_use + 1)
            })
            .ok()?;
        Some(ConnectionSlot {
            in_use: Arc::clone(&self.in_use),
        })
    }
}

impl Drop for ConnectionSlot {
    fn drop(&mut self) {
        self.in_use.fetch_sub(1, Ordering::AcqRel);
    }
}

fn read_request<R: BufRead>(reader: &mut R, max_bytes: usize) -> Result<Option<Request>> {
    read_request_with(reader, max_bytes, |_| Ok(()))
}

// `before_fill` runs before every fill_buf so callers can adjust the
// stream (for example its read timeout) ahead of each socket read.
fn read_request_with<R: BufRead>(
    reader: &mut R,
    max_bytes: usize,
    mut before_fill: impl FnMut(&R) -> io::Result<()>,
) -> Result<Option<Request>> {
    let mut line = Vec::new();
    loop {
        before_fill(reader)?;
        let available = reader.fill_buf()?;
        if available.is_empty() {
            if line.is_empty() {
                return Ok(None);
            }
            break;
        }

        let take = available
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(available.len(), |index| index + 1);
        if line.len() + take > max_bytes {
            bail!("pty owner request exceeds {max_bytes} bytes");
        }
        line.extend_from_slice(&available[..take]);
        reader.consume(take);
        if take > 0 && line.last() == Some(&b'\n') {
            break;
        }
    }

    let req: Request = serde_json::from_slice(&line)?;
    if req.kind == "input" && req.data.len() > MAX_OWNER_INPUT_SIZE {
        bail!("pty owner input exceeds {MAX_OWNER_INPUT_SIZE} bytes");
    }
    Ok(Some(req))
}

fn broadcast(shared: &Arc<Mutex<Shared>>, data: &[u8]) {
    let chunk = data.to_vec();
    let subscribers = {
        let mut shared = shared.lock().expect("shared poisoned");
        let title = update_terminal_title(&mut shared.title_parser, data);
        if title.is_some() {
            shared.title = title;
        }
        shared.output.extend_from_slice(data);
        if shared.output.len() > MAX_OUTPUT_REPLAY {
            let extra = shared.output.len() - MAX_OUTPUT_REPLAY;
            shared.output.drain(0..extra);
        }
        shared.subscribers.clone()
    };

    let mut failed_ids = Vec::new();
    for subscriber in subscribers {
        match subscriber.tx.try_send(chunk.clone()) {
            Ok(()) => {}
            Err(mpsc::TrySendError::Full(_)) | Err(mpsc::TrySendError::Disconnected(_)) => {
                failed_ids.push(subscriber.id);
            }
        }
    }
    if !failed_ids.is_empty() {
        let mut shared = shared.lock().expect("shared poisoned");
        shared
            .subscribers
            .retain(|subscriber| !failed_ids.contains(&subscriber.id));
    }
}

#[derive(Debug, Default)]
struct TitleParserState {
    pending: Vec<u8>,
}

const TITLE_PENDING_LIMIT: usize = 4096;

fn update_terminal_title(state: &mut TitleParserState, data: &[u8]) -> Option<String> {
    if data.is_empty() && state.pending.is_empty() {
        return None;
    }

    let mut buf = Vec::with_capacity(state.pending.len() + data.len());
    buf.extend_from_slice(&state.pending);
    buf.extend_from_slice(data);

    let (title, consumed) = parse_terminal_title(&buf);
    if consumed < buf.len() {
        state.pending = buf[consumed..].to_vec();
        if state.pending.len() > TITLE_PENDING_LIMIT {
            state
                .pending
                .drain(0..state.pending.len() - TITLE_PENDING_LIMIT);
        }
    } else {
        state.pending.clear();
    }
    title
}

fn parse_terminal_title(data: &[u8]) -> (Option<String>, usize) {
    let mut title = None;
    let mut consumed = 0;
    let mut i = 0;
    while i < data.len() {
        if data[i] == 0x1b && i + 1 >= data.len() {
            return (title, i);
        }
        if data[i] != 0x1b || data[i + 1] != b']' {
            consumed = i + 1;
            i += 1;
            continue;
        }

        let seq_start = i;
        let payload_start = i + 2;
        let mut terminator_start = None;
        let mut terminator_end = None;
        let mut j = payload_start;
        while j < data.len() {
            if data[j] == 0x07 {
                terminator_start = Some(j);
                terminator_end = Some(j + 1);
                break;
            }
            if data[j] == 0x1b && j + 1 < data.len() && data[j + 1] == b'\\' {
                terminator_start = Some(j);
                terminator_end = Some(j + 2);
                break;
            }
            j += 1;
        }

        let Some(term_start) = terminator_start else {
            return (title, seq_start);
        };
        let term_end = terminator_end.expect("terminal title terminator end");
        if let Some((code, value)) = split_osc_payload(&data[payload_start..term_start])
            && matches!(code, "0" | "1" | "2")
        {
            title = Some(value.trim().to_string());
        }
        consumed = term_end;
        i = term_end;
    }
    (title, consumed)
}

fn split_osc_payload(payload: &[u8]) -> Option<(&str, String)> {
    let semicolon = payload.iter().position(|byte| *byte == b';')?;
    let code = std::str::from_utf8(&payload[..semicolon]).ok()?;
    let value = String::from_utf8_lossy(&payload[semicolon + 1..]).to_string();
    Some((code, value))
}

#[derive(Default)]
struct TerminalResponseState {
    #[cfg(windows)]
    pending: Vec<u8>,
}

#[cfg(windows)]
fn terminal_response_for_output(
    state: &mut TerminalResponseState,
    data: &[u8],
) -> Option<&'static [u8]> {
    let mut combined = Vec::with_capacity(state.pending.len() + data.len());
    combined.extend_from_slice(&state.pending);
    combined.extend_from_slice(data);

    state.pending.clear();
    let pending_start = combined.len().saturating_sub(3);
    state.pending.extend_from_slice(&combined[pending_start..]);

    if combined.windows(4).any(|window| window == b"\x1b[6n") {
        return Some(b"\x1b[1;1R");
    }
    None
}

#[cfg(not(windows))]
fn terminal_response_for_output(
    _state: &mut TerminalResponseState,
    _data: &[u8],
) -> Option<&'static [u8]> {
    None
}

fn new_subscriber_channel() -> (mpsc::SyncSender<Vec<u8>>, mpsc::Receiver<Vec<u8>>) {
    mpsc::sync_channel(SUBSCRIBER_CHANNEL_CAPACITY)
}

enum Subscription {
    Live { subscriber_id: u64 },
    Complete { replay: Vec<u8>, exit_code: i32 },
}

// Runs under the shared lock, so the replay is queued before the subscriber
// becomes visible to broadcast and no newer chunk can overtake it; the Go
// owner's subscribe makes the same guarantee.
fn subscribe(shared: &mut Shared, tx: mpsc::SyncSender<Vec<u8>>) -> Subscription {
    if shared.exited && shared.reader_done {
        return Subscription::Complete {
            replay: shared.output.clone(),
            exit_code: shared.exit_code,
        };
    }
    if !shared.output.is_empty() {
        // A fresh channel with a live receiver always has room for one chunk.
        let _ = tx.try_send(shared.output.clone());
    }
    Subscription::Live {
        subscriber_id: add_subscriber(shared, tx),
    }
}

fn add_subscriber(shared: &mut Shared, tx: mpsc::SyncSender<Vec<u8>>) -> u64 {
    let id = shared.next_subscriber_id;
    shared.next_subscriber_id += 1;
    shared.subscribers.push(Subscriber { id, tx });
    id
}

// After the child exits, keep serving so the attached client can drain its
// output and exit frame, or a client that reconnects can still collect them.
// Leave once such an attachment ends, or after a bounded linger, matching the
// Go owner's waitAfterNaturalExit.
fn owner_should_exit(shared: &Arc<Mutex<Shared>>, completed_at: &mut Option<Instant>) -> bool {
    let shared = shared.lock().expect("shared poisoned");
    if shared.stopping {
        return true;
    }
    if !(shared.exited && shared.reader_done) {
        return false;
    }
    let completed_at = *completed_at.get_or_insert_with(Instant::now);
    (!shared.attached && shared.attachment_ended_after_exit)
        || completed_at.elapsed() >= OWNER_POST_EXIT_LINGER
}

fn stop_owner(shared: &Arc<Mutex<Shared>>, killer: &mut Box<dyn ChildKiller + Send + Sync>) {
    let subscribers = {
        let mut shared = shared.lock().expect("shared poisoned");
        // The shell can exit while descendants, such as a SIGHUP-ignoring
        // background job, stay in its process group, so the group kill must
        // run even after the child has exited. Until reap_child releases the
        // zombie, its PID cannot be reused, so the group ID still names only
        // this session's processes. Signal before setting `stopping`, and
        // under the lock, so the owner cannot reap in between.
        if !shared.child_reaped {
            let _ = killer.kill();
        }
        shared.stopping = true;
        std::mem::take(&mut shared.subscribers)
    };
    drop(subscribers);
}

fn mark_child_exited(shared: &Arc<Mutex<Shared>>, exit_code: i32) {
    let subscribers = {
        let mut shared = shared.lock().expect("shared poisoned");
        shared.exit_code = exit_code;
        shared.exited = true;
        take_subscribers_if_complete(&mut shared)
    };
    drop(subscribers);
}

fn mark_reader_done(shared: &Arc<Mutex<Shared>>) {
    let subscribers = {
        let mut shared = shared.lock().expect("shared poisoned");
        shared.reader_done = true;
        take_subscribers_if_complete(&mut shared)
    };
    drop(subscribers);
}

// A subscriber's channel also closes when broadcast drops it for falling
// behind, while the child may still be running. Give the child a short grace
// period to exit so the exit frame carries its real code instead of the -1
// placeholder, matching the Go owner's exitCodeAfterOutputClose.
fn exit_code_after_output_close(shared: &Arc<Mutex<Shared>>) -> i32 {
    let deadline = Instant::now() + OWNER_EXIT_CODE_GRACE_PERIOD;
    loop {
        {
            let shared = shared.lock().expect("shared poisoned");
            if shared.exited || Instant::now() >= deadline {
                return shared.exit_code;
            }
        }
        thread::sleep(Duration::from_millis(10));
    }
}

fn take_subscribers_if_complete(shared: &mut Shared) -> Vec<Subscriber> {
    if shared.exited && shared.reader_done {
        std::mem::take(&mut shared.subscribers)
    } else {
        Vec::new()
    }
}

// portable-pty's cloned killer only sends SIGHUP to the child PID, which a
// shell can ignore and which never reaches its background jobs. The child is
// a session leader (portable-pty calls setsid), so its PID is also its
// process group; SIGKILL the whole group like the Go owner does.
#[cfg(unix)]
fn owner_killer(
    child: &(dyn portable_pty::Child + Send + Sync),
) -> Box<dyn ChildKiller + Send + Sync> {
    match child.process_id() {
        Some(pid) => Box::new(ProcessGroupKiller { pid }),
        None => child.clone_killer(),
    }
}

#[cfg(windows)]
fn owner_killer(
    child: &(dyn portable_pty::Child + Send + Sync),
) -> Box<dyn ChildKiller + Send + Sync> {
    child.clone_killer()
}

#[cfg(unix)]
#[derive(Clone, Debug)]
struct ProcessGroupKiller {
    pid: u32,
}

#[cfg(unix)]
impl ChildKiller for ProcessGroupKiller {
    fn kill(&mut self) -> io::Result<()> {
        kill_process_group(self.pid)
    }

    fn clone_killer(&self) -> Box<dyn ChildKiller + Send + Sync> {
        Box::new(self.clone())
    }
}

#[cfg(unix)]
fn kill_process_group(pid: u32) -> io::Result<()> {
    unsafe extern "C" {
        fn kill(pid: i32, sig: i32) -> i32;
    }
    const SIGKILL: i32 = 9;
    const ESRCH: i32 = 3;

    // Process group 0 or 1 would signal this process's own group or every
    // process the user owns, never a child session.
    let pgid = i32::try_from(pid)
        .ok()
        .filter(|pgid| *pgid > 1)
        .ok_or_else(|| {
            io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("invalid child pid {pid}"),
            )
        })?;
    // SAFETY: kill takes plain integers and does not dereference pointers.
    if unsafe { kill(-pgid, SIGKILL) } == 0 {
        return Ok(());
    }
    let err = io::Error::last_os_error();
    if err.raw_os_error() == Some(ESRCH) {
        // The group is already gone.
        return Ok(());
    }
    Err(err)
}

fn ok<'a>() -> Response<'a> {
    Response {
        kind: "ok",
        ok: true,
        error: None,
        exit_code: None,
        output: Vec::new(),
        title: None,
    }
}

fn ok_with_output<'a>(output: Vec<u8>) -> Response<'a> {
    ok_with_output_and_title(output, None)
}

fn ok_with_output_and_title<'a>(output: Vec<u8>, title: Option<String>) -> Response<'a> {
    Response {
        kind: "output",
        ok: true,
        error: None,
        exit_code: None,
        output,
        title,
    }
}

fn exit<'a>(exit_code: i32) -> Response<'a> {
    Response {
        kind: "exit",
        ok: true,
        error: None,
        exit_code: Some(exit_code),
        output: Vec::new(),
        title: None,
    }
}

fn write_response(stream: &mut OwnerStream, response: Response<'_>) -> Result<()> {
    serde_json::to_writer(&mut *stream, &response)?;
    stream.write_all(b"\n")?;
    stream.flush()?;
    Ok(())
}

#[cfg(unix)]
fn bind_owner_listener(paths: &SessionPaths) -> Result<(OwnerListener, String)> {
    let _ = fs::remove_file(&paths.socket);
    let listener = UnixListener::bind(&paths.socket)
        .with_context(|| format!("listen on {}", paths.socket.display()))?;
    listener.set_nonblocking(true)?;
    Ok((listener, format!("unix://{}", paths.socket.display())))
}

#[cfg(windows)]
fn bind_owner_listener(_paths: &SessionPaths) -> Result<(OwnerListener, String)> {
    let listener = TcpListener::bind(("127.0.0.1", 0)).context("listen on tcp loopback")?;
    listener.set_nonblocking(true)?;
    let addr = listener.local_addr().context("read tcp listener address")?;
    Ok((listener, format!("tcp://{addr}")))
}

fn write_state(paths: &SessionPaths, state: &OwnerState<'_>) -> Result<()> {
    let data = serde_json::to_vec_pretty(state)?;
    let tmp = paths.state_path.with_extension("json.tmp");
    {
        let mut file = private_state_file(&tmp)?;
        file.write_all(&data)?;
        file.sync_all()?;
    }
    fs::rename(tmp, &paths.state_path)?;
    #[cfg(unix)]
    fs::set_permissions(&paths.state_path, fs::Permissions::from_mode(0o600))?;
    Ok(())
}

#[cfg(unix)]
fn private_state_file(path: &Path) -> Result<fs::File> {
    Ok(OpenOptions::new()
        .create(true)
        .truncate(true)
        .write(true)
        .mode(0o600)
        .open(path)?)
}

#[cfg(windows)]
fn private_state_file(path: &Path) -> Result<fs::File> {
    // The session directory is created with a protected inheritable DACL, so
    // files created inside it inherit the same current-user-only access. Calling
    // SetFileSecurityW again for this long state-file path fails on Windows.
    Ok(OpenOptions::new()
        .create(true)
        .truncate(true)
        .write(true)
        .open(path)?)
}

fn create_private_dir(path: &Path) -> Result<()> {
    fs::create_dir_all(path)?;
    #[cfg(unix)]
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))?;
    #[cfg(windows)]
    restrict_windows_acl(path, true).with_context(|| {
        format!(
            "restrict permissions on pty owner state directory {}",
            path.display()
        )
    })?;
    Ok(())
}

#[cfg(unix)]
fn create_private_socket_dir(path: &Path) -> Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.mode(0o700);
    match builder.create(path) {
        Ok(()) => return Ok(()),
        Err(err) if err.kind() == io::ErrorKind::AlreadyExists => {}
        Err(err) => return Err(err).with_context(|| format!("create {}", path.display())),
    }

    let metadata =
        fs::symlink_metadata(path).with_context(|| format!("inspect {}", path.display()))?;
    if metadata.file_type().is_symlink() {
        bail!("refusing fallback socket dir symlink {}", path.display());
    }
    if !metadata.is_dir() {
        bail!(
            "fallback socket path is not a directory: {}",
            path.display()
        );
    }
    let current_uid = current_uid();
    if metadata.uid() != current_uid {
        bail!(
            "fallback socket dir {} is owned by uid {}, not current uid {}",
            path.display(),
            metadata.uid(),
            current_uid
        );
    }
    if metadata.mode() & 0o077 != 0 {
        bail!(
            "fallback socket dir {} has permissions {:o}; expected private directory",
            path.display(),
            metadata.mode() & 0o777
        );
    }
    Ok(())
}

#[cfg(windows)]
fn create_private_socket_dir(path: &Path) -> Result<()> {
    create_private_dir(path)
}

#[cfg(unix)]
fn current_uid() -> u32 {
    unsafe extern "C" {
        fn geteuid() -> u32;
    }
    // SAFETY: geteuid has no preconditions and does not dereference pointers.
    unsafe { geteuid() }
}

#[cfg(windows)]
fn restrict_windows_acl(path: &Path, directory: bool) -> Result<()> {
    let current_user_sid = current_user_sid_string()?;
    let sddl = private_windows_sddl(&current_user_sid, directory);
    let sddl_wide = wide_null(&sddl);
    let path_wide = wide_path_null(path);
    let mut descriptor: PSECURITY_DESCRIPTOR = ptr::null_mut();

    // SAFETY: `sddl_wide` and `path_wide` are nul-terminated UTF-16 buffers
    // that live for the duration of the FFI calls. Windows allocates the
    // descriptor for the SDDL conversion, and we release it with LocalFree.
    unsafe {
        if ConvertStringSecurityDescriptorToSecurityDescriptorW(
            sddl_wide.as_ptr(),
            SDDL_REVISION_1,
            &mut descriptor,
            ptr::null_mut(),
        ) == 0
        {
            bail!(
                "build private security descriptor for {}: windows error {}",
                path.display(),
                GetLastError()
            );
        }

        let security_info = DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION;
        let ok = SetFileSecurityW(path_wide.as_ptr(), security_info, descriptor);
        let err = GetLastError();
        let _ = LocalFree(descriptor as _);
        if ok == 0 {
            bail!(
                "apply private security descriptor to {}: windows error {}",
                path.display(),
                err
            );
        }
    }

    Ok(())
}

#[cfg(windows)]
fn private_windows_sddl(current_user_sid: &str, directory: bool) -> String {
    let inheritance = if directory { "OICI" } else { "" };
    format!("D:P(A;{inheritance};FA;;;{current_user_sid})")
}

#[cfg(windows)]
fn current_user_sid_string() -> Result<String> {
    let mut token = ptr::null_mut();
    // SAFETY: GetCurrentProcess returns a pseudo-handle. OpenProcessToken writes
    // a real token handle into `token`, which we close before returning.
    unsafe {
        if OpenProcessToken(GetCurrentProcess(), TOKEN_QUERY, &mut token) == 0 {
            bail!("open process token: windows error {}", GetLastError());
        }
    }
    let _guard = HandleGuard(token);

    let mut needed = 0;
    // SAFETY: First call asks Windows for the required buffer size.
    unsafe {
        let _ = GetTokenInformation(token, TokenUser, ptr::null_mut(), 0, &mut needed);
    }
    if needed == 0 {
        // SAFETY: GetLastError reads thread-local Windows error state.
        unsafe {
            bail!(
                "query current user token size: windows error {}",
                GetLastError()
            );
        }
    }

    let mut buffer = vec![0_u8; needed as usize];
    // SAFETY: `buffer` is sized from the previous GetTokenInformation call and
    // is valid for writes of `needed` bytes.
    unsafe {
        if GetTokenInformation(
            token,
            TokenUser,
            buffer.as_mut_ptr().cast(),
            needed,
            &mut needed,
        ) == 0
        {
            bail!("query current user token: windows error {}", GetLastError());
        }

        let user = &*(buffer.as_ptr().cast::<TOKEN_USER>());
        let mut sid_ptr = ptr::null_mut();
        if ConvertSidToStringSidW(user.User.Sid, &mut sid_ptr) == 0 {
            bail!("convert current user SID: windows error {}", GetLastError());
        }
        let sid = wide_ptr_to_string(sid_ptr);
        let _ = LocalFree(sid_ptr as _);
        Ok(sid)
    }
}

#[cfg(windows)]
struct HandleGuard(windows_sys::Win32::Foundation::HANDLE);

#[cfg(windows)]
impl Drop for HandleGuard {
    fn drop(&mut self) {
        // SAFETY: HandleGuard owns this handle after successful OpenProcessToken.
        unsafe {
            let _ = CloseHandle(self.0);
        }
    }
}

#[cfg(windows)]
fn wide_null(value: &str) -> Vec<u16> {
    value.encode_utf16().chain(std::iter::once(0)).collect()
}

#[cfg(windows)]
fn wide_path_null(path: &Path) -> Vec<u16> {
    windows_acl_path(path)
        .encode_wide()
        .chain(std::iter::once(0))
        .collect()
}

#[cfg(windows)]
fn windows_acl_path(path: &Path) -> OsString {
    let raw = path.as_os_str().to_string_lossy();
    if raw.starts_with(r"\\?\") || raw.starts_with(r"\??\") {
        return path.as_os_str().to_os_string();
    }
    if let Some(unc) = raw.strip_prefix(r"\\") {
        return OsString::from(format!(r"\\?\UNC\{unc}"));
    }
    if raw.len() >= 3 && raw.as_bytes()[1] == b':' && raw.as_bytes()[2] == b'\\' {
        return OsString::from(format!(r"\\?\{raw}"));
    }
    path.as_os_str().to_os_string()
}

#[cfg(windows)]
unsafe fn wide_ptr_to_string(ptr: windows_sys::core::PWSTR) -> String {
    let mut len = 0;
    while unsafe { *ptr.add(len) } != 0 {
        len += 1;
    }
    let slice = unsafe { std::slice::from_raw_parts(ptr, len) };
    String::from_utf16_lossy(slice)
}

impl OwnerCleanup {
    fn new(paths: SessionPaths) -> Self {
        Self {
            paths,
            killer: None,
        }
    }

    fn set_killer(&mut self, killer: Box<dyn ChildKiller + Send + Sync>) {
        self.killer = Some(killer);
    }

    fn disarm_killer(&mut self) {
        self.killer = None;
    }
}

impl Drop for OwnerCleanup {
    fn drop(&mut self) {
        if let Some(killer) = &mut self.killer {
            let _ = killer.kill();
        }
        let _ = fs::remove_file(&self.paths.socket);
        if let Some(socket_dir) = &self.paths.socket_dir {
            let _ = fs::remove_dir_all(socket_dir);
        }
        let _ = fs::remove_dir_all(&self.paths.dir);
    }
}

impl Drop for ActiveAttachment {
    fn drop(&mut self) {
        let mut shared = self.shared.lock().expect("shared poisoned");
        if let Some(subscriber_id) = self.subscriber_id {
            shared
                .subscribers
                .retain(|subscriber| subscriber.id != subscriber_id);
        }
        shared.attached = false;
        if shared.exited && shared.reader_done {
            shared.attachment_ended_after_exit = true;
        }
    }
}

fn session_paths(root: &Path, session: &str) -> Result<SessionPaths> {
    validate_session_name(session)?;
    let dir = root.join(session_dir_name(session));
    let mut socket = root.join(format!("sock-{}", socket_hash(session)));
    let mut socket_dir = None;
    if socket.to_string_lossy().len() > MAX_UNIX_SOCKET_PATH_LEN {
        let fallback_dir = fallback_socket_dir(root, session, &env::temp_dir());
        socket = fallback_dir.join("sock");
        socket_dir = Some(fallback_dir);
    }
    ensure_socket_path_fits(&socket)?;
    Ok(SessionPaths {
        state_path: dir.join("owner.json"),
        dir,
        socket,
        socket_dir,
    })
}

fn fallback_socket_dir(root: &Path, session: &str, primary_temp_dir: &Path) -> PathBuf {
    fallback_socket_dir_for_platform(root, session, primary_temp_dir, cfg!(target_os = "macos"))
}

fn fallback_socket_dir_for_platform(
    root: &Path,
    session: &str,
    primary_temp_dir: &Path,
    include_darwin_private_tmp: bool,
) -> PathBuf {
    let dir_name = format!(
        "kenn-forge-pty-{}",
        socket_hash(&format!("{}-{session}", root.display()))
    );
    let mut bases = vec![primary_temp_dir];
    if include_darwin_private_tmp {
        bases.push(Path::new("/private/tmp"));
    }
    bases.push(Path::new("/tmp"));
    for base in bases {
        let candidate = base.join(&dir_name);
        if candidate.join("sock").to_string_lossy().len() <= MAX_UNIX_SOCKET_PATH_LEN {
            return candidate;
        }
    }
    Path::new("/tmp").join(dir_name)
}

fn session_dir_name(session: &str) -> String {
    if session.contains(['<', '>', ':', '"', '|', '?', '*']) {
        format!("session-{}", socket_hash(session))
    } else {
        session.to_string()
    }
}

fn ensure_socket_path_fits(socket: &Path) -> Result<()> {
    if socket.to_string_lossy().len() > MAX_UNIX_SOCKET_PATH_LEN {
        bail!(
            "pty manager socket path is too long for Unix sockets: {}",
            socket.display()
        );
    }
    Ok(())
}

fn validate_session_name(session: &str) -> Result<()> {
    if session.is_empty()
        || session.contains("..")
        || session.contains('/')
        || session.contains('\\')
        || session.contains('\0')
    {
        bail!("unsafe pty owner session name {session:?}");
    }
    Ok(())
}

fn socket_hash(value: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(value.as_bytes());
    let digest = hasher.finalize();
    digest[..8].iter().map(|b| format!("{b:02x}")).collect()
}

fn new_token() -> String {
    let mut data = [0_u8; 16];
    rand::rng().fill_bytes(&mut data);
    data.iter().map(|b| format!("{b:02x}")).collect()
}

fn default_shell_command() -> Vec<String> {
    env::var("SHELL")
        .map(|shell| vec![shell])
        .unwrap_or_else(|_| vec!["/bin/sh".to_string()])
}

fn strip_secret_env(cmd: &mut CommandBuilder) {
    for (key, _) in env::vars() {
        if key == "KENN_FORGE_GITHUB_TOKEN"
            || key == "GITHUB_TOKEN"
            || key == "GH_TOKEN"
            || key == "GH_PAT"
            || key == "GITHUB_PAT"
            || key == "GITHUB_ENTERPRISE_TOKEN"
            || key == "GH_ENTERPRISE_TOKEN"
            || key.starts_with("KENN_FORGE_GITHUB_TOKEN_")
            || key.starts_with("GITHUB_TOKEN_")
            || key.starts_with("GH_TOKEN_")
            || key.starts_with("GH_PAT_")
            || key.starts_with("GITHUB_PAT_")
            || key.starts_with("GITHUB_ENTERPRISE_TOKEN_")
            || key.starts_with("GH_ENTERPRISE_TOKEN_")
        {
            cmd.env_remove(key);
        }
    }
}

fn is_false(value: &bool) -> bool {
    !*value
}

fn serialize_base64_bytes<S>(bytes: &[u8], serializer: S) -> Result<S::Ok, S::Error>
where
    S: Serializer,
{
    serializer.serialize_str(&BASE64.encode(bytes))
}

fn deserialize_base64_bytes<'de, D>(deserializer: D) -> Result<Vec<u8>, D::Error>
where
    D: Deserializer<'de>,
{
    let value = Option::<String>::deserialize(deserializer)?;
    value
        .map(|data| BASE64.decode(data).map_err(serde::de::Error::custom))
        .transpose()
        .map(|data| data.unwrap_or_default())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(unix)]
    use serde_json::Value;
    use serde_json::json;
    use std::io::Cursor;
    use std::sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    };

    #[derive(Clone, Debug)]
    struct RecordingKiller {
        calls: Arc<AtomicUsize>,
    }

    impl ChildKiller for RecordingKiller {
        fn kill(&mut self) -> io::Result<()> {
            self.calls.fetch_add(1, Ordering::SeqCst);
            Ok(())
        }

        fn clone_killer(&self) -> Box<dyn ChildKiller + Send + Sync> {
            Box::new(self.clone())
        }
    }

    #[test]
    fn request_decodes_go_base64_bytes() {
        let req: Request = serde_json::from_value(json!({
            "type": "input",
            "token": "secret",
            "data": "aGVsbG8K"
        }))
        .unwrap();

        assert_eq!(req.kind, "input");
        assert_eq!(req.data, b"hello\n");
    }

    #[test]
    fn request_decodes_go_pixel_geometry() {
        let req: Request = serde_json::from_value(json!({
            "type": "resize",
            "cols": 132,
            "rows": 43,
            "pixel_width": 1056,
            "pixel_height": 688
        }))
        .unwrap();

        assert_eq!(
            req.pty_size(),
            PtySize {
                rows: 43,
                cols: 132,
                pixel_width: 1056,
                pixel_height: 688,
            }
        );
    }

    #[test]
    fn retained_pty_preserves_pixel_geometry_across_character_only_resize() {
        let pair = native_pty_system().openpty(INITIAL_PTY_SIZE).unwrap();
        let initial_size = pair.master.get_size().unwrap();
        #[cfg(unix)]
        assert_eq!(initial_size, INITIAL_PTY_SIZE);
        #[cfg(windows)]
        assert_eq!(
            (initial_size.rows, initial_size.cols),
            (INITIAL_PTY_SIZE.rows, INITIAL_PTY_SIZE.cols)
        );

        let master = Arc::new(Mutex::new(pair.master));
        let browser_size = PtySize {
            rows: 43,
            cols: 132,
            pixel_width: 1056,
            pixel_height: 688,
        };
        resize_pty(&master, browser_size);

        let resized = master.lock().expect("master poisoned").get_size().unwrap();
        #[cfg(unix)]
        assert_eq!(resized, browser_size);
        #[cfg(windows)]
        assert_eq!(
            (resized.rows, resized.cols),
            (browser_size.rows, browser_size.cols)
        );

        let character_only_size = PtySize {
            rows: 50,
            cols: 100,
            pixel_width: 0,
            pixel_height: 0,
        };
        resize_pty(&master, character_only_size);

        let resized = master.lock().expect("master poisoned").get_size().unwrap();
        #[cfg(unix)]
        assert_eq!(
            resized,
            PtySize {
                rows: 50,
                cols: 100,
                pixel_width: 800,
                pixel_height: 800,
            }
        );
        #[cfg(windows)]
        assert_eq!(
            (resized.rows, resized.cols),
            (character_only_size.rows, character_only_size.cols)
        );
    }

    #[test]
    fn pixel_scaling_preserves_non_integral_resize_round_trip() {
        let expanded = scale_pixel_dimension(875, 100, 101);

        assert_eq!(expanded, 884);
        assert_eq!(scale_pixel_dimension(expanded, 101, 100), 875);
    }

    #[test]
    fn response_encodes_go_base64_bytes() {
        let raw = serde_json::to_value(ok_with_output(b"ready".to_vec())).unwrap();

        assert_eq!(raw["output"], "cmVhZHk=");
    }

    #[test]
    fn read_request_rejects_oversized_frames_before_decode() {
        let frame = format!(
            "{{\"type\":\"status\",\"token\":\"{}\"}}\n",
            "x".repeat(MAX_OWNER_FIRST_REQUEST_SIZE)
        );
        let mut reader = BufReader::new(Cursor::new(frame.into_bytes()));

        let err = read_request(&mut reader, MAX_OWNER_FIRST_REQUEST_SIZE).unwrap_err();

        assert!(err.to_string().contains("pty owner request exceeds"));
    }

    #[test]
    fn read_request_rejects_oversized_input_data() {
        let data = BASE64.encode(vec![b'x'; MAX_OWNER_INPUT_SIZE + 1]);
        let frame = format!("{{\"type\":\"input\",\"token\":\"secret\",\"data\":\"{data}\"}}\n");
        let mut reader = BufReader::new(Cursor::new(frame.into_bytes()));

        let err = read_request(&mut reader, MAX_OWNER_REQUEST_SIZE).unwrap_err();

        assert!(err.to_string().contains("pty owner input exceeds"));
    }

    #[test]
    fn paths_reject_unsafe_sessions() {
        for session in ["", "../ws", "a/b", "a\\b", "a\0b"] {
            assert!(session_paths(Path::new("/tmp/root"), session).is_err());
        }
    }

    #[test]
    fn paths_match_go_hash_shape() {
        let paths = session_paths(Path::new("/tmp/root"), "kenn-forge-abc123").unwrap();

        assert_eq!(paths.dir, Path::new("/tmp/root/kenn-forge-abc123"));
        assert_eq!(paths.socket, Path::new("/tmp/root/sock-5e73279a62064378"));
        assert!(paths.socket_dir.is_none());
        assert_eq!(
            paths.state_path,
            Path::new("/tmp/root/kenn-forge-abc123/owner.json")
        );
    }

    #[test]
    fn paths_hash_filesystem_hostile_sessions() {
        let paths = session_paths(Path::new("/tmp/root"), "ws-1:codex").unwrap();
        let dir = Path::new("/tmp/root").join("session-8dc56ff7cc3fbfcb");

        assert_eq!(paths.dir, dir);
        assert_eq!(paths.state_path, dir.join("owner.json"));
    }

    #[test]
    fn paths_use_private_temp_socket_dir_for_long_roots() {
        let root = PathBuf::from("/tmp").join("x".repeat(MAX_UNIX_SOCKET_PATH_LEN));

        let paths = session_paths(&root, "kenn-forge-abc123").unwrap();

        assert_eq!(paths.dir, root.join("kenn-forge-abc123"));
        let socket_dir = paths.socket_dir.as_ref().unwrap();
        let temp_socket = env::temp_dir()
            .join(format!(
                "kenn-forge-pty-{}",
                socket_hash(&format!("{}-kenn-forge-abc123", root.display()))
            ))
            .join("sock");
        if temp_socket.to_string_lossy().len() <= MAX_UNIX_SOCKET_PATH_LEN {
            assert!(socket_dir.starts_with(env::temp_dir()));
        }
        let expected_file_name = format!(
            "kenn-forge-pty-{}",
            socket_hash(&format!("{}-kenn-forge-abc123", root.display()))
        );
        assert_eq!(
            socket_dir.file_name().and_then(|name| name.to_str()),
            Some(expected_file_name.as_str())
        );
        assert_eq!(paths.socket, socket_dir.join("sock"));
        assert!(paths.socket.to_string_lossy().len() <= MAX_UNIX_SOCKET_PATH_LEN);
    }

    #[test]
    fn fallback_socket_dir_uses_private_tmp_when_temp_dir_is_too_long() {
        let root = PathBuf::from("/tmp").join("x".repeat(MAX_UNIX_SOCKET_PATH_LEN));
        let long_temp_dir = PathBuf::from("/tmp").join("long-temp-root-".repeat(8));

        let socket_dir =
            fallback_socket_dir_for_platform(&root, "kenn-forge-abc123", &long_temp_dir, true);

        assert_eq!(
            socket_dir,
            Path::new("/private/tmp").join(format!(
                "kenn-forge-pty-{}",
                socket_hash(&format!("{}-kenn-forge-abc123", root.display()))
            ))
        );
        assert!(socket_dir.join("sock").to_string_lossy().len() <= MAX_UNIX_SOCKET_PATH_LEN);
    }

    #[test]
    fn fallback_socket_dir_skips_private_tmp_off_macos() {
        let root = PathBuf::from("/tmp").join("x".repeat(MAX_UNIX_SOCKET_PATH_LEN));
        let long_temp_dir = PathBuf::from("/tmp").join("long-temp-root-".repeat(8));

        let socket_dir =
            fallback_socket_dir_for_platform(&root, "kenn-forge-abc123", &long_temp_dir, false);

        assert_eq!(
            socket_dir,
            Path::new("/tmp").join(format!(
                "kenn-forge-pty-{}",
                socket_hash(&format!("{}-kenn-forge-abc123", root.display()))
            ))
        );
        assert!(socket_dir.join("sock").to_string_lossy().len() <= MAX_UNIX_SOCKET_PATH_LEN);
    }

    #[cfg(unix)]
    #[test]
    fn create_private_socket_dir_rejects_symlink() {
        let parent = env::temp_dir().join(format!("mm-pty-symlink-test-{}", new_token()));
        let target = parent.join("target");
        let socket_dir = parent.join("kenn-forge-pty-symlink");
        fs::create_dir_all(&target).unwrap();
        std::os::unix::fs::symlink(&target, &socket_dir).unwrap();

        let err = create_private_socket_dir(&socket_dir).unwrap_err();

        assert!(
            err.to_string()
                .contains("refusing fallback socket dir symlink")
        );
        let _ = fs::remove_dir_all(&parent);
    }

    #[cfg(unix)]
    #[test]
    fn create_private_socket_dir_rejects_shared_existing_dir() {
        let socket_dir = env::temp_dir().join(format!("mm-pty-shared-test-{}", new_token()));
        fs::create_dir(&socket_dir).unwrap();
        fs::set_permissions(&socket_dir, fs::Permissions::from_mode(0o755)).unwrap();

        let err = create_private_socket_dir(&socket_dir).unwrap_err();

        assert!(err.to_string().contains("expected private directory"));
        let _ = fs::remove_dir_all(&socket_dir);
    }

    #[cfg(windows)]
    #[test]
    fn owner_listener_uses_tcp_loopback_on_windows() {
        let root = env::temp_dir().join(format!("mm-pty-listener-test-{}", new_token()));
        let paths = session_paths(&root, "kenn-forge-abc123").unwrap();
        create_private_dir(&paths.dir).unwrap();

        let (_listener, addr) = bind_owner_listener(&paths).unwrap();

        assert!(addr.starts_with("tcp://127.0.0.1:"));
        assert!(!paths.socket.exists());

        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(windows)]
    #[test]
    fn windows_acl_paths_use_extended_length_prefix() {
        let path = Path::new(r"C:\tmp")
            .join("long-owner-root-".repeat(8))
            .join("kenn-forge-abc123")
            .join("owner.json.tmp");

        assert_eq!(
            windows_acl_path(&path).to_string_lossy(),
            format!(r"\\?\{}", path.display())
        );
    }

    #[cfg(windows)]
    #[test]
    fn conpty_cursor_position_requests_get_default_response() {
        let mut state = TerminalResponseState::default();
        assert_eq!(
            terminal_response_for_output(&mut state, b"\x1b[6n"),
            Some(&b"\x1b[1;1R"[..])
        );
        assert_eq!(terminal_response_for_output(&mut state, b"ready"), None);
    }

    #[cfg(windows)]
    #[test]
    fn conpty_cursor_position_requests_can_span_reads() {
        let mut state = TerminalResponseState::default();
        assert_eq!(terminal_response_for_output(&mut state, b"abc\x1b"), None);
        assert_eq!(terminal_response_for_output(&mut state, b"[6"), None);
        assert_eq!(
            terminal_response_for_output(&mut state, b"nxyz"),
            Some(&b"\x1b[1;1R"[..])
        );
    }

    #[cfg(unix)]
    #[test]
    fn state_files_are_private() {
        let root = Path::new("/tmp").join(format!("mm-pty-test-{}", new_token()));
        let paths = session_paths(&root, "kenn-forge-abc123").unwrap();
        create_private_dir(&paths.dir).unwrap();
        write_state(
            &paths,
            &OwnerState {
                session: "kenn-forge-abc123",
                addr: "unix:///tmp/forge.sock".to_string(),
                token: "secret",
                cwd: "/tmp/work",
                pid: 123,
                created_at: Utc::now(),
            },
        )
        .unwrap();

        let dir_mode = fs::metadata(&paths.dir).unwrap().permissions().mode() & 0o777;
        let state_mode = fs::metadata(&paths.state_path)
            .unwrap()
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(dir_mode, 0o700);
        assert_eq!(state_mode, 0o600);

        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn owner_cleanup_kills_child_and_removes_paths_on_drop() {
        let root = env::temp_dir().join(format!(
            "mm-pty-cleanup-test-{}-{}",
            new_token(),
            "x".repeat(MAX_UNIX_SOCKET_PATH_LEN)
        ));
        let paths = session_paths(&root, "kenn-forge-abc123").unwrap();
        create_private_dir(&paths.dir).unwrap();
        if let Some(socket_dir) = &paths.socket_dir {
            create_private_dir(socket_dir).unwrap();
        }
        fs::write(&paths.socket, b"not a real socket").unwrap();

        let kill_calls = Arc::new(AtomicUsize::new(0));
        {
            let mut cleanup = OwnerCleanup::new(paths.clone());
            cleanup.set_killer(Box::new(RecordingKiller {
                calls: Arc::clone(&kill_calls),
            }));
        }

        assert_eq!(kill_calls.load(Ordering::SeqCst), 1);
        assert!(!paths.socket.exists());
        assert!(!paths.dir.exists());

        if root.exists() {
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[test]
    fn child_exit_keeps_subscribers_open_until_reader_drains() {
        let (tx, rx) = mpsc::sync_channel(1);
        let shared = Arc::new(Mutex::new(Shared {
            subscribers: vec![Subscriber { id: 1, tx }],
            next_subscriber_id: 2,
            exit_code: -1,
            ..Shared::default()
        }));

        mark_child_exited(&shared, 7);
        assert!(rx.try_recv().is_err());

        broadcast(&shared, b"final");
        assert_eq!(rx.recv().unwrap(), b"final");

        mark_reader_done(&shared);
        assert!(rx.recv().is_err());
        assert_eq!(shared.lock().expect("shared poisoned").exit_code, 7);
    }

    #[cfg(unix)]
    fn process_exists(pid: i32) -> bool {
        unsafe extern "C" {
            fn kill(pid: i32, sig: i32) -> i32;
        }
        // SAFETY: signal 0 only checks that the process exists.
        unsafe { kill(pid, 0) == 0 }
    }

    #[cfg(unix)]
    #[test]
    fn owner_killer_kills_hup_ignoring_child_and_background_jobs() {
        let pair = native_pty_system().openpty(INITIAL_PTY_SIZE).unwrap();
        let mut cmd = CommandBuilder::new("/bin/sh");
        // exec keeps the shell from forking while the group kill lands; a
        // child mid-fork can miss a process-group signal on macOS.
        cmd.args([
            "-c",
            "trap '' HUP; sleep 30 & echo \"bg=$!\"; exec sleep 30",
        ]);
        let mut child = pair.slave.spawn_command(cmd).unwrap();
        drop(pair.slave);
        let mut reader = pair.master.try_clone_reader().unwrap();
        let (line_tx, line_rx) = mpsc::channel();
        thread::spawn(move || {
            let mut output = Vec::new();
            let mut buf = [0_u8; 1024];
            while let Ok(n) = reader.read(&mut buf) {
                if n == 0 {
                    break;
                }
                output.extend_from_slice(&buf[..n]);
                let text = String::from_utf8_lossy(&output);
                if let Some(pid) = text
                    .split("bg=")
                    .nth(1)
                    .and_then(|rest| rest.split_whitespace().next())
                    .and_then(|pid| pid.parse::<i32>().ok())
                {
                    let _ = line_tx.send(pid);
                    break;
                }
            }
        });
        let background_pid = line_rx.recv_timeout(Duration::from_secs(5)).unwrap();

        owner_killer(child.as_ref()).kill().unwrap();

        let deadline = Instant::now() + Duration::from_secs(5);
        while child.try_wait().unwrap().is_none() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(20));
        }
        let shell_exited = child.try_wait().unwrap().is_some();
        if !shell_exited {
            let _ = child.kill();
        }
        assert!(shell_exited, "HUP-ignoring shell survived the owner killer");
        while process_exists(background_pid) && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(20));
        }
        assert!(
            !process_exists(background_pid),
            "background job {background_pid} survived the owner killer"
        );
    }

    #[cfg(unix)]
    fn start_test_owner(command: &str) -> (PathBuf, thread::JoinHandle<Result<()>>, Value) {
        let root = Path::new("/tmp").join(format!("mm-pty-owner-{}", &new_token()[..12]));
        let args = Args {
            root: root.clone(),
            session: "s1".to_string(),
            cwd: PathBuf::from("/tmp"),
            command: vec!["/bin/sh".to_string(), "-c".to_string(), command.to_string()],
        };
        let owner = thread::spawn(move || run_owner(args));
        let state_path = root.join("s1").join("owner.json");
        let deadline = Instant::now() + Duration::from_secs(5);
        let state = loop {
            if let Ok(data) = fs::read(&state_path)
                && let Ok(state) = serde_json::from_slice::<Value>(&data)
            {
                break state;
            }
            assert!(Instant::now() < deadline, "owner never wrote its state");
            thread::sleep(Duration::from_millis(10));
        };
        (root, owner, state)
    }

    #[cfg(unix)]
    fn attach_and_collect(state: &Value) -> (Vec<u8>, Option<i64>) {
        let addr = state["addr"]
            .as_str()
            .unwrap()
            .trim_start_matches("unix://");
        let mut stream = UnixStream::connect(addr).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(10)))
            .unwrap();
        let request = json!({
            "type": "attach",
            "token": state["token"],
            "cols": 80,
            "rows": 24,
        });
        stream.write_all(format!("{request}\n").as_bytes()).unwrap();
        let mut output = Vec::new();
        let mut exit_code = None;
        for line in BufReader::new(stream).lines() {
            let frame: Value = serde_json::from_str(&line.unwrap()).unwrap();
            match frame["type"].as_str() {
                Some("output") => {
                    output.extend(BASE64.decode(frame["output"].as_str().unwrap()).unwrap());
                }
                Some("exit") => exit_code = frame["exit_code"].as_i64(),
                _ => {}
            }
        }
        (output, exit_code)
    }

    #[cfg(unix)]
    #[test]
    fn owner_serves_late_attach_after_child_exit() {
        let (root, owner, state) = start_test_owner("echo late-output; exit 3");
        thread::sleep(Duration::from_millis(500));

        let (output, exit_code) = attach_and_collect(&state);

        assert!(String::from_utf8_lossy(&output).contains("late-output"));
        assert_eq!(exit_code, Some(3));
        owner.join().unwrap().unwrap();
        assert!(!root.join("s1").exists());
        let _ = fs::remove_dir_all(root);
    }

    #[cfg(unix)]
    #[test]
    fn owner_delivers_exit_frame_to_client_attached_at_exit() {
        let (root, owner, state) = start_test_owner("sleep 0.3; echo final-line; exit 4");

        let (output, exit_code) = attach_and_collect(&state);

        assert!(String::from_utf8_lossy(&output).contains("final-line"));
        assert_eq!(exit_code, Some(4));
        let started = Instant::now();
        owner.join().unwrap().unwrap();
        assert!(started.elapsed() < OWNER_POST_EXIT_LINGER);
        let _ = fs::remove_dir_all(root);
    }

    #[test]
    fn owner_lingers_after_exit_until_bounded_timeout() {
        let shared = Arc::new(Mutex::new(Shared {
            exited: true,
            reader_done: true,
            ..Shared::default()
        }));
        let mut completed_at = None;
        assert!(!owner_should_exit(&shared, &mut completed_at));

        completed_at = Some(Instant::now() - OWNER_POST_EXIT_LINGER);
        assert!(owner_should_exit(&shared, &mut completed_at));
    }

    #[test]
    fn stop_signals_group_until_child_is_reaped() {
        let kill_calls = Arc::new(AtomicUsize::new(0));
        let mut killer: Box<dyn ChildKiller + Send + Sync> = Box::new(RecordingKiller {
            calls: Arc::clone(&kill_calls),
        });
        let running = Arc::new(Mutex::new(Shared::default()));
        let exited_unreaped = Arc::new(Mutex::new(Shared {
            exited: true,
            reader_done: true,
            ..Shared::default()
        }));
        let reaped = Arc::new(Mutex::new(Shared {
            exited: true,
            reader_done: true,
            child_reaped: true,
            ..Shared::default()
        }));

        stop_owner(&reaped, &mut killer);
        assert_eq!(kill_calls.load(Ordering::SeqCst), 0);
        stop_owner(&running, &mut killer);
        assert_eq!(kill_calls.load(Ordering::SeqCst), 1);
        stop_owner(&exited_unreaped, &mut killer);
        assert_eq!(kill_calls.load(Ordering::SeqCst), 2);
    }

    #[cfg(unix)]
    #[test]
    fn stop_after_shell_exit_kills_surviving_background_job() {
        let (root, owner, state) =
            start_test_owner("trap '' HUP; sleep 30 & echo \"bg=$!\"; exit 0");
        let addr = state["addr"]
            .as_str()
            .unwrap()
            .trim_start_matches("unix://");
        let request = |kind: &str| {
            let mut stream = UnixStream::connect(addr).unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let request = json!({ "type": kind, "token": state["token"] });
            stream.write_all(format!("{request}\n").as_bytes()).unwrap();
            let mut line = String::new();
            BufReader::new(stream).read_line(&mut line).unwrap();
            serde_json::from_str::<Value>(&line).unwrap()
        };

        let deadline = Instant::now() + Duration::from_secs(5);
        let background_pid = loop {
            let status = request("status");
            let output = BASE64
                .decode(status["output"].as_str().unwrap_or_default())
                .unwrap();
            if let Some(pid) = String::from_utf8_lossy(&output)
                .split("bg=")
                .nth(1)
                .and_then(|rest| rest.split_whitespace().next())
                .and_then(|pid| pid.parse::<i32>().ok())
            {
                break pid;
            }
            assert!(Instant::now() < deadline, "background pid never printed");
            thread::sleep(Duration::from_millis(20));
        };
        // Let the shell exit and be reaped while the background job keeps
        // the terminal open.
        thread::sleep(Duration::from_millis(300));
        assert!(process_exists(background_pid));

        request("stop");
        owner.join().unwrap().unwrap();

        let deadline = Instant::now() + Duration::from_secs(5);
        while process_exists(background_pid) && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(20));
        }
        let survived = process_exists(background_pid);
        if survived {
            // SAFETY: plain signal to the leaked test process.
            unsafe extern "C" {
                fn kill(pid: i32, sig: i32) -> i32;
            }
            unsafe { kill(background_pid, 9) };
        }
        let _ = fs::remove_dir_all(root);
        assert!(!survived, "background job {background_pid} survived stop");
    }

    #[cfg(unix)]
    #[test]
    fn first_request_times_out_when_client_sends_nothing() {
        let (server, _client) = UnixStream::pair().unwrap();
        let (result_tx, result_rx) = mpsc::channel();
        thread::spawn(move || {
            let mut reader = BufReader::new(server);
            let _ = result_tx.send(
                read_first_request(&mut reader, Duration::from_millis(50)).map(|req| req.is_some()),
            );
        });

        let result = result_rx
            .recv_timeout(Duration::from_secs(5))
            .expect("first request read never timed out");

        assert!(result.is_err());
    }

    #[cfg(unix)]
    #[test]
    fn first_request_deadline_covers_trickled_bytes() {
        let (server, mut client) = UnixStream::pair().unwrap();
        // Each byte arrives well inside the per-read timeout, but the request
        // never completes; trickling would last about 6s.
        thread::spawn(move || {
            for _ in 0..200 {
                if client.write_all(b"{").is_err() {
                    return;
                }
                thread::sleep(Duration::from_millis(30));
            }
        });
        let (result_tx, result_rx) = mpsc::channel();
        thread::spawn(move || {
            let mut reader = BufReader::new(server);
            let _ = result_tx.send(
                read_first_request(&mut reader, Duration::from_millis(200))
                    .map(|req| req.is_some()),
            );
        });

        let result = result_rx
            .recv_timeout(Duration::from_secs(2))
            .expect("trickled first request extended the deadline");

        assert!(result.is_err());
    }

    #[test]
    fn connection_slots_cap_concurrent_connections() {
        let slots = ConnectionSlots::new(2);

        let first = slots.try_acquire().expect("first slot");
        let _second = slots.try_acquire().expect("second slot");
        assert!(slots.try_acquire().is_none());

        drop(first);
        assert!(slots.try_acquire().is_some());
    }

    #[test]
    fn subscribe_queues_replay_ahead_of_later_broadcasts() {
        let shared = Arc::new(Mutex::new(Shared {
            output: b"earlier".to_vec(),
            exit_code: -1,
            ..Shared::default()
        }));
        let (tx, rx) = new_subscriber_channel();

        let subscription = subscribe(&mut shared.lock().expect("shared poisoned"), tx);
        broadcast(&shared, b"later");

        assert!(matches!(subscription, Subscription::Live { .. }));
        assert_eq!(rx.recv().unwrap(), b"earlier");
        assert_eq!(rx.recv().unwrap(), b"later");
    }

    #[test]
    fn subscribe_after_completion_returns_replay_and_exit_code() {
        let shared = Arc::new(Mutex::new(Shared {
            output: b"done".to_vec(),
            exited: true,
            reader_done: true,
            exit_code: 9,
            ..Shared::default()
        }));
        let (tx, _rx) = new_subscriber_channel();

        let subscription = subscribe(&mut shared.lock().expect("shared poisoned"), tx);

        match subscription {
            Subscription::Complete { replay, exit_code } => {
                assert_eq!(replay, b"done");
                assert_eq!(exit_code, 9);
            }
            Subscription::Live { .. } => panic!("completed owner registered a live subscriber"),
        }
        assert!(
            shared
                .lock()
                .expect("shared poisoned")
                .subscribers
                .is_empty()
        );
    }

    #[test]
    fn exit_code_after_output_close_waits_for_child_exit() {
        let shared = Arc::new(Mutex::new(Shared {
            exit_code: -1,
            ..Shared::default()
        }));
        let waiter_shared = Arc::clone(&shared);
        let waiter = thread::spawn(move || exit_code_after_output_close(&waiter_shared));

        thread::sleep(Duration::from_millis(50));
        mark_child_exited(&shared, 7);

        assert_eq!(waiter.join().unwrap(), 7);
    }

    #[test]
    fn exit_code_after_output_close_gives_up_after_grace_period() {
        let shared = Arc::new(Mutex::new(Shared {
            exit_code: -1,
            ..Shared::default()
        }));

        let started = Instant::now();
        let code = exit_code_after_output_close(&shared);

        assert_eq!(code, -1);
        assert!(started.elapsed() >= OWNER_EXIT_CODE_GRACE_PERIOD);
    }

    #[test]
    fn broadcast_uses_bounded_subscriber_channels() {
        let (tx, rx) = new_subscriber_channel();
        let shared = Arc::new(Mutex::new(Shared {
            subscribers: vec![Subscriber { id: 1, tx }],
            next_subscriber_id: 2,
            exit_code: -1,
            ..Shared::default()
        }));

        broadcast(&shared, b"chunk");

        assert_eq!(rx.recv().unwrap(), b"chunk");
    }

    #[test]
    fn broadcast_removes_only_full_subscriber_channels() {
        let (full_tx, _full_rx) = new_subscriber_channel();
        for _ in 0..SUBSCRIBER_CHANNEL_CAPACITY {
            full_tx.try_send(vec![b'x']).unwrap();
        }
        let (active_tx, active_rx) = new_subscriber_channel();
        let shared = Arc::new(Mutex::new(Shared {
            subscribers: vec![
                Subscriber { id: 1, tx: full_tx },
                Subscriber {
                    id: 2,
                    tx: active_tx,
                },
            ],
            next_subscriber_id: 3,
            exit_code: -1,
            ..Shared::default()
        }));

        broadcast(&shared, b"chunk");

        assert_eq!(active_rx.recv().unwrap(), b"chunk");
        let remaining_ids: Vec<u64> = shared
            .lock()
            .expect("shared poisoned")
            .subscribers
            .iter()
            .map(|subscriber| subscriber.id)
            .collect();
        assert_eq!(remaining_ids, vec![2]);
    }

    #[test]
    fn terminal_title_parser_updates_from_osc_sequences() {
        let mut state = TitleParserState::default();

        assert_eq!(
            update_terminal_title(&mut state, b"before\x1b]0;busy title\x07after"),
            Some("busy title".to_string())
        );
    }

    #[test]
    fn terminal_title_parser_handles_split_st_sequences() {
        let mut state = TitleParserState::default();

        assert_eq!(update_terminal_title(&mut state, b"\x1b]2;split"), None);
        assert_eq!(
            update_terminal_title(&mut state, b" title\x1b\\tail"),
            Some("split title".to_string())
        );
    }

    #[test]
    fn terminal_title_parser_handles_split_escape_prefix() {
        let mut state = TitleParserState::default();

        assert_eq!(update_terminal_title(&mut state, b"\x1b"), None);
        assert_eq!(
            update_terminal_title(&mut state, b"]0;edge title\x07"),
            Some("edge title".to_string())
        );
    }
}
