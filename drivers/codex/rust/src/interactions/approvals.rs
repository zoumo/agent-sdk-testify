use std::collections::HashMap;
use std::path::Path;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    CollabAgentTool, CollabAgentToolCallStatus, CommandExecutionApprovalDecision,
    CommandExecutionApprovalKind, CommandExecutionRequestApprovalResponse, CommandExecutionStatus,
    DynamicToolCallStatus, FileChangeApprovalDecision, FileChangeRequestApprovalResponse,
    FileUpdateChange, ItemStartedNotification, McpServerElicitationAction,
    McpServerElicitationRequest, McpServerElicitationRequestResponse, McpToolCallStatus,
    PatchApplyStatus, PatchChangeKind, RequestId, ServerNotification, ServerRequest,
    ThreadItemPayload, ToolRequestUserInputAnswer, ToolRequestUserInputParams,
    ToolRequestUserInputResponse, TurnStatus,
};
use serde_json::{Value, json};

use crate::Plan;

use super::fixture_tools;

const COMMAND: &str = "printf SYNTHETIC_COMMAND";
const READ_COMMAND: &str = "cat numbers.txt";
const MCP_TOKEN: &str = "SYNTHETIC_MCP_APPROVED";

#[derive(Clone, Copy, PartialEq)]
enum Kind {
    Fixture,
    Question,
    File,
    Command,
    Mcp,
}

struct Answered {
    id: RequestId,
    thread: String,
    turn: String,
    item: String,
    kind: Kind,
    resolved: bool,
    completed: bool,
    response: Value,
}

pub(super) struct Approvals<'a> {
    plan: &'a Plan,
    workspace: &'a Path,
    execution_workspace: &'a Path,
    expected: Vec<String>,
    answered: Vec<Answered>,
    started: HashMap<String, ItemStartedNotification>,
    child: Option<String>,
    child_turn: Option<String>,
    child_completed: bool,
}

impl<'a> Approvals<'a> {
    pub(super) fn new(
        plan: &'a Plan,
        workspace: &'a Path,
        execution_workspace: &'a Path,
    ) -> Result<Self> {
        let expected: Vec<String> = match plan.scenario.get("requiredRequests") {
            Some(requests) => serde_json::from_value(requests.clone())
                .context("invalid native request binding")?,
            None => Vec::new(),
        };
        let required: &[&str] = match plan.driver_profile.as_str() {
            "fixture-tools" if plan.case_id == "SDK-CASE-006" => &["fixture_read", "fixture_write"],
            "fixture-tools" => &["fixture_read"],
            "approval" => &["approval_answer"],
            "deferred-approval" => &["approval_answer", "held-control", "held-event"],
            "questions" => &["question"],
            "mcp-tool-approval" => &["mcp-tool-approval"],
            _ => &[],
        };
        ensure!(
            expected.len() == required.len()
                && expected
                    .iter()
                    .zip(required)
                    .all(|(name, required)| name == required),
            "native request binding differs from the selected case"
        );
        // The control and notification are observed separately from native server requests.
        let expected = if plan.driver_profile == "deferred-approval" {
            ensure!(
                plan.scenario["files"] == json!({"numbers.txt":"17\n25\n"}),
                "unexpected deferred read fixture"
            );
            vec![expected[0].clone()]
        } else {
            expected
        };
        Ok(Self {
            plan,
            workspace,
            execution_workspace,
            expected,
            answered: Vec::new(),
            started: HashMap::new(),
            child: None,
            child_turn: None,
            child_completed: false,
        })
    }

    fn parameters(&self) -> &Value {
        &self.plan.scenario["parameters"]
    }

    fn check_request(&self, request: &ServerRequest, name: &str, item: &str) -> Result<()> {
        ensure!(
            self.expected
                .get(self.answered.len())
                .is_some_and(|expected| expected == name)
                && !item.is_empty()
                && !matches!(request.id(), RequestId::String(id) if id.is_empty())
                && !self
                    .answered
                    .iter()
                    .any(|reply| &reply.id == request.id() || reply.item == item)
                && self.answered.last().is_none_or(|reply| reply.completed),
            "unexpected, repeated or out-of-order native request {name}"
        );
        Ok(())
    }

