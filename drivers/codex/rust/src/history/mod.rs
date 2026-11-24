use std::collections::BTreeSet;
use std::path::Path;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    AskForApproval, ClientRequest, JSONRPCErrorError, RequestId, SandboxMode, ServerRequest,
    SortDirection, Thread, ThreadListCwdFilter, ThreadListParams, ThreadListResponse,
    ThreadReadParams, ThreadReadResponse, ThreadResumeParams, ThreadResumeResponse,
    ThreadSourceKind, ThreadStartParams, ThreadStartResponse, ThreadTurnsListParams,
    ThreadUnsubscribeParams, ThreadUnsubscribeResponse, ThreadUnsubscribeStatus, Turn,
    TurnItemsView, TurnStartParams, TurnStartResponse, TurnStatus, UserInput,
};
use codex_app_server_remote_client::{AppServerEvent, RemoteAppServerClient, TypedRequestError};
use serde::Deserialize;
use serde_json::{Value, json};

use crate::{PeerNotice, Plan, Runtime, record_receipt, run_connection};

mod children;
mod seeds;

#[derive(Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub(super) struct Projection {
    history_seed: Option<Value>,
    existing_children_setup: Option<seeds::Setup>,
    #[serde(default)]
    history_bindings: Vec<seeds::Binding>,
}

