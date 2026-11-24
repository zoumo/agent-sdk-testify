use std::collections::HashSet;
use std::path::Path;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    AdditionalFileSystemPermissions, FileSystemAccessMode, FileSystemPath, FileSystemSandboxEntry,
    GrantedPermissionProfile, JSONRPCErrorError, PermissionGrantScope,
    PermissionsRequestApprovalParams, PermissionsRequestApprovalResponse, RequestId,
    ServerNotification, ServerRequest, TurnStatus,
};
use codex_app_server_remote_client::{AppServerEvent, RemoteAppServerClient};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use crate::{Plan, record_receipt, start_turn};

pub(crate) const CASE_ID: &str = "SDK-CASE-041";

#[derive(Clone, Copy, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
enum Decision {
    Turn,
    Session,
    Deny,
}

impl Decision {
    fn directory(self) -> &'static str {
        match self {
            Self::Turn => "grant-turn",
            Self::Session => "grant-session",
            Self::Deny => "grant-deny",
        }
    }
}

#[derive(Deserialize)]
pub(crate) struct Scenario {
    #[serde(default)]
    parameters: Parameters,
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Parameters {
    #[serde(default)]
    approval_scopes: Vec<Decision>,
}

pub(crate) fn validate(plan: &Plan) -> Result<()> {
    let scenario = plan.scenario.as_ref().context("missing 041 scenario")?;
    ensure!(
        scenario.parameters.approval_scopes == [Decision::Turn, Decision::Session, Decision::Deny],
        "041 requires the turn, session, and deny decisions in order"
    );
    Ok(())
}

fn response(
    params: &PermissionsRequestApprovalParams,
    workspace: &Path,
    decision: Decision,
) -> Result<PermissionsRequestApprovalResponse> {
    ensure!(
        Some(params.cwd.as_str()) == workspace.to_str(),
        "041 permission request cwd differs from the protocol workspace"
    );
    ensure!(
        params.permissions.network.is_none(),
        "041 permission request includes network access"
    );
    let filesystem = params
        .permissions
        .file_system
        .as_ref()
        .context("041 permission request lacks filesystem permissions")?;
    ensure!(
        filesystem.glob_scan_max_depth.is_none()
            && filesystem.read.as_ref().is_none_or(Vec::is_empty),
        "041 permission request includes read access or glob scan depth"
    );

    let target = workspace.join(decision.directory());
    let target = target.to_str().context("041 grant path is not UTF-8")?;
    let legacy_path = match filesystem.write.as_deref() {
        None => None,
        Some([path]) if path.as_str() == target => Some(path),
        _ => bail!(
            "041 requested write path differs from {}",
            decision.directory()
        ),
    };
    let entry_path = match filesystem.entries.as_deref() {
        None => None,
        Some(
            [
                FileSystemSandboxEntry {
                    path: FileSystemPath::Path { path },
                    access: FileSystemAccessMode::Write,
                },
            ],
        ) if path.as_str() == target => Some(path),
        _ => bail!(
            "041 requested filesystem entry differs from {}",
            decision.directory()
        ),
    };
    let path = legacy_path
        .or(entry_path)
        .context("041 permission request lacks a write path")?;

    // Use the TUI's typed grant shape. Both path representations describe the same directory.
    let mut permissions = GrantedPermissionProfile::default();
    if decision != Decision::Deny {
        permissions.file_system = Some(AdditionalFileSystemPermissions {
            read: None,
            write: Some(vec![path.clone()]),
            entries: Some(vec![FileSystemSandboxEntry {
                path: FileSystemPath::Path { path: path.clone() },
                access: FileSystemAccessMode::Write,
            }]),
            glob_scan_max_depth: None,
        });
    }
    Ok(PermissionsRequestApprovalResponse {
        permissions,
        scope: match decision {
            Decision::Session => PermissionGrantScope::Session,
            Decision::Turn | Decision::Deny => PermissionGrantScope::Turn,
        },
        strict_auto_review: None,
    })
}

#[derive(PartialEq)]
enum TurnPhase {
    Submitted,
    Started,
    Answered(RequestId),
    Resolved,
}

pub(crate) async fn exercise(
    client: &mut RemoteAppServerClient,
    plan: &Plan,
    workspace: &Path,
    thread_id: &str,
    report: &mut Value,
) -> Result<()> {
    let scenario = plan.scenario.as_ref().context("missing 041 scenario")?;
    let mut turn_ids = HashSet::new();
    let mut request_ids = HashSet::new();
    let mut item_ids = HashSet::new();
    for (index, (prompt, decision)) in plan
        .prompts
        .iter()
        .zip(scenario.parameters.approval_scopes.iter().copied())
        .enumerate()
    {
        let accepted = start_turn(client, index as i64 + 2, thread_id, prompt).await?;
        let turn_id = &accepted.turn.id;
        ensure!(
            !turn_id.is_empty() && turn_ids.insert(turn_id.clone()),
            "missing or repeated 041 turn ID"
        );
        report["inputs"] = json!(index + 1);
        record_receipt(report, json!({"input-submitted": accepted}));
        let mut phase = TurnPhase::Submitted;
        loop {
            match client.next_event().await {
                Some(AppServerEvent::ServerRequest(request)) => {
                    let id = request.id().clone();
                    let answer = (|| {
                        let ServerRequest::PermissionsRequestApproval { params, .. } = &*request
                        else {
                            bail!("unexpected server request in 041");
                        };
                        ensure!(
                            phase == TurnPhase::Started
                                && params.thread_id == thread_id
                                && params.turn_id == *turn_id
                                && !params.item_id.is_empty()
                                && !matches!(&id, RequestId::String(value) if value.is_empty())
                                && request_ids.insert(id.clone())
                                && item_ids.insert(params.item_id.clone()),
                            "unexpected, uncorrelated, or repeated 041 permission request"
                        );
                        Ok((
                            params,
                            serde_json::to_value(response(params, workspace, decision)?)?,
                        ))
                    })();
                    let (params, answer) = match answer {
                        Ok(answer) => answer,
                        Err(error) => {
                            client
                                .reject_server_request(
                                    id,
                                    JSONRPCErrorError {
                                        code: -32602,
                                        message: error.to_string(),
                                        data: None,
                                    },
                                )
                                .await
                                .context("reject invalid 041 request")?;
                            return Err(error);
                        }
                    };
                    client
                        .resolve_server_request(id.clone(), answer.clone())
                        .await
                        .context("answer 041 permission request")?;
                    record_receipt(
                        report,
                        json!({"sandbox-approval": {
                            "decision": decision,
                            "threadId": params.thread_id,
                            "turnId": params.turn_id,
                            "itemId": params.item_id,
                            "requestId": id,
                            "cwd": params.cwd,
                            "requestedPermissions": params.permissions,
                            "response": answer,
                        }}),
                    );
                    phase = TurnPhase::Answered(id);
                }
                Some(AppServerEvent::ServerNotification(event)) => match *event {
                    ServerNotification::TurnStarted(started) => {
                        ensure!(
                            phase == TurnPhase::Submitted
                                && started.thread_id == thread_id
                                && started.turn.id == *turn_id
                                && started.turn.status == TurnStatus::InProgress
                                && started.turn.error.is_none(),
                            "unexpected 041 turn start"
                        );
                        phase = TurnPhase::Started;
                        record_receipt(report, json!({"turn/started": started}));
                    }
                    ServerNotification::ServerRequestResolved(resolved) => {
                        ensure!(
                            resolved.thread_id == thread_id
                                && matches!(&phase, TurnPhase::Answered(id) if id == &resolved.request_id),
                            "unexpected or uncorrelated 041 permission resolution"
                        );
                        phase = TurnPhase::Resolved;
                        record_receipt(report, json!({"serverRequest/resolved": resolved}));
                    }
                    ServerNotification::TurnCompleted(completed) => {
                        ensure!(
                            phase == TurnPhase::Resolved
                                && completed.thread_id == thread_id
                                && completed.turn.id == *turn_id
                                && completed.turn.status == TurnStatus::Completed
                                && completed.turn.error.is_none(),
                            "unexpected or unsuccessful 041 turn completion"
                        );
                        record_receipt(report, json!({"turn/completed": completed}));
                        report["results"] = json!(index + 1);
                        break;
                    }
                    ServerNotification::Error(event) => {
                        bail!("041 native error: {:?}", event.error)
                    }
                    _ => {}
                },
                Some(AppServerEvent::Disconnected { message }) => bail!("{message}"),
                Some(event) => bail!("unexpected 041 app-server event: {event:?}"),
                None => bail!("app-server closed before 041 turn completion"),
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_conflicting_permission_path_representations() {
        let params = serde_json::from_value(json!({
            "threadId": "thread", "turnId": "turn", "itemId": "item",
            "startedAtMs": 1, "cwd": "/fixture",
            "permissions": {
                "fileSystem": {
                    "write": ["/fixture"],
                    "entries": [{"path": {"type": "path", "path": "/fixture/grant-turn"}, "access": "write"}]
                }
            }
        }))
        .unwrap();
        let error = response(&params, Path::new("/fixture"), Decision::Turn).unwrap_err();
        assert_eq!(
            error.to_string(),
            "041 requested write path differs from grant-turn"
        );
    }
}
