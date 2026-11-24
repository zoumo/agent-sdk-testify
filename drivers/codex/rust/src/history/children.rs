use std::collections::{BTreeMap, BTreeSet};
use std::fs;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    CollabAgentTool, CollabAgentToolCallStatus, DynamicToolCallOutputContentItem,
    DynamicToolCallParams, DynamicToolCallResponse, DynamicToolSpec, JSONRPCErrorError, RequestId,
    ServerNotification, SubAgentActivityKind, ThreadItemPayload, TurnCompletedNotification,
    TurnStatus,
};
use serde_json::json;

use super::{Query, check_parent, check_saved_turns, native_id};
use crate::{Plan, Runtime, record_receipt};

const FIXTURE: &str = "child-fixture.txt";
const FIXTURE_TOOL: &str = "fixture_read";

pub(super) fn fixture_tool() -> Result<Vec<DynamicToolSpec>> {
    // DynamicToolSpec is public, but the function struct it wraps is not re-exported.
    Ok(serde_json::from_value(json!([{
        "type": "function", "name": FIXTURE_TOOL,
        "description": "Child-only: read the single synthetic fixture exactly once.",
        "inputSchema": {
            "type": "object", "properties": {"path": {"type": "string", "enum": [FIXTURE]}},
            "required": ["path"], "additionalProperties": false,
        },
    }]))?)
}

#[derive(Default)]
struct TurnObservation {
    started: Option<usize>,
    completed: Option<usize>,
    output: bool,
}

struct ParentEdge {
    parent: String,
    parent_turn: String,
    created_at: usize,
    completed_at: Option<usize>,
    model: Option<String>,
}

#[derive(Default)]
pub(super) struct Observed {
    ordinal: usize,
    edges: BTreeMap<String, ParentEdge>,
    turns: BTreeMap<(String, String), TurnObservation>,
    pub(super) child_deltas: BTreeMap<String, usize>,
    fixture_read: Option<(String, String, String)>,
}