pub(super) fn case(id: &str) -> Option<(&'static str, &'static str, usize)> {
    Some(match id {
        "SDK-CASE-016" => ("SDK-US-005", "children", 1),
        "SDK-CASE-017" | "SDK-CASE-018" => ("SDK-US-005", "topology", 1),
        "SDK-CASE-019" => ("SDK-US-005", "background-children", 1),
        "SDK-CASE-020" => ("SDK-US-005", "history", 1),
        "SDK-CASE-029" => ("SDK-US-006", "history", 1),
        "SDK-CASE-032" => ("SDK-US-007", "remote-close", 1),
        "SDK-CASE-033" | "SDK-CASE-034" => ("SDK-US-008", "history", 1),
        "SDK-CASE-035" => ("SDK-US-008", "existing-children", 0),
        "SDK-CASE-036" => ("SDK-US-008", "history", 2),
        _ => return None,
    })
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Parameters {
    topology: Option<String>,
    child_tools: Option<String>,
    child_detail_policy: Option<String>,
    child_configuration: Option<String>,
    history_target: Option<String>,
    setup_prompt: Option<String>,
    #[serde(default)]
    self_seed: bool,
}

impl Parameters {
    fn from_plan(plan: &Plan) -> Result<Self> {
        let parameters: Self = serde_json::from_value(plan.scenario["parameters"].clone())
            .context("invalid history case parameters")?;
        let valid = match plan.case_id.as_str() {
            "SDK-CASE-016" | "SDK-CASE-019" => {
                parameters.child_tools.as_deref() == Some("inherited-dynamic-v2")
            }
            "SDK-CASE-017" => parameters.topology.as_deref() == Some("nested"),
            "SDK-CASE-018" => parameters.topology.as_deref() == Some("parallel"),
            "SDK-CASE-020" => {
                parameters.child_detail_policy.as_deref() == Some("suppress")
                    && parameters.history_target.as_deref() == Some("child-messages")
            }
            "SDK-CASE-029" => {
                parameters.child_configuration.as_deref() == Some("model")
                    && parameters.history_target.as_deref() == Some("child-messages")
            }
            "SDK-CASE-033" => parameters.history_target.as_deref() == Some("messages"),
            "SDK-CASE-034" => parameters.history_target.as_deref() == Some("child-messages"),
            "SDK-CASE-035" if parameters.self_seed => {
                parameters
                    .setup_prompt
                    .as_ref()
                    .is_some_and(|prompt| !prompt.trim().is_empty())
                    && plan.history.history_seed.is_none()
            }
            "SDK-CASE-035" => false,
            "SDK-CASE-036" => parameters.history_target.as_deref() == Some("pagination"),
            "SDK-CASE-032" => true,
            _ => false,
        };
        ensure!(
            valid && !plan.model.is_empty(),
            "invalid history case parameters"
        );
        Ok(parameters)
    }
}

pub(super) async fn run(
    runtime: &Runtime,
    plan: &Plan,
    workspace: &Path,
    report: &mut Value,
) -> Result<()> {
    let parameters = Parameters::from_plan(plan)?;
    let remote = plan.case_id == "SDK-CASE-032";
    runtime.validate_connections(remote || parameters.self_seed)?;
    if parameters.self_seed {
        return seeds::self_seeded(runtime, plan, workspace, &parameters, report).await;
    }
    ensure!(
        plan.case_id != "SDK-CASE-019" || runtime.replay,
        "019 live child-gate fixture is unavailable in the Rust driver"
    );
    let ((root, submitted, saved_child), main, closed) =
        run_connection(runtime, "main", report, async |client, report| {
            let mut query = Query::new(client, report);
            let tool = parameters.child_tools.is_some();
            let root = query
                .start(
                    plan,
                    workspace,
                    if tool {
                        Start::FixtureRead
                    } else if remote {
                        Start::Saved
                    } else {
                        Start::Standard
                    },
                )
                .await?;
            let mut children = children::Observed::default();
            let mut submitted = Vec::new();
            for prompt in &plan.prompts {
                let turn = query.submit(&root.id, prompt).await?;
                ensure!(!submitted.contains(&turn), "repeated native turn identity");
                submitted.push(turn.clone());
                query
                    .until_completion(
                        &root.id,
                        &turn,
                        &mut children,
                        tool.then_some((runtime, root.id.as_str())),
                        false,
                    )
                    .await?;
            }
            let saved_child = match plan.driver_profile.as_str() {
                "children" | "background-children" | "topology" => {
                    children::check_live(&mut query, plan, &root.id, &mut children, runtime)
                        .await?;
                    None
                }
                "history" => {
                    query
                        .check_history(workspace, &root, &submitted, &children, &parameters)
                        .await?
                }
                "remote-close" => None,
                _ => bail!("unsupported history driver profile"),
            };
            Ok((root.id, submitted, saved_child))
        })
        .await?;

    if plan.case_id == "SDK-CASE-034" {
        let child = saved_child
            .as_deref()
            .context("missing saved child identity")?;
        seeds::check_child_history(runtime, plan, &main, &root, child, workspace)?;
        record_receipt(
            report,
            json!({"history-bindings": {
                "parentThreadId": root, "childThreadId": child,
            }}),
        );
    }
    if parameters.child_configuration.is_some() {
        report["childModelRequested"] = json!(plan.model);
    }
    if remote {
        let closed_query = ClientRequest::ThreadRead {
            request_id: RequestId::Integer(100),
            params: ThreadReadParams {
                thread_id: root.clone(),
                include_turns: false,
            },
        };
        ensure!(
            closed.is_closed()
                && matches!(
                    closed
                        .request_typed::<ThreadReadResponse>(closed_query)
                        .await,
                    Err(TypedRequestError::Transport { .. })
                ),
            "closed SDK connection accepted another request"
        );
        record_receipt(
            report,
            json!({"remote-close": {"firstClientRejected": true}}),
        );
        let ((), probe, _) = run_connection(runtime, "probe", report, async |client, report| {
            let thread = Query::new(client, report).read(&root, true).await?;
            check_saved_turns(&thread.turns, &submitted)?;
            Ok(())
        })
        .await?;
        same_execution(&main, &probe)?;
        record_receipt(
            report,
            json!({"remote-probe": {
                "threadId": root, "completedTurnIds": submitted,
                "sameExecution": true, "liveServiceObserved": !runtime.replay,
            }}),
        );
    }
    Ok(())
}

fn same_execution(main: &PeerNotice, probe: &PeerNotice) -> Result<()> {
    ensure!(
        main.fixture_id == probe.fixture_id
            && main.material_id == probe.material_id
            && main.execution_id == probe.execution_id
            && main.connection_id == "main"
            && probe.connection_id == "probe",
        "Testify connections disagree on their execution or material"
    );
    Ok(())
}

#[derive(Clone, Copy)]
enum Start {
    Standard,
    Saved,
    FixtureRead,
}

struct Query<'a> {
    client: &'a mut RemoteAppServerClient,
    report: &'a mut Value,
    next_id: i64,
}