    pub(super) fn answer(
        &mut self,
        request: &ServerRequest,
        root: &str,
        root_turn: &str,
    ) -> Result<Value> {
        let profile = self.plan.driver_profile.as_str();
        let (thread, turn, item, kind, response) = match request {
            ServerRequest::DynamicToolCall { params, .. } if profile == "fixture-tools" => {
                check_turn(root, root_turn, &params.thread_id, &params.turn_id)?;
                self.check_request(request, &params.tool, &params.call_id)?;
                let started = self
                    .started
                    .get(&params.call_id)
                    .context("missing native dynamic tool item")?;
                let response = fixture_tools::answer(
                    params,
                    &started.item.payload,
                    self.workspace,
                    self.execution_workspace,
                    &self.plan.scenario["files"],
                )?;
                (
                    &params.thread_id,
                    &params.turn_id,
                    params.call_id.as_str(),
                    Kind::Fixture,
                    serde_json::to_value(response)?,
                )
            }
            ServerRequest::ToolRequestUserInput { params, .. } if profile == "questions" => {
                check_turn(root, root_turn, &params.thread_id, &params.turn_id)?;
                self.check_request(request, "question", &params.item_id)?;
                (
                    &params.thread_id,
                    &params.turn_id,
                    params.item_id.as_str(),
                    Kind::Question,
                    serde_json::to_value(self.questions(params)?)?,
                )
            }
            ServerRequest::FileChangeRequestApproval { params, .. } if profile == "approval" => {
                check_turn(root, root_turn, &params.thread_id, &params.turn_id)?;
                self.check_request(request, "approval_answer", &params.item_id)?;
                ensure!(
                    self.parameters()["approvalKind"] == "file"
                        && self.parameters()["nativeFileTool"] == "apply_patch"
                        && params.grant_root.is_none(),
                    "unexpected file approval kind, tool or grant root"
                );
                self.check_file_change(&params.item_id)?;
                let decision = if self.allow()? {
                    FileChangeApprovalDecision::Accept
                } else {
                    FileChangeApprovalDecision::Decline
                };
                (
                    &params.thread_id,
                    &params.turn_id,
                    params.item_id.as_str(),
                    Kind::File,
                    serde_json::to_value(FileChangeRequestApprovalResponse { decision })?,
                )
            }
            ServerRequest::CommandExecutionRequestApproval { params, .. }
                if matches!(profile, "approval" | "deferred-approval") =>
            {
                check_turn(root, root_turn, &params.thread_id, &params.turn_id)?;
                self.check_request(request, "approval_answer", &params.item_id)?;
                let deferred = profile == "deferred-approval";
                let expected_command = if deferred { READ_COMMAND } else { COMMAND };
                ensure!(
                    (deferred || self.parameters()["approvalKind"] == "command")
                        && params.kind == CommandExecutionApprovalKind::Command
                        && params.approval_id.is_none()
                        && params.additional_permissions.is_none()
                        && params.network_approval_context.is_none()
                        && params
                            .proposed_network_policy_amendments
                            .as_ref()
                            .is_none_or(Vec::is_empty)
                        && params.cwd.as_ref().map(|path| path.as_str()) == self.workspace.to_str()
                        && params
                            .command
                            .as_deref()
                            .is_some_and(|command| allowed_command(command, expected_command)),
                    "command approval is outside the synthetic command and workspace"
                );
                let Some(ThreadItemPayload::CommandExecution {
                    command,
                    cwd,
                    status,
                    ..
                }) = self
                    .started
                    .get(&params.item_id)
                    .map(|item| &item.item.payload)
                else {
                    bail!("missing native command item");
                };
                ensure!(
                    params.command.as_ref() == Some(command)
                        && Some(cwd.as_str()) == self.workspace.to_str()
                        && *status == CommandExecutionStatus::InProgress,
                    "command approval differs from its native item"
                );
                let decision = if deferred || self.allow()? {
                    ensure!(
                        params
                            .available_decisions
                            .as_ref()
                            .is_none_or(|decisions| decisions
                                .contains(&CommandExecutionApprovalDecision::Accept)),
                        "one-time approval unavailable"
                    );
                    CommandExecutionApprovalDecision::Accept
                } else {
                    // Decline preserves the turn for the denial scenario. Cancel interrupts it.
                    CommandExecutionApprovalDecision::Decline
                };
                (
                    &params.thread_id,
                    &params.turn_id,
                    params.item_id.as_str(),
                    Kind::Command,
                    serde_json::to_value(CommandExecutionRequestApprovalResponse { decision })?,
                )
            }
            ServerRequest::McpServerElicitationRequest { params, .. }
                if profile == "mcp-tool-approval" =>
            {
                let turn = params
                    .turn_id
                    .as_ref()
                    .context("MCP approval lacks turn identity")?;
                match self.parameters()["approvalThread"].as_str() {
                    Some("root") if self.plan.case_id == "SDK-CASE-039" => {
                        check_turn(root, root_turn, &params.thread_id, turn)?
                    }
                    Some("child") if self.plan.case_id == "SDK-CASE-040" => {
                        ensure!(
                            self.child.as_ref() == Some(&params.thread_id)
                                && self.child_turn.as_ref() == Some(turn)
                                && !self.child_completed,
                            "MCP approval did not originate on the confirmed child turn"
                        );
                    }
                    _ => bail!("unexpected MCP source binding"),
                }
                let McpServerElicitationRequest::Form {
                    meta: Some(meta),
                    message,
                    requested_schema,
                } = &params.request
                else {
                    bail!("unexpected MCP approval form");
                };
                ensure!(
                    params.server_name == "approval_fixture"
                        && message
                            == "Allow the approval_fixture MCP server to run tool \"token\"?"
                        && meta["codex_approval_kind"] == "mcp_tool_call"
                        && meta["tool_params"] == json!({})
                        && meta.get("tool_name").is_none_or(|tool| tool == "token")
                        && requested_schema.properties.is_empty(),
                    "unexpected MCP approval metadata"
                );
                // The protocol does not carry an MCP item ID in the approval itself.
                // Observe the sole preceding token call on this thread and turn.
                let candidates: Vec<_> = self.started.iter().filter(|(_, item)| item.thread_id == params.thread_id && item.turn_id == *turn && matches!(&item.item.payload,
                    ThreadItemPayload::McpToolCall { server, tool, status: McpToolCallStatus::InProgress,
                        arguments, .. } if server == "approval_fixture" && tool == "token" && arguments == &json!({})
                )).collect();
                let [(item, _)] = candidates.as_slice() else {
                    bail!("missing or ambiguous MCP token item");
                };
                self.check_request(request, "mcp-tool-approval", item)?;
                (
                    &params.thread_id,
                    turn,
                    item.as_str(),
                    Kind::Mcp,
                    serde_json::to_value(McpServerElicitationRequestResponse {
                        action: McpServerElicitationAction::Accept,
                        content: None,
                        meta: None,
                    })?,
                )
            }
            _ => bail!("unexpected native server request for {}", self.plan.case_id),
        };
        self.answered.push(Answered {
            id: request.id().clone(),
            thread: thread.clone(),
            turn: turn.clone(),
            item: item.into(),
            kind,
            resolved: false,
            completed: false,
            response: response.clone(),
        });
        Ok(response)
    }

