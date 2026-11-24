use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::ThreadSettingsUpdatedNotification;
use codex_app_server_remote_client::TypedRequestError;
use serde_json::{Value, json};

use crate::Plan;

use super::Interaction;

pub(super) fn configure_creation(plan: &Plan, params: &mut Value) -> Result<()> {
    if plan.case_id == "SDK-CASE-027" {
        let effort = plan.scenario["parameters"]["effort"]
            .as_str()
            .filter(|s| !s.is_empty())
            .context("missing creation effort")?;
        params["config"] = json!({"model_reasoning_effort":effort});
    }
    Ok(())
}

#[derive(Default)]
pub(super) struct Settings<'a> {
    timing: &'a str,
    targets: Vec<(&'static str, &'a str)>,
    applied: bool,
    observed: bool,
    rejected: bool,
}

impl<'a> Settings<'a> {
    pub(super) fn new(plan: &'a Plan, report: &Value) -> Result<Self> {
        let parameters = &plan.scenario["parameters"];
        let configuration = &plan.scenario["configuration"];
        let opened = &report["receipts"][0]["thread/start"];
        let mut settings = Self::default();
        match plan.case_id.as_str() {
            "SDK-CASE-021" => {
                ensure!(
                    parameters["setting"] == "model"
                        && parameters["configurationTiming"] == "creation"
                        && !plan.model.is_empty()
                        && opened["model"] == plan.model,
                    "created thread did not report the selected model"
                );
            }
            "SDK-CASE-022" | "SDK-CASE-023" | "SDK-CASE-024" => {
                let initial = plan
                    .initial_model
                    .as_deref()
                    .context("missing initial model")?;
                ensure!(
                    !initial.is_empty()
                        && !plan.model.is_empty()
                        && initial != plan.model
                        && opened["model"] == initial,
                    "model update lacks a distinct observed initial model"
                );
                settings.timing = match plan.case_id.as_str() {
                    "SDK-CASE-022" => "before-first-input",
                    "SDK-CASE-023" => "between-inputs",
                    _ => "during-execution",
                };
                settings.targets.push(("model", &plan.model));
                if plan.case_id == "SDK-CASE-022" {
                    ensure!(
                        configuration["timing"] == settings.timing
                            && configuration["modelSource"] == "session",
                        "unexpected pre-input configuration binding"
                    );
                    let effort = configuration["thinking"]["effort"]
                        .as_str()
                        .filter(|s| !s.is_empty())
                        .context("missing configured effort")?;
                    settings.targets.push(("effort", effort));
                } else {
                    ensure!(
                        parameters["setting"] == "model"
                            && parameters["configurationTiming"] == settings.timing,
                        "unexpected model update binding"
                    );
                }
            }
            "SDK-CASE-027" => {
                let initial = parameters["effort"]
                    .as_str()
                    .context("missing initial effort")?;
                let target = parameters["targetEffort"]
                    .as_str()
                    .context("missing runtime effort")?;
                ensure!(
                    !initial.is_empty()
                        && !target.is_empty()
                        && initial != target
                        && opened["reasoningEffort"] == initial
                        && parameters["setting"] == "effort"
                        && parameters["configurationTiming"] == "between-inputs",
                    "effort update lacks a distinct observed creation effort"
                );
                settings.timing = "between-inputs";
                settings.targets.push(("effort", target));
            }
            "SDK-CASE-028" => {
                ensure!(
                    parameters["setting"] == "display"
                        && parameters["configurationTiming"] == "before-first-input",
                    "unexpected reasoning summary binding"
                );
                settings.timing = "before-first-input";
                settings.targets.push(("summary", "none"));
            }
            _ => {}
        }
        Ok(settings)
    }

    pub(super) fn during_turn(&self) -> bool {
        self.timing == "during-execution"
    }

    pub(super) async fn apply_at(
        &mut self,
        timing: &str,
        flow: &mut Interaction<'_>,
    ) -> Result<()> {
        if timing != self.timing {
            return Ok(());
        }
        ensure!(!self.applied, "repeated settings update");
        for (key, value) in &self.targets {
            let mut requested = json!({"threadId": flow.thread});
            requested[*key] = json!(value);
            let outcome = flow
                .request::<Value>("thread/settings/update", requested.clone())
                .await;
            match outcome {
                Ok(response) => {
                    ensure!(
                        response == json!({}),
                        "unexpected thread/settings/update acknowledgement"
                    );
                    flow.record(
                        "settings-update",
                        json!({"timing":timing,"requested":requested,
                        "accepted":true,"response":response}),
                    );
                }
                Err(TypedRequestError::Server { source, .. })
                    if self.during_turn() && matches!(source.code, -32600 | -32601 | -32602) =>
                {
                    self.rejected = true;
                    flow.record(
                        "settings-update",
                        json!({"timing":timing,"requested":requested,
                        "accepted":false,"error":source}),
                    );
                }
                Err(error) => return Err(error.into()),
            }
        }
        self.applied = true;
        Ok(())
    }

    pub(super) fn observe(
        &mut self,
        notification: ThreadSettingsUpdatedNotification,
        flow: &mut Interaction<'_>,
    ) -> Result<()> {
        if !self.applied || self.rejected {
            return Ok(());
        }
        let raw = serde_json::to_value(notification.thread_settings)?;
        if self.targets.iter().all(|(key, value)| raw[*key] == *value) {
            self.observed = true;
        }
        flow.record(
            "settings-observed",
            json!({"threadId":notification.thread_id,"settings":raw}),
        );
        Ok(())
    }

    pub(super) fn finish(&self) -> Result<()> {
        if self.targets.is_empty() {
            return Ok(());
        }
        ensure!(self.applied, "settings update was never sent");
        if self.targets[0].0 != "summary" && !self.rejected && !self.observed {
            bail!("accepted settings update has no matching native observation");
        }
        Ok(())
    }
}