impl<'a> Query<'a> {
    fn new(client: &'a mut RemoteAppServerClient, report: &'a mut Value) -> Self {
        Self {
            client,
            report,
            next_id: 1,
        }
    }

    fn id(&mut self) -> RequestId {
        let id = RequestId::Integer(self.next_id);
        self.next_id += 1;
        id
    }

    async fn start(&mut self, plan: &Plan, workspace: &Path, style: Start) -> Result<Thread> {
        let (base, developer) = match style {
            Start::Standard => (
                "Complete the supplied synthetic task in the working directory. Be concise.",
                Some("Complete the supplied task in the synthetic project."),
            ),
            Start::Saved => ("Complete the supplied synthetic task. Be concise.", None),
            Start::FixtureRead => (
                "Complete the supplied synthetic child task.",
                Some("Delegate the fixture read to fixture_reader."),
            ),
        };
        let dynamic_tools = matches!(style, Start::FixtureRead)
            .then(children::fixture_tool)
            .transpose()?;
        let request_id = self.id();
        let response: ThreadStartResponse = self
            .client
            .request_typed(ClientRequest::ThreadStart {
                request_id,
                params: ThreadStartParams {
                    cwd: Some(
                        workspace
                            .to_str()
                            .context("protocol workspace must be UTF-8")?
                            .to_owned(),
                    ),
                    model: Some(plan.model.clone()),
                    ephemeral: Some(false),
                    sandbox: Some(SandboxMode::WorkspaceWrite),
                    approval_policy: Some(AskForApproval::OnRequest),
                    base_instructions: Some(base.to_owned()),
                    developer_instructions: developer.map(str::to_owned),
                    dynamic_tools,
                    ..Default::default()
                },
            })
            .await?;
        ensure!(
            native_id(&response.thread.id)
                && response.model == plan.model
                && !response.thread.ephemeral
                && response.thread.parent_thread_id.is_none(),
            "native root identity or model differs from request"
        );
        record_receipt(self.report, json!({"thread/start": response}));
        Ok(response.thread)
    }

    async fn submit(&mut self, thread: &str, prompt: &str) -> Result<String> {
        let request_id = self.id();
        let response: TurnStartResponse = self
            .client
            .request_typed(ClientRequest::TurnStart {
                request_id,
                params: TurnStartParams {
                    thread_id: thread.to_owned(),
                    input: vec![UserInput::Text {
                        text: prompt.to_owned(),
                        text_elements: vec![],
                    }],
                    ..Default::default()
                },
            })
            .await?;
        ensure!(
            native_id(&response.turn.id) && response.turn.status == TurnStatus::InProgress,
            "native turn did not start"
        );
        self.increment("inputs");
        record_receipt(self.report, json!({"input-submitted": response}));
        Ok(response.turn.id)
    }

    fn increment(&mut self, key: &str) {
        self.report[key] = json!(self.report[key].as_u64().unwrap_or_default() + 1);
    }

    async fn reject(&self, request: &ServerRequest) -> Result<()> {
        self.client
            .reject_server_request(
                request.id().clone(),
                JSONRPCErrorError {
                    code: -32601,
                    message: "Unexpected server request in Codex history case".into(),
                    data: None,
                },
            )
            .await?;
        bail!("unexpected server request in Codex history case")
    }

