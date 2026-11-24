use std::collections::HashSet;
use std::path::Path;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    JSONRPCErrorError, ServerNotification, ServerRequest, ThreadItemPayload, TurnStartResponse,
    TurnStatus, UserInput,
};
use codex_app_server_remote_client::{AppServerEvent, RemoteAppServerClient, TypedRequestError};
use serde::de::DeserializeOwned;
use serde_json::{Value, json};
use tokio::time::timeout;

use crate::{Plan, record_receipt, request};

mod approvals;
mod fixture_tools;
mod settings;

pub(crate) fn case_spec(case_id: &str) -> Option<(&'static str, &'static str, usize)> {
    Some(match case_id {
        "SDK-CASE-002" | "SDK-CASE-004" => ("SDK-US-001", "structured-input", 1),
        "SDK-CASE-005" => ("SDK-US-001", "active-input", 2),
        "SDK-CASE-006" | "SDK-CASE-007" => ("SDK-US-002", "fixture-tools", 1),
        "SDK-CASE-008" | "SDK-CASE-009" | "SDK-CASE-010" | "SDK-CASE-011" => {
            ("SDK-US-003", "approval", 1)
        }
        "SDK-CASE-013" | "SDK-CASE-014" | "SDK-CASE-015" => ("SDK-US-004", "questions", 1),
        "SDK-CASE-021" | "SDK-CASE-024" | "SDK-CASE-028" => ("SDK-US-006", "structured-input", 1),
        "SDK-CASE-022" => ("SDK-US-006", "configuration-timing", 2),
        "SDK-CASE-023" | "SDK-CASE-027" => ("SDK-US-006", "structured-input", 2),
        "SDK-CASE-030" => ("SDK-US-007", "interrupt", 1),
        "SDK-CASE-031" => ("SDK-US-007", "session-close", 1),
        "SDK-CASE-037" | "SDK-CASE-038" => ("SDK-US-001", "context-injection", 2),
        "SDK-CASE-039" | "SDK-CASE-040" => ("SDK-US-003", "mcp-tool-approval", 1),
        _ => return None,
    })
}

pub(crate) fn configure_thread_start(plan: &Plan, params: &mut Value) -> Result<()> {
    if plan.driver_profile == "fixture-tools" {
        params["dynamicTools"] = fixture_tools::tool_specs();
    }
    settings::configure_creation(plan, params)
}

struct Interaction<'a> {
    client: &'a mut RemoteAppServerClient,
    thread: &'a str,
    report: &'a mut Value,
    next_id: i64,
}

impl Interaction<'_> {
    async fn request<T: DeserializeOwned>(
        &mut self,
        method: &str,
        params: Value,
    ) -> std::result::Result<T, TypedRequestError> {
        let id = self.next_id;
        self.next_id += 1;
        request(self.client, id, method, params).await
    }

    fn record(&mut self, key: &str, value: Value) {
        record_receipt(self.report, json!({key: value}));
    }

    async fn submit(
        &mut self,
        input: Value,
    ) -> std::result::Result<TurnStartResponse, TypedRequestError> {
        self.report["inputs"] = json!(self.report["inputs"].as_u64().unwrap_or(0) + 1);
        self.request(
            "turn/start",
            json!({"threadId": self.thread, "input": input}),
        )
        .await
    }

    async fn server_request(
        &mut self,
        request: &ServerRequest,
        answer: Result<Value>,
    ) -> Result<()> {
        match answer {
            Ok(answer) => {
                self.client
                    .resolve_server_request(request.id().clone(), answer)
                    .await?
            }
            Err(error) => {
                self.client
                    .reject_server_request(
                        request.id().clone(),
                        JSONRPCErrorError {
                            code: -32602,
                            message: error.to_string(),
                            data: None,
                        },
                    )
                    .await
                    .context("reject invalid native request")?;
                return Err(error);
            }
        }
        Ok(())
    }

    async fn inject(&mut self, fact: &str, turn: Option<&str>) -> Result<()> {
        let params = json!({"threadId": self.thread, "items": [
            {"type":"message","role":"user","content":[
                {"type":"input_text","text":format!("The stored vault code is {fact}.")}
            ]}
        ]});
        let ack: Value = self.request("thread/inject_items", params.clone()).await?;
        ensure!(
            ack == json!({}),
            "context injection must return an empty object"
        );
        self.record(
            "context-injected",
            json!({"activeTurnId": turn, "requested": params, "response": ack}),
        );
        Ok(())
    }

    async fn idle_barrier(&mut self) -> Result<()> {
        let barrier: Value = self.request("thread/loaded/list", json!({})).await?;
        ensure!(
            barrier["data"]
                .as_array()
                .is_some_and(|ids| ids.iter().any(|id| id == self.thread)),
            "injection barrier did not observe the loaded thread"
        );
        self.record("injection-barrier", barrier);
        // The worker queues notifications encountered before the barrier response.
        // Check that queue before sending the next explicit query.
        while let Ok(event) = timeout(Duration::ZERO, self.client.next_event()).await {
            let Some(AppServerEvent::ServerNotification(event)) = event else {
                bail!("unexpected event during idle injection");
            };
            match *event {
                ServerNotification::TurnStarted(_)
                | ServerNotification::TurnCompleted(_)
                | ServerNotification::AgentMessageDelta(_)
                | ServerNotification::Error(_) => {
                    bail!("idle injection triggered assistant activity or a native error")
                }
                ServerNotification::ItemStarted(item)
                    if matches!(item.item.payload, ThreadItemPayload::AgentMessage { .. }) =>
                {
                    bail!("idle injection triggered an assistant message")
                }
                ServerNotification::ItemCompleted(item)
                    if matches!(item.item.payload, ThreadItemPayload::AgentMessage { .. }) =>
                {
                    bail!("idle injection produced an assistant message")
                }
                _ => {}
            }
        }
        Ok(())
    }
}