    pub(super) fn observe(
        &mut self,
        event: &ServerNotification,
        root: &str,
        root_turn: &str,
    ) -> Result<Option<Value>> {
        if self.expected.is_empty() {
            return Ok(None);
        }
        if let ServerNotification::ServerRequestResolved(resolved) = event {
            let reply = self
                .answered
                .iter_mut()
                .find(|reply| reply.id == resolved.request_id)
                .context("resolved request has no native answer")?;
            ensure!(
                reply.thread == resolved.thread_id
                    && reply.kind != Kind::Fixture
                    && !reply.resolved,
                "unexpected or repeated native request resolution"
            );
            reply.resolved = true;
            if reply.kind == Kind::Question {
                reply.completed = true;
            }
            return Ok(Some(json!({"serverRequest/resolved":resolved})));
        }
        if self.plan.case_id == "SDK-CASE-040" {
            match event {
                ServerNotification::ItemCompleted(item)
                    if item.thread_id == root && item.turn_id == root_turn =>
                {
                    if let ThreadItemPayload::CollabAgentToolCall {
                        tool: CollabAgentTool::SpawnAgent,
                        status: CollabAgentToolCallStatus::Completed,
                        sender_thread_id,
                        receiver_thread_ids,
                        ..
                    } = &item.item.payload
                    {
                        let [child] = receiver_thread_ids.as_slice() else {
                            bail!("expected one spawned child");
                        };
                        ensure!(
                            sender_thread_id == root
                                && child != root
                                && !child.is_empty()
                                && self.child.is_none(),
                            "unconfirmed or repeated root-child relationship"
                        );
                        self.child = Some(child.clone());
                        return Ok(Some(json!({"spawned-child":item})));
                    }
                }
                ServerNotification::TurnStarted(start)
                    if self.child.as_ref() == Some(&start.thread_id) =>
                {
                    ensure!(
                        self.child_turn.is_none()
                            && start
                                .turn
                                .root_turn_id
                                .as_deref()
                                .is_none_or(|id| id == root_turn)
                            && start.turn.id != root_turn
                            && !start.turn.id.is_empty()
                            && start.turn.status == TurnStatus::InProgress,
                        "unexpected child turn start"
                    );
                    self.child_turn = Some(start.turn.id.clone());
                    return Ok(Some(json!({"child-turn/started":start})));
                }
                ServerNotification::TurnCompleted(done)
                    if self.child.as_ref() == Some(&done.thread_id) =>
                {
                    ensure!(
                        !self.child_completed
                            && self.child_turn.as_ref() == Some(&done.turn.id)
                            && done
                                .turn
                                .root_turn_id
                                .as_deref()
                                .is_none_or(|id| id == root_turn)
                            && done.turn.status == TurnStatus::Completed
                            && done.turn.error.is_none()
                            && self
                                .answered
                                .iter()
                                .any(|reply| reply.thread == done.thread_id && reply.completed),
                        "child turn lacks an associated completed MCP result"
                    );
                    self.child_completed = true;
                    return Ok(Some(json!({"child-turn/completed":done})));
                }
                _ => {}
            }
        }

        let relevant = |thread: &str, turn: &str| {
            (thread == root && turn == root_turn)
                || (self.child.as_deref() == Some(thread)
                    && self.child_turn.as_deref() == Some(turn))
        };
        match event {
            ServerNotification::ItemStarted(item)
                if relevant(&item.thread_id, &item.turn_id)
                    && tool_kind(&item.item.payload).is_some_and(|kind| self.observes(kind)) =>
            {
                let id = item.item.id();
                ensure!(
                    !id.is_empty() && self.started.insert(id.to_owned(), item.clone()).is_none(),
                    "missing or duplicate native tool item identity"
                );
                Ok(Some(json!({"tool-item/started":item})))
            }
            ServerNotification::ItemCompleted(item)
                if relevant(&item.thread_id, &item.turn_id)
                    && tool_kind(&item.item.payload).is_some_and(|kind| self.observes(kind)) =>
            {
                let reply = self
                    .answered
                    .iter_mut()
                    .find(|reply| reply.item == item.item.id())
                    .context("native tool completed without its correlated request")?;
                ensure!(
                    !reply.completed
                        && reply.thread == item.thread_id
                        && reply.turn == item.turn_id
                        && (reply.kind == Kind::Fixture || reply.resolved),
                    "native tool completion lacks the matching answer or resolution"
                );
                let started = &self.started[&reply.item].item.payload;
                check_tool_result(reply, started, &item.item.payload)?;
                reply.completed = true;
                Ok(Some(json!({"tool-item/completed":item})))
            }
            _ => Ok(None),
        }
    }