    async fn until_completion(
        &mut self,
        thread: &str,
        turn: &str,
        children: &mut children::Observed,
        tool: Option<(&Runtime, &str)>,
        post_root: bool,
    ) -> Result<()> {
        loop {
            match self.client.next_event().await {
                Some(AppServerEvent::ServerNotification(event)) => {
                    self.increment("events");
                    let completed = children.accept(&event)?;
                    if let Some(completed) = completed.filter(|event| event.thread_id == thread) {
                        ensure!(completed.turn.id == turn, "unexpected native terminal turn");
                        if !post_root {
                            self.increment("results");
                        }
                        record_receipt(self.report, json!({"turn/completed": completed}));
                        return Ok(());
                    }
                }
                Some(AppServerEvent::ServerRequest(request)) => {
                    if let (
                        Some((runtime, root)),
                        ServerRequest::DynamicToolCall { request_id, params },
                    ) = (tool, request.as_ref())
                    {
                        children
                            .fixture_read(self, runtime, root, request_id, params, post_root)
                            .await?;
                    } else {
                        self.reject(&request).await?;
                    }
                }
                Some(AppServerEvent::Disconnected { message }) => bail!("{message}"),
                Some(_) => bail!("unexpected app-server event"),
                None => bail!("app-server closed before native turn completion"),
            }
        }
    }

    async fn detach(&mut self, thread: &str) -> Result<()> {
        let request_id = self.id();
        let response: ThreadUnsubscribeResponse = self
            .client
            .request_typed(ClientRequest::ThreadUnsubscribe {
                request_id,
                params: ThreadUnsubscribeParams {
                    thread_id: thread.to_owned(),
                },
            })
            .await?;
        ensure!(
            response.status == ThreadUnsubscribeStatus::Unsubscribed,
            "native thread did not detach"
        );
        record_receipt(
            self.report,
            json!({"thread/unsubscribe": {"threadId": thread, "response": response}}),
        );
        Ok(())
    }

    async fn read(&mut self, thread: &str, full: bool) -> Result<Thread> {
        let params = ThreadReadParams {
            thread_id: thread.to_owned(),
            include_turns: full,
        };
        let request_id = self.id();
        let native: Value = self
            .client
            .request_typed(ClientRequest::ThreadRead {
                request_id,
                params: params.clone(),
            })
            .await?;
        let response: ThreadReadResponse = serde_json::from_value(native.clone())?;
        ensure!(
            response.thread.id == thread && (full || response.thread.turns.is_empty()),
            "native thread read returned a different identity or scope"
        );
        record_receipt(
            self.report,
            json!({"history-query": {
                "method": "thread/read", "params": params, "response": native,
            }}),
        );
        Ok(response.thread)
    }

    async fn list(&mut self, mut params: ThreadListParams) -> Result<Vec<Thread>> {
        let mut rows = Vec::new();
        let mut cursors = BTreeSet::new();
        let mut identities = BTreeSet::new();
        loop {
            let request_id = self.id();
            let native: Value = self
                .client
                .request_typed(ClientRequest::ThreadList {
                    request_id,
                    params: params.clone(),
                })
                .await?;
            ensure!(
                native.get("nextCursor").is_some(),
                "native list omitted its cursor boundary"
            );
            let response: ThreadListResponse = serde_json::from_value(native.clone())?;
            record_receipt(
                self.report,
                json!({"history-query": {
                    "method": "thread/list", "params": params, "response": native,
                }}),
            );
            for row in &response.data {
                ensure!(
                    native_id(&row.id) && identities.insert(row.id.clone()),
                    "native discovery returned an invalid or repeated thread identity"
                );
            }
            let next = next_cursor(response.next_cursor, response.data.len(), &mut cursors)?;
            rows.extend(response.data);
            if next.is_none() {
                return Ok(rows);
            }
            params.cursor = next;
        }
    }