impl Observed {
    pub(super) fn accept<'a>(
        &mut self,
        event: &'a ServerNotification,
    ) -> Result<Option<&'a TurnCompletedNotification>> {
        self.ordinal += 1;
        match event {
            ServerNotification::TurnStarted(started) => {
                let ordinal = self.ordinal;
                let observed = self.turn(&started.thread_id, &started.turn.id)?;
                ensure!(
                    observed.started.replace(ordinal).is_none(),
                    "duplicate native turn start"
                );
            }
            ServerNotification::TurnCompleted(completed) => {
                ensure!(
                    completed.turn.status == TurnStatus::Completed
                        && completed.turn.error.is_none(),
                    "native turn failed"
                );
                let ordinal = self.ordinal;
                let observed = self.turn(&completed.thread_id, &completed.turn.id)?;
                ensure!(
                    observed.completed.replace(ordinal).is_none(),
                    "duplicate native turn terminal"
                );
                return Ok(Some(completed));
            }
            ServerNotification::ItemCompleted(completed) => match &completed.item.payload {
                ThreadItemPayload::SubAgentActivity {
                    kind,
                    agent_thread_id,
                    model,
                    ..
                } => match kind {
                    SubAgentActivityKind::Started => self.spawn(
                        &completed.thread_id,
                        &completed.turn_id,
                        agent_thread_id,
                        model.clone(),
                    )?,
                    SubAgentActivityKind::Completed => {
                        let ordinal = self.ordinal;
                        let edge = self
                            .edges
                            .get_mut(agent_thread_id)
                            .context("unknown completed child")?;
                        ensure!(
                            edge.parent == completed.thread_id
                                && edge.parent_turn == completed.turn_id
                                && edge.completed_at.replace(ordinal).is_none(),
                            "native child completion changed its parent relation"
                        );
                    }
                    _ => {}
                },
                ThreadItemPayload::CollabAgentToolCall {
                    tool: CollabAgentTool::SpawnAgent,
                    status: CollabAgentToolCallStatus::Completed,
                    sender_thread_id,
                    receiver_thread_ids,
                    ..
                } => {
                    ensure!(
                        sender_thread_id == &completed.thread_id,
                        "native spawn changed sender"
                    );
                    // Legacy collaboration exposes native receivers on the completed spawn.
                    // Its optional model belongs to the request, rather than a resolved setting.
                    for child in receiver_thread_ids {
                        self.spawn(&completed.thread_id, &completed.turn_id, child, None)?;
                    }
                }
                ThreadItemPayload::AgentMessage { text, .. } if !text.trim().is_empty() => {
                    self.turn(&completed.thread_id, &completed.turn_id)?.output = true;
                }
                _ => {}
            },
            ServerNotification::AgentMessageDelta(delta) if !delta.delta.is_empty() => {
                if self.edges.contains_key(&delta.thread_id) {
                    *self
                        .child_deltas
                        .entry(delta.thread_id.clone())
                        .or_default() += 1;
                }
            }
            _ => {}
        }
        Ok(None)
    }

    fn turn(&mut self, thread: &str, id: &str) -> Result<&mut TurnObservation> {
        ensure!(
            native_id(thread) && native_id(id),
            "invalid native thread or turn identity"
        );
        Ok(self
            .turns
            .entry((thread.to_owned(), id.to_owned()))
            .or_default())
    }

    fn spawn(
        &mut self,
        parent: &str,
        turn: &str,
        child: &str,
        model: Option<String>,
    ) -> Result<()> {
        ensure!(
            native_id(parent) && native_id(child) && native_id(turn) && parent != child,
            "invalid native spawn identities"
        );
        let edge = self
            .edges
            .entry(child.to_owned())
            .or_insert_with(|| ParentEdge {
                parent: parent.to_owned(),
                parent_turn: turn.to_owned(),
                created_at: self.ordinal,
                completed_at: None,
                model: None,
            });
        ensure!(
            edge.parent == parent
                && edge.parent_turn == turn
                && (edge.model.is_none() || model.is_none() || edge.model == model),
            "conflicting native spawn evidence"
        );
        if model.is_some() {
            edge.model = model;
        }
        Ok(())
    }

    pub(super) fn direct_child<'a>(&self, parent: &str, child: &'a str) -> Result<&'a str> {
        ensure!(
            self.edges
                .get(child)
                .is_some_and(|edge| edge.parent == parent),
            "saved child was not identified in native spawn activity"
        );
        Ok(child)
    }

    pub(super) fn single_child(&self, parent: &str) -> Result<&str> {
        let mut children = self.edges.iter();
        let (child, edge) = children.next().context("missing native child")?;
        ensure!(
            edge.parent == parent && children.next().is_none(),
            "expected one direct native child"
        );
        Ok(child)
    }

    pub(super) fn created_model(&self, child: &str) -> Option<&str> {
        self.edges.get(child)?.model.as_deref()
    }

    fn completed(&self, child: &str) -> Result<(&str, usize, usize)> {
        let mut turns = self.turns.iter().filter(|((thread, _), _)| thread == child);
        let ((_, id), turn) = turns.next().context("native child turn is absent")?;
        ensure!(
            turns.next().is_none() && turn.output,
            "expected one native child turn with output"
        );
        let (start, end) = turn
            .started
            .zip(turn.completed)
            .context("native child turn is incomplete")?;
        ensure!(
            start < end,
            "native child turn completion preceded its start"
        );
        Ok((id, start, end))
    }

    pub(super) fn completed_turn(&self, child: &str) -> Result<&str> {
        Ok(self.completed(child)?.0)
    }

    pub(super) fn check_setup(&self, parent: &str, parent_turn: &str, child: &str) -> Result<()> {
        let edge = &self.edges[child];
        let (_, _, child_end) = self.completed(child)?;
        let root_end = self.turns[&(parent.to_owned(), parent_turn.to_owned())]
            .completed
            .context("setup root was not completed")?;
        ensure!(
            edge.parent_turn == parent_turn
                && edge.created_at < child_end
                && edge
                    .completed_at
                    .is_some_and(|end| child_end <= end && end < root_end),
            "setup child creation and result do not precede the root result"
        );
        Ok(())
    }

    pub(super) async fn fixture_read(
        &mut self,
        query: &mut Query<'_>,
        runtime: &Runtime,
        root: &str,
        id: &RequestId,
        params: &DynamicToolCallParams,
        post_root: bool,
    ) -> Result<()> {
        let background = query.report["caseId"] == "SDK-CASE-019";
        let observed_turn = self
            .turns
            .get(&(params.thread_id.clone(), params.turn_id.clone()));
        let authorized = params.tool == FIXTURE_TOOL
            && params.namespace.is_none()
            && !params.call_id.is_empty()
            && params.arguments == json!({"path": FIXTURE})
            && self.fixture_read.is_none()
            && self.edges.len() == 1
            && self
                .edges
                .get(&params.thread_id)
                .is_some_and(|edge| edge.parent == root)
            && observed_turn.is_some_and(|turn| turn.started.is_some() && turn.completed.is_none())
            && post_root == background;
        if !authorized {
            query
                .client
                .reject_server_request(
                    id.clone(),
                    JSONRPCErrorError {
                        code: -32602,
                        message: "Unauthorized child fixture_read request".into(),
                        data: None,
                    },
                )
                .await?;
            bail!("unauthorized child fixture_read request");
        }
        let path = runtime.workspace.join(FIXTURE);
        ensure!(
            fs::symlink_metadata(&path)?.is_file(),
            "child fixture is not a regular file"
        );
        let text = fs::read_to_string(path)?;
        ensure!(
            text == "SYNTHETIC_CHILD_TOKEN\n",
            "child fixture text differs from case input"
        );
        let response = DynamicToolCallResponse {
            content_items: vec![DynamicToolCallOutputContentItem::InputText { text: text.clone() }],
            success: true,
        };
        query
            .client
            .resolve_server_request(id.clone(), serde_json::to_value(response)?)
            .await?;
        self.fixture_read = Some((params.thread_id.clone(), params.turn_id.clone(), text));
        record_receipt(
            query.report,
            json!({"fixture_read": {
                "requestId": id, "params": params, "afterRootResult": post_root,
            }}),
        );
        Ok(())
    }
}

