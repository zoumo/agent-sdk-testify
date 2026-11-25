use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{ExitStatus, Stdio};
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    ClientInfo, InitializeCapabilities, InitializeParams, JSONRPCErrorError, JSONRPCRequest,
    RequestId, ServerNotification, TurnStartResponse, TurnStatus,
};
use codex_app_server_remote_client::{
    AppServerEvent, RemoteAppServerClient, RemoteAppServerRequestHandle, TypedRequestError,
    decode_typed_response,
};
use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::{Value, json};
use tokio::process::{Child, Command};
use tokio::time::timeout;

mod history;
mod interactions;
mod sandbox_permissions;

const CONTINUATION_CASE: &str = "SDK-CASE-001";
const REVISION: &str = include_str!("../upstream-revision");

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    case: String,
    materials: PathBuf,
    runtime: PathBuf,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Runtime {
    schema_version: u32,
    root: PathBuf,
    workspace: PathBuf,
    home: PathBuf,
    config: PathBuf,
    raw: PathBuf,
    tmp: PathBuf,
    output: PathBuf,
    seed_root: Option<PathBuf>,
    recorded_workspace: Option<PathBuf>,
    fixture_dir: PathBuf,
    replay: bool,
    peer: Peer,
    agent_results: BTreeMap<String, PathBuf>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Peer {
    command: PathBuf,
    #[serde(default)]
    args: Vec<String>,
    #[serde(default)]
    env: BTreeMap<String, String>,
    probe: Option<Box<Peer>>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Plan {
    case_id: String,
    story_id: String,
    provider: String,
    driver_profile: String,
    project: PathBuf,
    model: String,
    initial_model: Option<String>,
    prompts: Vec<String>,
    cli_version: Option<String>,
    #[serde(default)]
    scenario: Value,
    #[serde(flatten)]
    history: history::Projection,
}

#[derive(Deserialize, PartialEq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct PeerNotice {
    schema_version: u32,
    fixture_id: String,
    material_id: String,
    execution_id: String,
    connection_id: String,
    command: String,
    status: String,
}

impl Runtime {
    fn validate(&self) -> Result<()> {
        ensure!(
            self.schema_version == 1,
            "unsupported official driver runtime schema"
        );
        for (name, path) in [
            ("root", &self.root),
            ("workspace", &self.workspace),
            ("home", &self.home),
            ("config", &self.config),
            ("raw", &self.raw),
            ("tmp", &self.tmp),
            ("output", &self.output),
            ("fixtureDir", &self.fixture_dir),
            ("main peer command", &self.peer.command),
        ] {
            require_absolute(name, path)?;
        }
        for (name, path) in [
            ("seedRoot", &self.seed_root),
            ("recordedWorkspace", &self.recorded_workspace),
        ] {
            if let Some(path) = path {
                require_absolute(name, path)?;
            }
        }
        if let Some(peer) = &self.peer.probe {
            ensure!(peer.probe.is_none(), "nested probe launcher");
            require_absolute("probe peer command", &peer.command)?;
        }
        for path in self.agent_results.values() {
            require_absolute("agentResult", path)?;
        }
        Ok(())
    }

    fn validate_connections(&self, probe: bool) -> Result<()> {
        ensure!(
            self.peer.probe.is_some() == probe
                && self.agent_results.contains_key("main")
                && self.agent_results.contains_key("probe") == probe
                && self.agent_results.len() == if probe { 2 } else { 1 },
            "runtime connections differ from case"
        );
        Ok(())
    }

    fn spawn_peer(&self, id: &str) -> Result<Child> {
        let peer = match id {
            "main" => Some(&self.peer),
            "probe" => self.peer.probe.as_deref(),
            _ => None,
        }
        .context("missing Testify peer launcher")?;
        Command::new(&peer.command)
            .args(&peer.args)
            .current_dir(&self.workspace)
            .envs(&peer.env)
            .env("HOME", &self.home)
            .env("CODEX_HOME", &self.config)
            .env("TMPDIR", &self.tmp)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .kill_on_drop(true)
            .spawn()
            .with_context(|| format!("start Testify {id} peer"))
    }

    fn connection(&self, id: &str) -> Result<PeerNotice> {
        let path = self.agent_results.get(id).context("missing agentResult")?;
        let data = fs::read_to_string(path).context("read Testify peer notifications")?;
        let mut notices = data.lines();
        let mut started: PeerNotice = serde_json::from_str(
            notices
                .next()
                .context("missing running peer notification")?,
        )?;
        let finished: PeerNotice =
            serde_json::from_str(notices.next().context("missing final peer notification")?)?;
        ensure!(
            data.ends_with('\n')
                && notices.next().is_none()
                && started.schema_version == 3
                && started.connection_id == id
                && started.command == if self.replay { "mock" } else { "record" }
                && started.status == "running"
                && matches!(finished.status.as_str(), "passed" | "failed")
                && !started.fixture_id.is_empty()
                && safe_id(&started.material_id)
                && safe_id(&started.execution_id),
            "incomplete Testify peer notifications"
        );
        started.status.clone_from(&finished.status);
        ensure!(
            started == finished,
            "Testify peer notification identity changed"
        );
        Ok(finished)
    }
}

fn safe_id(id: &str) -> bool {
    !id.is_empty()
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

fn require_absolute(name: &str, path: &Path) -> Result<()> {
    ensure!(path.is_absolute(), "{name} must be an absolute path");
    Ok(())
}

fn read_json<T: DeserializeOwned>(path: &Path) -> Result<T> {
    serde_json::from_slice(&fs::read(path).with_context(|| format!("read {}", path.display()))?)
        .with_context(|| format!("invalid JSON in {}", path.display()))
}

fn write_json(path: &Path, value: &Value) -> Result<()> {
    let temp = path.with_extension("json.tmp");
    fs::write(&temp, serde_json::to_vec_pretty(value)?)?;
    fs::rename(temp, path)?;
    Ok(())
}

fn record_receipt(report: &mut Value, receipt: Value) {
    report["receipts"]
        .as_array_mut()
        .expect("initialized receipts")
        .push(receipt);
}

async fn request<T: DeserializeOwned>(
    client: &RemoteAppServerClient,
    id: i64,
    method: &str,
    params: Value,
) -> std::result::Result<T, TypedRequestError> {
    // Use the upstream raw request API for the case's native parameters.
    let response = client
        .request_handle()
        .request_json_rpc(JSONRPCRequest {
            id: RequestId::Integer(id),
            method: method.to_owned(),
            params: Some(params),
            trace: None,
        })
        .await;
    decode_typed_response(method, response)
}

async fn open_thread(
    client: &RemoteAppServerClient,
    plan: &Plan,
    workspace: &Path,
) -> Result<(String, Value)> {
    let sandbox = if matches!(
        plan.driver_profile.as_str(),
        "approval" | "deferred-approval" | "sandbox-permissions"
    ) {
        "read-only"
    } else {
        "workspace-write"
    };
    let mut params = json!({
        "cwd": workspace,
        "sandbox": sandbox,
        "approvalPolicy": "on-request",
        "ephemeral": plan.driver_profile != "deferred-approval",
        "baseInstructions": "Complete the supplied synthetic task in the working directory. Be concise.",
        "developerInstructions": "Complete the supplied task in the synthetic project.",
    });
    if !plan.model.is_empty() {
        params["model"] = json!(plan.initial_model.as_deref().unwrap_or(&plan.model));
    }
    interactions::configure_thread_start(plan, &mut params)?;
    let thread: Value = request(client, 1, "thread/start", params).await?;
    let thread_id = thread["thread"]["id"]
        .as_str()
        .filter(|id| !id.is_empty())
        .context("missing opened thread ID")?
        .to_owned();
    Ok((thread_id, thread))
}

async fn start_turn(
    client: &RemoteAppServerClient,
    id: i64,
    thread_id: &str,
    prompt: &str,
) -> Result<TurnStartResponse> {
    request(
        client,
        id,
        "turn/start",
        json!({"threadId": thread_id, "input": [{"type": "text", "text": prompt}]}),
    )
    .await
    .map_err(Into::into)
}

async fn exercise_continuation(
    client: &mut RemoteAppServerClient,
    plan: &Plan,
    thread_id: &str,
    report: &mut Value,
) -> Result<()> {
    let receipts = report["receipts"]
        .as_array_mut()
        .expect("initialized receipts");
    let mut last_turn = String::new();
    for (index, prompt) in plan.prompts.iter().enumerate() {
        let accepted = start_turn(client, index as i64 + 2, thread_id, prompt).await?;
        let turn_id = &accepted.turn.id;
        ensure!(
            !turn_id.is_empty() && turn_id != &last_turn,
            "missing or repeated turn ID"
        );
        receipts.push(json!({"input-submitted": accepted}));
        loop {
            match client.next_event().await {
                Some(AppServerEvent::ServerNotification(event)) => {
                    if let ServerNotification::TurnCompleted(completed) = *event {
                        ensure!(
                            completed.thread_id == thread_id
                                && completed.turn.id == *turn_id
                                && completed.turn.status == TurnStatus::Completed
                                && completed.turn.error.is_none(),
                            "unexpected or unsuccessful turn completion"
                        );
                        receipts.push(json!({"turn/completed": completed}));
                        break;
                    }
                }
                Some(AppServerEvent::ServerRequest(request)) => {
                    client
                        .reject_server_request(
                            request.id().clone(),
                            JSONRPCErrorError {
                                code: -32601,
                                message: "Unexpected server request in SDK-CASE-001".into(),
                                data: None,
                            },
                        )
                        .await?;
                    bail!("unexpected server request in SDK-CASE-001");
                }
                Some(AppServerEvent::Disconnected { message }) => bail!("{message}"),
                Some(_) => bail!("unexpected app-server event"),
                None => bail!("app-server closed before turn completion"),
            }
        }
        last_turn.clone_from(turn_id);
    }
    report["inputs"] = json!(2);
    report["results"] = json!(2);
    Ok(())
}

async fn reap(child: &mut Child) -> Result<ExitStatus> {
    match timeout(Duration::from_secs(5), child.wait()).await {
        Ok(status) => Ok(status.context("wait for Testify peer")?),
        Err(_) => {
            child
                .kill()
                .await
                .context("kill and reap timed-out Testify peer")?;
            bail!("Testify peer did not exit after stdin closed; killed and reaped");
        }
    }
}

async fn exercise(
    client: &mut RemoteAppServerClient,
    plan: &Plan,
    workspace: &Path,
    execution_workspace: &Path,
    report: &mut Value,
) -> Result<()> {
    let (thread_id, thread) = open_thread(client, plan, workspace).await?;
    record_receipt(report, json!({"thread/start": thread}));
    if plan.case_id == sandbox_permissions::CASE_ID {
        sandbox_permissions::exercise(client, plan, workspace, &thread_id, report).await
    } else if plan.case_id == CONTINUATION_CASE {
        exercise_continuation(client, plan, &thread_id, report).await
    } else {
        interactions::exercise(
            client,
            plan,
            workspace,
            execution_workspace,
            &thread_id,
            report,
        )
        .await
    }
}

async fn run_connection<T>(
    runtime: &Runtime,
    id: &str,
    report: &mut Value,
    exercise: impl AsyncFnOnce(&mut RemoteAppServerClient, &mut Value) -> Result<T>,
) -> Result<(T, PeerNotice, RemoteAppServerRequestHandle)> {
    let mut child = runtime.spawn_peer(id)?;
    let pid = child.id();
    let stdin = child.stdin.take().expect("piped stdin");
    let stdout = child.stdout.take().expect("piped stdout");
    let result = match RemoteAppServerClient::connect_stdio(
        stdin,
        stdout,
        InitializeParams {
            client_info: ClientInfo {
                name: "agent-sdk-testify-codex".into(),
                title: Some("Testify case driver".into()),
                version: env!("CARGO_PKG_VERSION").into(),
            },
            capabilities: Some(InitializeCapabilities {
                experimental_api: true,
                ..Default::default()
            }),
        },
        16,
    )
    .await
    {
        Ok(mut client) => {
            let handle = client.request_handle();
            let result = exercise(&mut client, report).await;
            let shutdown = client.shutdown().await.context("close SDK connection");
            report["sdkConnectionsClosed"][id] = json!(shutdown.is_ok());
            if id == "main" {
                report["sdkClosed"] = json!(shutdown.is_ok());
            }
            result.and_then(|value| shutdown.map(|()| (value, handle)))
        }
        Err(error) => Err(error).context("initialize SDK connection"),
    };
    let status = reap(&mut child).await;
    let process = json!({
        "pid": pid,
        "reaped": child.id().is_none(),
        "exitCode": status.as_ref().ok().and_then(ExitStatus::code),
        "success": status.as_ref().is_ok_and(ExitStatus::success),
    });
    report["peerProcesses"][id] = process.clone();
    if id == "main" {
        report["peerProcess"] = process;
    }
    let connection = runtime.connection(id);
    if let Ok(connection) = &connection {
        report["connections"][id] = json!(connection);
        if id == "main" {
            report["connection"] = json!(connection);
        }
    }
    let (value, handle) = result?;
    let status = status?;
    ensure!(status.success(), "Testify {id} peer exited with {status}");
    let connection = connection.with_context(|| format!("read Testify {id} connection"))?;
    ensure!(
        connection.status == "passed",
        "Testify {id} connection failed"
    );
    Ok((value, connection, handle))
}

async fn run(runtime: &Runtime, plan: &Plan, report: &mut Value) -> Result<()> {
    let workspace = runtime
        .recorded_workspace
        .as_deref()
        .unwrap_or(&plan.project);
    require_absolute("protocol workspace", workspace)?;
    if history::case(&plan.case_id).is_some() {
        return history::run(runtime, plan, workspace, report).await;
    }
    runtime.validate_connections(false)?;
    let ((), _, handle) = run_connection(runtime, "main", report, async |client, report| {
        exercise(client, plan, workspace, &runtime.workspace, report).await
    })
    .await?;
    if plan.case_id == "SDK-CASE-031" {
        let rejected = handle
            .request_json_rpc(JSONRPCRequest {
                id: RequestId::Integer(100),
                method: "turn/start".into(),
                params: Some(
                    json!({"threadId": report["receipts"][0]["thread/start"]["thread"]["id"],
                    "input": [{"type": "text", "text": "Reply Ready."}]}),
                ),
                trace: None,
            })
            .await;
        ensure!(
            handle.is_closed() && rejected.is_err(),
            "closed SDK accepted subsequent input"
        );
        report["sessionClose"] = json!({
            "interactionClosed": true, "processClosed": true, "postCloseRejected": true
        });
        record_receipt(
            report,
            json!({"post-close-submit": {"rejected": true,
            "reason": rejected.unwrap_err().to_string()}}),
        );
    }
    Ok(())
}

#[tokio::main(flavor = "current_thread")]
async fn main() -> Result<()> {
    let args: Vec<_> = std::env::args_os().skip(1).collect();
    ensure!(
        args.len() == 2 && args[0] == "--request",
        "usage: agent-sdk-testify-codex --request FILE"
    );
    let request: Request = read_json(Path::new(&args[1]))?;
    require_absolute("runtime", &request.runtime)?;
    require_absolute("materials", &request.materials)?;
    let runtime: Runtime = read_json(&request.runtime)?;
    runtime.validate()?;

    let spec = match request.case.as_str() {
        CONTINUATION_CASE => Some(("SDK-US-001", "continuation", 2)),
        sandbox_permissions::CASE_ID => Some(("SDK-US-003", "sandbox-permissions", 3)),
        id => interactions::case_spec(id).or_else(|| history::case(id)),
    };
    let Some((story_id, driver_profile, prompt_count)) = spec else {
        return write_json(
            &runtime.output.join("result.json"),
            &json!({"status": "unsupported", "message": "case is outside the Rust candidate scope"}),
        );
    };
    let sdk = json!({
        "package": "codex-app-server-remote-client",
        "version": "0.0.0",
        "revision": REVISION.trim(),
    });
    let mut report = json!({
        "provider": "codex", "storyId": story_id, "caseId": request.case, "officialSDK": sdk,
        "receipts": [], "inputs": 0, "results": 0,
    });
    let result = async {
        let plan: Plan = read_json(&request.materials.join("case.json"))?;
        ensure!(
            plan.case_id == request.case
                && plan.story_id == story_id
                && plan.provider == "codex"
                && plan.driver_profile == driver_profile
                && (plan.case_id != CONTINUATION_CASE || !plan.model.is_empty())
                && plan.prompts.len() == prompt_count
                && plan.prompts.iter().all(|p| !p.trim().is_empty()),
            "invalid Codex {} case projection",
            request.case
        );
        if plan.case_id == sandbox_permissions::CASE_ID {
            sandbox_permissions::validate(&plan)?;
        }
        report["cliVersion"] = json!(plan.cli_version);
        run(&runtime, &plan, &mut report).await
    }
    .await;
    let (status, capture, message) = match result {
        Ok(()) => ("passed", "complete", String::new()),
        Err(error) => ("failed", "failed", format!("{error:#}")),
    };
    report["status"] = json!(status);
    report["branchStatus"] = json!(status);
    report["captureStatus"] = json!(capture);
    report["gaps"] = if message.is_empty() {
        json!([])
    } else {
        json!([message])
    };
    let report_file = if runtime.replay {
        "replay-report.json"
    } else {
        "report.json"
    };
    write_json(&runtime.output.join(report_file), &report)?;
    write_json(
        &runtime.output.join("result.json"),
        &json!({"status": status, "message": message, "sdk": sdk}),
    )
}