    async fn pages(&mut self, thread: &str, limit: u32) -> Result<Vec<Value>> {
        let mut cursors = BTreeSet::new();
        let mut turns = Vec::new();
        let mut cursor = None;
        loop {
            let params = ThreadTurnsListParams {
                thread_id: thread.to_owned(),
                cursor,
                limit: Some(limit),
                sort_direction: Some(SortDirection::Asc),
                items_view: Some(TurnItemsView::Full),
            };
            let request_id = self.id();
            let native: Value = self
                .client
                .request_typed(ClientRequest::ThreadTurnsList {
                    request_id,
                    params: params.clone(),
                })
                .await?;
            record_receipt(
                self.report,
                json!({"history-query": {
                    "method": "thread/turns/list", "params": params, "response": native,
                }}),
            );
            let data = native["data"]
                .as_array()
                .context("native turn page omitted data")?;
            let size = data.len();
            ensure!(
                size <= limit as usize && (limit != 1 || size == 1),
                "native turn page violates its window"
            );
            let boundary = native
                .get("nextCursor")
                .context("native turn page omitted its cursor boundary")?;
            cursor = next_cursor(
                serde_json::from_value(boundary.clone())?,
                size,
                &mut cursors,
            )?;
            turns.extend_from_slice(data);
            if cursor.is_none() {
                return Ok(turns);
            }
        }
    }

    async fn check_history(
        &mut self,
        workspace: &Path,
        root: &Thread,
        submitted: &[String],
        children: &children::Observed,
        parameters: &Parameters,
    ) -> Result<Option<String>> {
        self.detach(&root.id).await?;
        let mut scope = list_scope();
        scope.cwd = Some(ThreadListCwdFilter::One(
            workspace.to_string_lossy().into_owned(),
        ));
        scope.source_kinds = Some(vec![
            ThreadSourceKind::Cli,
            ThreadSourceKind::VsCode,
            ThreadSourceKind::Exec,
            ThreadSourceKind::AppServer,
        ]);
        let roots = self.list(scope).await?;
        ensure!(
            roots
                .iter()
                .any(|thread| thread.id == root.id && thread.parent_thread_id.is_none()),
            "seed root was not independently discovered in the requested workspace"
        );

        let child_history = parameters.history_target.as_deref() == Some("child-messages");
        let mut target = root.id.clone();
        let mut expected = submitted.to_vec();
        if child_history {
            let mut scope = list_scope();
            scope.parent_thread_id = Some(root.id.clone());
            scope.source_kinds = Some(vec![ThreadSourceKind::SubAgentThreadSpawn]);
            let discovered = self.list(scope).await?;
            let [child] = discovered.as_slice() else {
                bail!("expected exactly one saved child")
            };
            target = children.direct_child(&root.id, &child.id)?.to_owned();
            check_parent(child, &root.id)?;
            expected = vec![children.completed_turn(&target)?.to_owned()];
        }
        let metadata = self.read(&target, false).await?;
        if child_history {
            check_parent(&metadata, &root.id)?;
        }
        let turns = if parameters.history_target.as_deref() == Some("pagination") {
            let windows = self.pages(&target, 1).await?;
            let whole = self.pages(&target, 100).await?;
            ensure!(
                windows.len() >= 2 && windows == whole,
                "ordered native windows differ from complete native history"
            );
            serde_json::from_value(Value::Array(windows))?
        } else {
            let history = self.read(&target, true).await?;
            if child_history {
                check_parent(&history, &root.id)?;
                if parameters.child_configuration.is_some() {
                    let model = history
                        .model
                        .as_deref()
                        .filter(|model| !model.is_empty())
                        .context("child model was not exposed by native history")?;
                    if let Some(created) = children.created_model(&target) {
                        ensure!(
                            created == model,
                            "native child creation and saved model differ"
                        );
                    }
                    record_receipt(
                        self.report,
                        json!({"child-model": {
                            "rootObserved": root.model,
                            "childObserved": model, "childCreationModel": children.created_model(&target),
                            "childThreadId": target, "scope": "native thread model",
                        }}),
                    );
                }
            }
            history.turns
        };
        check_saved_turns(&turns, &expected)?;
        if let Some(requested) = &parameters.child_detail_policy {
            let detail = children
                .child_deltas
                .get(&target)
                .copied()
                .unwrap_or_default();
            ensure!(
                detail > 0,
                "no confirmed child detail was delivered by the SDK"
            );
            record_receipt(
                self.report,
                json!({"child-detail-selection": {
                    "rootId": root.id, "childIds": [target], "nativeDetailCount": detail,
                    "requested": requested, "scope": "native source; delivery is checked by Go SDK simulation",
                }}),
            );
        }
        Ok(child_history.then_some(target))
    }