    fn observes(&self, kind: Kind) -> bool {
        match (
            self.plan.driver_profile.as_str(),
            self.parameters()["approvalKind"].as_str(),
        ) {
            ("fixture-tools", _) => kind == Kind::Fixture,
            ("approval", Some("file")) => kind == Kind::File,
            ("approval", Some("command")) | ("deferred-approval", _) => kind == Kind::Command,
            ("mcp-tool-approval", _) => kind == Kind::Mcp,
            _ => false,
        }
    }

    pub(super) fn finish(&self) -> Result<()> {
        ensure!(
            self.answered.len() == self.expected.len()
                && self.answered.iter().all(|reply| reply.completed)
                && (self.plan.case_id != "SDK-CASE-040" || self.child_completed),
            "required native requests, resolutions or tool completions are absent"
        );
        Ok(())
    }

    fn allow(&self) -> Result<bool> {
        match self.parameters()["approvalDecision"].as_str() {
            Some("allow") => Ok(true),
            Some("deny") => Ok(false),
            _ => bail!("invalid approval decision binding"),
        }
    }

    fn check_file_change(&self, id: &str) -> Result<()> {
        let (file, contents) = if self.allow()? {
            ("approved.txt", "SYNTHETIC_APPROVED\n")
        } else {
            ("denied.txt", "denied\n")
        };
        let Some(ThreadItemPayload::FileChange {
            changes,
            status: PatchApplyStatus::InProgress,
            ..
        }) = self.started.get(id).map(|item| &item.item.payload)
        else {
            bail!("missing native file-change item");
        };
        let [
            FileUpdateChange {
                path,
                kind: PatchChangeKind::Add,
                diff,
            },
        ] = changes.as_slice()
        else {
            bail!("file approval is not a single fixture addition");
        };
        ensure!(
            Some(path.as_str()) == self.workspace.join(file).to_str() && diff == contents,
            "file approval is outside the prescribed fixture change"
        );
        Ok(())
    }