pub(super) async fn check_live(
    query: &mut Query<'_>,
    plan: &Plan,
    root: &str,
    observed: &mut Observed,
    runtime: &Runtime,
) -> Result<()> {
    if plan.case_id == "SDK-CASE-019" {
        let child = observed.single_child(root)?.to_owned();
        let active = query.read(&child, true).await?;
        check_parent(&active, root)?;
        let active_turn = active
            .turns
            .iter()
            .find(|turn| turn.status == TurnStatus::InProgress)
            .context("background child was not active after root result")?;
        query
            .until_completion(
                &child,
                &active_turn.id,
                observed,
                Some((runtime, root)),
                true,
            )
            .await?;
    }

    let ids: Vec<_> = observed.edges.keys().cloned().collect();
    let expect_tool = matches!(plan.case_id.as_str(), "SDK-CASE-016" | "SDK-CASE-019");
    ensure!(
        ids.len() == if expect_tool { 1 } else { 2 },
        "wrong number of native children"
    );
    let mut visited = BTreeSet::from([root]);
    if !expect_tool {
        let root_history = query.read(root, true).await?;
        check_saved_turns(
            &root_history.turns,
            &[observed.completed_turn(root)?.to_owned()],
        )?;
    }
    let mut intervals = Vec::new();
    for id in &ids {
        let edge = &observed.edges[id];
        let thread = query.read(id, true).await?;
        check_parent(&thread, &edge.parent)?;
        let (turn, start, end) = observed.completed(id)?;
        check_saved_turns(&thread.turns, &[turn.to_owned()])?;
        if expect_tool {
            let (caller, called_turn, text) = observed
                .fixture_read
                .as_ref()
                .context("dynamic child fixture read was not observed")?;
            ensure!(caller == id && called_turn == turn && edge.parent == root
                && thread.turns[0].items.iter().any(|item| matches!(&item.payload,
                    ThreadItemPayload::AgentMessage { text: output, .. } if output.trim() == text.trim())),
                "fixture read and native child result differ");
        }
        intervals.push((start, end));
        record_receipt(
            query.report,
            json!({"child-evidence": {
                "parentThreadId": edge.parent, "threadId": id, "turnId": turn,
                "startedEvent": start, "completedEvent": end,
            }}),
        );
    }
    // Traverse confirmed parent edges. A display path or UUID sort cannot imply depth.
    loop {
        let count = visited.len();
        for (child, edge) in &observed.edges {
            if visited.contains(edge.parent.as_str()) {
                visited.insert(child.as_str());
            }
        }
        if visited.len() == count {
            break;
        }
    }
    ensure!(
        visited.len() == ids.len() + 1,
        "spawn topology is disconnected or cyclic"
    );
    let direct = observed
        .edges
        .values()
        .filter(|edge| edge.parent == root)
        .count();
    if plan.case_id == "SDK-CASE-017" {
        ensure!(direct == 1, "native grandchild edge is absent");
    }
    if plan.case_id == "SDK-CASE-018" {
        let [(first_start, first_end), (second_start, second_end)] = intervals.as_slice() else {
            bail!("native parallel intervals are absent")
        };
        ensure!(
            direct == 2 && first_start.max(second_start) < first_end.min(second_end),
            "native child activity intervals did not overlap"
        );
    }
    Ok(())
}