fn input(prompt: &str, kind: Option<&str>) -> Result<Value> {
    let mut items = vec![json!({"type":"text","text":prompt})];
    if let Some(kind) = kind {
        ensure!(
            matches!(kind, "image" | "mixed"),
            "unsupported structured input"
        );
        // This 64x64 PNG is the deterministic blue stimulus used by stimuli.py.
        let image = include_str!("interactions/blue-image.txt").trim();
        items.push(json!({"type":"image","url":format!("data:image/png;base64,{image}")}));
        if kind == "mixed" {
            items.push(json!({"type":"text","text":"This final text block belongs to the same mixed input."}));
        }
    }
    Ok(json!(items))
}

#[derive(Default)]
struct Turn {
    started: bool,
    complete: bool,
    output: String,
}

impl Turn {
    fn completed(&mut self, status: &TurnStatus, error: bool, interrupted: bool) -> Result<()> {
        let expected = if interrupted {
            TurnStatus::Interrupted
        } else {
            TurnStatus::Completed
        };
        ensure!(
            self.started && !self.complete && !error && *status == expected,
            "unexpected terminal status {status:?}, expected {expected:?}"
        );
        self.complete = true;
        Ok(())
    }
}

pub(crate) async fn exercise(
    client: &mut RemoteAppServerClient,
    plan: &Plan,
    workspace: &Path,
    execution_workspace: &Path,
    thread: &str,
    report: &mut Value,
) -> Result<()> {
    let mut flow = Interaction {
        client,
        thread,
        report,
        next_id: 2,
    };
    let parameters = &plan.scenario["parameters"];
    let media = match plan.case_id.as_str() {
        "SDK-CASE-002" => Some("image"),
        "SDK-CASE-004" => Some("mixed"),
        _ => None,
    };
    ensure!(
        parameters["inputKind"].as_str() == media,
        "invalid structured input binding"
    );
    let supplemental = plan.driver_profile == "active-input";
    let interrupt = plan.driver_profile == "interrupt";
    let injection = parameters["injectionTiming"].as_str();
    let fact = parameters["contextValue"].as_str().unwrap_or_default();
    if plan.driver_profile == "context-injection" {
        let expected_timing = if plan.case_id == "SDK-CASE-037" {
            "idle"
        } else {
            "during-execution"
        };
        ensure!(
            injection == Some(expected_timing)
                && fact.len() == 32
                && fact.bytes().all(|b| b.is_ascii_hexdigit())
                && plan.prompts.iter().all(|prompt| !prompt.contains(fact)),
            "invalid injection binding"
        );
    }
    let mut settings = settings::Settings::new(plan, flow.report)?;
    let mut approval = approvals::Approvals::new(plan, workspace, execution_workspace)?;
    let mut seen_turns = HashSet::new();
    let mut outputs = Vec::new();
    settings.apply_at("before-first-input", &mut flow).await?;
    for (index, prompt) in
        plan.prompts
            .iter()
            .enumerate()
            .take(if supplemental { 1 } else { plan.prompts.len() })
    {
        if index == 1 {
            settings.apply_at("between-inputs", &mut flow).await?;
            if injection == Some("idle") {
                flow.inject(fact, None).await?;
                flow.idle_barrier().await?;
            }
        }
        let accepted = flow.submit(input(prompt, media)?).await?;
        let root_turn = accepted.turn.id.clone();
        ensure!(
            !root_turn.is_empty()
                && seen_turns.insert(root_turn.clone())
                && accepted.turn.status == TurnStatus::InProgress
                && accepted.turn.error.is_none(),
            "invalid accepted turn"
        );
        flow.record("input-submitted", serde_json::to_value(accepted)?);
        let mut root = Turn::default();
        let mut extra: Option<(String, Turn)> = None;
        let mut supplemental_turn = None;
        let mut supplemental_observed = false;
        let mut acted = false;
        let needs_progress = supplemental
            || interrupt
            || settings.during_turn()
            || (index == 0 && injection == Some("during-execution"));
        while !root.complete || extra.as_ref().is_some_and(|(_, turn)| !turn.complete) {
            match flow.client.next_event().await {
                Some(AppServerEvent::ServerRequest(request)) => {
                    let answer = if root.started && !root.complete {
                        approval.answer(&request, thread, &root_turn)
                    } else {
                        Err(anyhow::anyhow!("native request outside the active turn"))
                    };
                    let receipt = answer
                        .as_ref()
                        .ok()
                        .map(|response| json!({"request": &request, "response": response}));
                    flow.server_request(&request, answer).await?;
                    if let Some(receipt) = receipt {
                        flow.record("server-request", receipt);
                    }
                }
                Some(AppServerEvent::ServerNotification(event)) => {
                    if let Some(receipt) = approval.observe(&event, thread, &root_turn)? {
                        record_receipt(flow.report, receipt);
                    }
                    match *event {
                        ServerNotification::TurnStarted(started) if started.thread_id == thread => {
                            let turn = if started.turn.id == root_turn {
                                &mut root
                            } else {
                                let (id, turn) =
                                    extra.as_mut().context("unexpected root turn start")?;
                                ensure!(
                                    *id == started.turn.id,
                                    "unexpected supplemental turn start"
                                );
                                turn
                            };
                            ensure!(
                                !turn.started
                                    && started.turn.status == TurnStatus::InProgress
                                    && started.turn.error.is_none(),
                                "invalid or repeated native turn start"
                            );
                            turn.started = true;
                            flow.record("turn/started", serde_json::to_value(started)?);
                        }
                        ServerNotification::TurnCompleted(completed)
                            if completed.thread_id == thread =>
                        {
                            let turn = if completed.turn.id == root_turn {
                                ensure!(
                                    !needs_progress || acted,
                                    "turn ended before the requested progress action"
                                );
                                &mut root
                            } else {
                                let (id, turn) =
                                    extra.as_mut().context("unexpected root turn terminal")?;
                                ensure!(
                                    *id == completed.turn.id,
                                    "unexpected supplemental turn terminal"
                                );
                                turn
                            };
                            turn.completed(
                                &completed.turn.status,
                                completed.turn.error.is_some(),
                                interrupt,
                            )?;
                            flow.report["results"] =
                                json!(flow.report["results"].as_u64().unwrap_or(0) + 1);
                            flow.record("turn/completed", serde_json::to_value(completed)?);
                        }
                        ServerNotification::ItemStarted(item) if item.thread_id == thread => {
                            let is_root = item.turn_id == root_turn;
                            ensure!(
                                if is_root {
                                    root.started && !root.complete
                                } else {
                                    extra.as_ref().is_some_and(|(id, turn)| {
                                        *id == item.turn_id && turn.started && !turn.complete
                                    })
                                },
                                "item started outside the matching active turn"
                            );
                            if is_root
                                && needs_progress
                                && !acted
                                && !matches!(
                                    item.item.payload,
                                    ThreadItemPayload::UserMessage { .. }
                                )
                            {
                                flow.record("action-progress", serde_json::to_value(&item)?);
                                if supplemental {
                                    match flow.submit(input(&plan.prompts[1], None)?).await {
                                        Ok(result) => {
                                            let id = &result.turn.id;
                                            ensure!(
                                                !id.is_empty()
                                                    && result.turn.status == TurnStatus::InProgress
                                                    && result.turn.error.is_none(),
                                                "invalid supplemental acceptance"
                                            );
                                            if *id != root_turn {
                                                ensure!(
                                                    seen_turns.insert(id.clone()),
                                                    "repeated supplemental turn ID"
                                                );
                                                extra = Some((id.clone(), Turn::default()));
                                            }
                                            supplemental_turn = Some(id.clone());
                                            flow.record(
                                                "supplemental-accepted",
                                                serde_json::to_value(result)?,
                                            );
                                        }
                                        Err(TypedRequestError::Server { source, .. })
                                            if matches!(source.code, -32600 | -32602) =>
                                        {
                                            flow.record(
                                                "supplemental-rejected",
                                                serde_json::to_value(source)?,
                                            );
                                        }
                                        Err(error) => return Err(error.into()),
                                    }
                                } else if interrupt {
                                    let ack: Value = flow
                                        .request(
                                            "turn/interrupt",
                                            json!({"threadId":thread,"turnId":root_turn}),
                                        )
                                        .await?;
                                    ensure!(
                                        ack == json!({}),
                                        "interrupt did not return the native acknowledgement"
                                    );
                                    flow.record("turn/interrupt", ack);
                                } else if injection == Some("during-execution") {
                                    flow.inject(fact, Some(&root_turn)).await?;
                                } else {
                                    settings.apply_at("during-execution", &mut flow).await?;
                                }
                                acted = true;
                            }
                        }
                        ServerNotification::ItemCompleted(item) if item.thread_id == thread => {
                            if let ThreadItemPayload::AgentMessage { text, .. } = &item.item.payload
                            {
                                let turn = if item.turn_id == root_turn {
                                    &mut root
                                } else {
                                    let (id, turn) = extra
                                        .as_mut()
                                        .context("assistant message outside an explicit turn")?;
                                    ensure!(
                                        *id == item.turn_id,
                                        "assistant message outside the accepted supplemental turn"
                                    );
                                    turn
                                };
                                ensure!(
                                    turn.started && !turn.complete,
                                    "assistant message outside an active turn"
                                );
                                turn.output.push_str(text);
                            } else if supplemental
                                && let ThreadItemPayload::UserMessage { content, .. } =
                                    &item.item.payload
                                && matches!(content.as_slice(), [UserInput::Text { text, .. }] if text == &plan.prompts[1])
                            {
                                ensure!(
                                    supplemental_turn.as_ref() == Some(&item.turn_id)
                                        && !supplemental_observed,
                                    "supplemental input was observed on an unaccepted or repeated turn"
                                );
                                supplemental_observed = true;
                                flow.record("supplemental-observed", serde_json::to_value(item)?);
                            }
                        }
                        ServerNotification::AgentMessageDelta(delta)
                            if delta.thread_id == thread =>
                        {
                            ensure!(
                                (delta.turn_id == root_turn && root.started && !root.complete)
                                    || extra
                                        .as_ref()
                                        .is_some_and(|(id, turn)| *id == delta.turn_id
                                            && turn.started
                                            && !turn.complete),
                                "assistant delta outside the matching active turn"
                            );
                        }
                        ServerNotification::ThreadSettingsUpdated(observed)
                            if observed.thread_id == thread =>
                        {
                            settings.observe(observed, &mut flow)?
                        }
                        ServerNotification::TurnStarted(_)
                        | ServerNotification::TurnCompleted(_)
                            if plan.case_id != "SDK-CASE-040" =>
                        {
                            bail!("unexpected turn on an unrelated thread");
                        }
                        ServerNotification::Error(event) if !event.will_retry => {
                            bail!("native execution error: {:?}", event.error)
                        }
                        _ => {}
                    }
                }
                Some(AppServerEvent::Disconnected { message }) => bail!("{message}"),
                Some(event) => bail!("unexpected SDK event: {event:?}"),
                None => bail!("app-server closed before the turn terminal"),
            }
        }
        ensure!(
            supplemental_turn.is_none() || supplemental_observed,
            "accepted supplemental input has no native item on its turn"
        );
        outputs.push(root.output);
    }
    approval.finish()?;
    settings.finish()?;
    if media.is_some() {
        ensure!(
            outputs[0]
                .trim()
                .trim_end_matches('.')
                .eq_ignore_ascii_case("blue"),
            "assistant result does not corroborate the supplied image"
        );
        flow.record(
            "media-content-observed",
            json!({"kind":media,"answer":outputs[0]}),
        );
    }
    if let Some(timing) = injection {
        ensure!(
            outputs.len() == 2 && outputs[1].trim() == fact,
            "subsequent query did not return the exact injected fact"
        );
        flow.report["contextInjection"] = json!({
            "timing":timing,"explicitQueries":2,"extraTurnObserved":false,
            "currentQueryUsedFact":outputs[0].contains(fact),"subsequentQueryUsedFact":true,
            "outputs":outputs,
        });
    }
    Ok(())
}