    fn questions(
        &self,
        params: &ToolRequestUserInputParams,
    ) -> Result<ToolRequestUserInputResponse> {
        let parameters = self.parameters();
        let count = parameters["questionCount"]
            .as_u64()
            .context("missing question count binding")?;
        let free_text = parameters["freeText"]
            .as_bool()
            .context("missing question mode binding")?;
        let choice = self.plan.scenario["answers"]["choice"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("missing fixture answer")?;
        ensure!(
            params.questions.len() as u64 == count && count > 0,
            "native question count differs from binding"
        );
        let mut answers = HashMap::new();
        for question in &params.questions {
            ensure!(
                !question.id.is_empty() && !question.question.is_empty() && !question.is_secret,
                "unexpected question identity or secret field"
            );
            let text = if free_text {
                ensure!(
                    question.is_other || question.options.is_none(),
                    "no native free-text input"
                );
                choice
            } else {
                let options = question
                    .options
                    .as_deref()
                    .filter(|items| !items.is_empty())
                    .context("native choice question has no options")?;
                // The binding's configured choice may have a native label decoration.
                options
                    .iter()
                    .find(|option| {
                        option.label == choice
                            || option.label.strip_suffix(" (Recommended)") == Some(choice)
                    })
                    .context("configured answer is not a native option")?
                    .label
                    .as_str()
            };
            ensure!(
                !text.is_empty()
                    && answers
                        .insert(
                            question.id.clone(),
                            ToolRequestUserInputAnswer {
                                answers: vec![text.into()]
                            }
                        )
                        .is_none(),
                "missing answer or duplicate native question ID"
            );
        }
        Ok(ToolRequestUserInputResponse { answers })
    }
}

fn check_turn(root: &str, root_turn: &str, thread: &str, turn: &str) -> Result<()> {
    ensure!(
        thread == root && turn == root_turn,
        "native request belongs to another thread or turn"
    );
    Ok(())
}

fn allowed_command(command: &str, expected: &str) -> bool {
    command == expected
        || command == format!("/bin/zsh -c '{expected}'")
        || command == format!("/bin/zsh -lc '{expected}'")
        || command == format!("/bin/bash -lc '{expected}'")
}

fn tool_kind(payload: &ThreadItemPayload) -> Option<Kind> {
    match payload {
        ThreadItemPayload::DynamicToolCall { .. } => Some(Kind::Fixture),
        ThreadItemPayload::FileChange { .. } => Some(Kind::File),
        ThreadItemPayload::CommandExecution { .. } => Some(Kind::Command),
        ThreadItemPayload::McpToolCall { .. } => Some(Kind::Mcp),
        _ => None,
    }
}

fn check_tool_result(
    reply: &Answered,
    started: &ThreadItemPayload,
    item: &ThreadItemPayload,
) -> Result<()> {
    ensure!(
        tool_kind(item) == Some(reply.kind),
        "terminal native item differs from its request kind"
    );
    match item {
        ThreadItemPayload::DynamicToolCall {
            status,
            content_items,
            success,
            ..
        } => {
            ensure!(
                *status == DynamicToolCallStatus::Completed
                    && *success == Some(true)
                    && serde_json::to_value(content_items)? == reply.response["contentItems"],
                "native fixture tool did not consume the correlated answer"
            );
        }
        ThreadItemPayload::FileChange {
            status, changes, ..
        } => {
            ensure!(
                matches!(started, ThreadItemPayload::FileChange { changes: original, .. } if original == changes),
                "file result differs from the approved fixture change"
            );
            let accepted = reply.response["decision"] == "accept";
            // The case observes the native result of an approval, including a
            // post-approval sandbox/application failure in the adopted material.
            ensure!(
                if accepted {
                    matches!(
                        status,
                        PatchApplyStatus::Completed | PatchApplyStatus::Failed
                    )
                } else {
                    *status == PatchApplyStatus::Declined
                },
                "file result contradicts the approval decision"
            );
        }
        ThreadItemPayload::CommandExecution {
            status,
            exit_code,
            command,
            cwd,
            aggregated_output,
            ..
        } => {
            ensure!(
                matches!(started, ThreadItemPayload::CommandExecution { command: original, cwd: dir, .. }
                if original == command && dir == cwd),
                "command result differs from the approved fixture command"
            );
            ensure!(
                if reply.response["decision"] == "accept" {
                    *status == CommandExecutionStatus::Completed
                        && *exit_code == Some(0)
                        && aggregated_output.as_deref()
                            == Some(if allowed_command(command, READ_COMMAND) {
                                "17\n25\n"
                            } else {
                                "SYNTHETIC_COMMAND"
                            })
                } else {
                    *status == CommandExecutionStatus::Declined && exit_code.is_none()
                },
                "command result contradicts the approval decision"
            );
        }
        ThreadItemPayload::McpToolCall {
            server,
            tool,
            result,
            error,
            status,
            ..
        } => {
            ensure!(
                server == "approval_fixture"
                    && tool == "token"
                    && error.is_none()
                    && *status == McpToolCallStatus::Completed
                    && serde_json::to_value(result)?["content"]
                        == json!([{"type":"text","text":MCP_TOKEN}]),
                "MCP approval lacks the corresponding synthetic token result"
            );
        }
        _ => bail!("unexpected native tool result"),
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{COMMAND, READ_COMMAND, allowed_command};

    #[test]
    fn permits_exact_native_non_login_shell_command() {
        assert!(allowed_command(
            "/bin/zsh -lc 'cat numbers.txt'",
            READ_COMMAND
        ));
        assert!(!allowed_command(
            "cat numbers.txt; cat extra.txt",
            READ_COMMAND
        ));
        assert!(!allowed_command("cat numbers.txt", COMMAND));
        assert!(allowed_command(
            "/bin/zsh -c 'printf SYNTHETIC_COMMAND'",
            COMMAND
        ));
        assert!(allowed_command(
            "/bin/zsh -lc 'printf SYNTHETIC_COMMAND'",
            COMMAND
        ));
        assert!(!allowed_command(
            "/bin/zsh -c 'printf SYNTHETIC_COMMAND; printf EXTRA'",
            COMMAND
        ));
        assert!(!allowed_command("/bin/zsh -c 'printf OTHER'", COMMAND));
    }
}