    async fn observe(&mut self, seed: &seeds::Seed, cwd: &Path, model: &str) -> Result<()> {
        let request_id = self.id();
        let response: ThreadResumeResponse = self
            .client
            .request_typed(ClientRequest::ThreadResume {
                request_id,
                // The adopted material resumes with its ordinary saved history.
                params: ThreadResumeParams {
                    thread_id: seed.session_id.clone(),
                    cwd: Some(
                        cwd.to_str()
                            .context("seed project must be UTF-8")?
                            .to_owned(),
                    ),
                    model: Some(model.to_owned()),
                    ..Default::default()
                },
            })
            .await?;
        ensure!(
            response.thread.id == seed.session_id
                && !response.thread.ephemeral
                && response.thread.parent_thread_id.is_none()
                && response.model == model,
            "native observation resumed a different saved parent or model"
        );
        record_receipt(self.report, json!({"thread/resume": response}));
        let mut scope = list_scope();
        scope.ancestor_thread_id = Some(seed.session_id.clone());
        scope.source_kinds = Some(vec![ThreadSourceKind::SubAgentThreadSpawn]);
        let children = self.list(scope).await?;
        for child in &children {
            check_parent(child, &seed.session_id)?;
        }
        ensure!(
            children
                .iter()
                .map(|child| &child.id)
                .collect::<BTreeSet<_>>()
                == seed.agent_ids.iter().collect::<BTreeSet<_>>(),
            "native discovery differs from saved child set"
        );
        self.detach(&seed.session_id).await?;
        record_receipt(
            self.report,
            json!({"existing-children": {
                "sessionId": seed.session_id, "agentIds": seed.agent_ids,
            }}),
        );
        self.report["observationInputs"] = json!(0);
        Ok(())
    }
}

fn list_scope() -> ThreadListParams {
    ThreadListParams {
        limit: Some(10),
        cursor: None,
        include_attached_threads: false,
        current_account_only: false,
        included_thread_ids: None,
        excluded_thread_ids: None,
        sort_key: None,
        sort_direction: None,
        model_providers: None,
        source_kinds: None,
        originators: None,
        archived: None,
        section_id: None,
        project_id: None,
        cwd: None,
        use_state_db_only: false,
        search_term: None,
        parent_thread_id: None,
        ancestor_thread_id: None,
        attached_to_thread_id: None,
    }
}

fn next_cursor(
    cursor: Option<String>,
    size: usize,
    cursors: &mut BTreeSet<String>,
) -> Result<Option<String>> {
    if let Some(cursor) = &cursor {
        ensure!(
            size > 0 && !cursor.is_empty() && cursors.len() < 99 && cursors.insert(cursor.clone()),
            "native history cursor did not make bounded progress"
        );
    }
    Ok(cursor)
}

fn native_id(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(index, byte)| {
            if matches!(index, 8 | 13 | 18 | 23) {
                byte == b'-'
            } else {
                byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte)
            }
        })
}

fn check_parent(thread: &Thread, parent: &str) -> Result<()> {
    // The public parent field is derived from Codex's native spawn metadata.
    ensure!(
        !thread.ephemeral
            && thread.id != parent
            && thread.parent_thread_id.as_deref() == Some(parent),
        "saved native child does not identify its parent"
    );
    Ok(())
}

fn check_saved_turns(turns: &[Turn], expected: &[String]) -> Result<()> {
    ensure!(!turns.is_empty()
        && turns.iter().map(|turn| &turn.id).eq(expected.iter())
        && turns.iter().all(|turn| turn.status == TurnStatus::Completed && turn.error.is_none()
            && turn.items.iter().any(|item| matches!(&item.payload,
                codex_app_server_protocol::ThreadItemPayload::AgentMessage { text, .. } if !text.trim().is_empty()))),
        "saved turns lack the expected ordered identities, successful terminals or output");
    Ok(())
}
