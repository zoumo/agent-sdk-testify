use std::collections::HashSet;

use anyhow::{Result, bail, ensure};
use codex_app_server_protocol::{
    McpServerElicitationAction::{Accept, Cancel, Decline},
    McpServerElicitationRequest, McpServerElicitationRequestResponse, McpToolCallStatus, RequestId,
    ServerNotification, ServerRequest, ThreadItemPayload, TurnStatus,
};
use codex_app_server_remote_client::AppServerEvent;
use serde_json::{Value, json};

use super::{Interaction, Turn, input};
use crate::Plan;

const SERVER: &str = "elicitation_fixture";
const TOOL: &str = "roundtrip";
const TOKEN: &str = "SYNTHETIC_ELICITATION_SEQUENCE_DONE";

fn answer(request: &McpServerElicitationRequest, index: usize) -> Result<Value> {
    let (action, label) = match index % 3 {
        0 => (Accept, "accept"),
        1 => (Decline, "decline"),
        _ => (Cancel, "cancel"),
    };
    let content = match request {
        McpServerElicitationRequest::Form {
            meta: None,
            message,
            requested_schema,
        } if index < 3 => {
            ensure!(
                message == &format!("SYNTHETIC form {label}")
                    && serde_json::to_value(requested_schema)?
                        == json!({"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}),
                "unexpected standalone elicitation form or schema"
            );
            (action == Accept).then(|| json!({"value":"SYNTHETIC_FORM"}))
        }
        McpServerElicitationRequest::Url {
            meta: None,
            message,
            url,
            elicitation_id,
        } if (3..6).contains(&index) => {
            ensure!(
                message == &format!("SYNTHETIC url {label}")
                    && url == "https://example.invalid/synthetic"
                    && elicitation_id == &format!("fixture-{index}"),
                "unexpected standalone elicitation URL or identity"
            );
            None
        }
        _ => bail!("unexpected standalone elicitation mode or sequence"),
    };
    Ok(serde_json::to_value(McpServerElicitationRequestResponse {
        action,
        content,
        meta: None,
    })?)
}

pub(super) async fn exercise(flow: &mut Interaction<'_>, plan: &Plan) -> Result<()> {
    ensure!(
        plan.scenario["requiredRequests"] == json!(["standalone-elicitation"]),
        "invalid standalone elicitation binding"
    );
    let started = flow.submit(input(&plan.prompts[0], None)?).await?;
    let turn_id = started.turn.id.clone();
    ensure!(
        !turn_id.is_empty()
            && started.turn.status == TurnStatus::InProgress
            && started.turn.error.is_none(),
        "invalid standalone elicitation turn"
    );
    flow.record("input-submitted", json!(started));

    let mut turn = Turn::default();
    let mut tool_id = None;
    let mut tool_complete = false;
    let mut answered = HashSet::new();
    let mut pending = HashSet::new();
    while !turn.complete {
        match flow.client.next_event().await {
            Some(AppServerEvent::ServerRequest(request)) => {
                let response = (|| {
                    let ServerRequest::McpServerElicitationRequest { params, .. } =
                        request.as_ref()
                    else {
                        bail!("unexpected request in standalone elicitation");
                    };
                    ensure!(
                        turn.started
                            && tool_id.is_some()
                            && !tool_complete
                            && params.thread_id == flow.thread
                            && params.turn_id.as_deref() == Some(turn_id.as_str())
                            && params.server_name == SERVER
                            && !matches!(request.id(), RequestId::String(id) if id.is_empty())
                            && !answered.contains(request.id()),
                        "elicitation does not belong to the active fixture tool call or repeats an ID"
                    );
                    answer(&params.request, answered.len())
                })();
                let receipt = response
                    .as_ref()
                    .ok()
                    .map(|value| json!({"request":&request,"response":value}));
                flow.server_request(&request, response).await?;
                answered.insert(request.id().clone());
                pending.insert(request.id().clone());
                if let Some(receipt) = receipt {
                    flow.record("elicitation", receipt);
                }
            }
            Some(AppServerEvent::ServerNotification(event)) => match *event {
                ServerNotification::TurnStarted(start) => {
                    ensure!(
                        !turn.started
                            && start.thread_id == flow.thread
                            && start.turn.id == turn_id
                            && start.turn.status == TurnStatus::InProgress
                            && start.turn.error.is_none(),
                        "unexpected native turn start"
                    );
                    turn.started = true;
                    flow.record("turn/started", json!(start));
                }
                ServerNotification::ItemStarted(item) => {
                    if let ThreadItemPayload::McpToolCall {
                        id,
                        server,
                        tool,
                        status,
                        arguments,
                        ..
                    } = &item.item.payload
                    {
                        ensure!(
                            turn.started
                                && item.thread_id == flow.thread
                                && item.turn_id == turn_id
                                && tool_id.is_none()
                                && !id.is_empty()
                                && server == SERVER
                                && tool == TOOL
                                && *status == McpToolCallStatus::InProgress
                                && arguments == &json!({}),
                            "unexpected MCP tool call in standalone elicitation"
                        );
                        tool_id = Some(id.clone());
                        flow.record("tool-item/started", json!(item));
                    }
                }
                ServerNotification::ServerRequestResolved(resolved) => {
                    ensure!(
                        resolved.thread_id == flow.thread && pending.remove(&resolved.request_id),
                        "unexpected or repeated standalone elicitation resolution"
                    );
                    flow.record("serverRequest/resolved", json!(resolved));
                }
                ServerNotification::ItemCompleted(item) => {
                    if let ThreadItemPayload::McpToolCall {
                        id,
                        server,
                        tool,
                        status,
                        arguments,
                        result,
                        error,
                        ..
                    } = &item.item.payload
                    {
                        ensure!(
                            item.thread_id == flow.thread
                                && item.turn_id == turn_id
                                && tool_id.as_ref() == Some(id)
                                && !tool_complete
                                && server == SERVER
                                && tool == TOOL
                                && arguments == &json!({})
                                && answered.len() == 6
                                && error.is_none()
                                && *status == McpToolCallStatus::Completed
                                && result.as_ref().is_some_and(|result| result.content
                                    == [json!({"type":"text","text":TOKEN})]),
                            "MCP fixture did not confirm all six elicitation answers"
                        );
                        tool_complete = true;
                        flow.record("tool-item/completed", json!(item));
                    }
                }
                ServerNotification::TurnCompleted(done) => {
                    ensure!(
                        done.thread_id == flow.thread
                            && done.turn.id == turn_id
                            && tool_complete
                            && pending.is_empty(),
                        "turn ended before the standalone elicitation evidence was complete"
                    );
                    turn.completed(&done.turn.status, done.turn.error.is_some(), false)?;
                    flow.record("turn/completed", json!(done));
                }
                ServerNotification::Error(event) if !event.will_retry => {
                    bail!("native execution error: {:?}", event.error);
                }
                _ => {}
            },
            Some(AppServerEvent::Disconnected { message }) => bail!("{message}"),
            Some(event) => bail!("unexpected SDK event: {event:?}"),
            None => bail!("app-server closed before the standalone elicitation terminal"),
        }
    }
    flow.report["requests"] = json!(answered.len());
    flow.report["results"] = json!(1);
    Ok(())
}
