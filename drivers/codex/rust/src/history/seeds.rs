use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Component, Path};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

use super::{Parameters, Query, Start, children, native_id, same_execution};
use crate::{PeerNotice, Plan, Runtime, read_json, record_receipt, run_connection};

#[derive(Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub(super) struct Setup {
    schema_version: u32,
    provider: String,
    #[serde(flatten)]
    seed: Seed,
    history_bindings: Vec<Binding>,
    #[serde(rename = "rawSHA256")]
    raw_sha256: String,
    #[serde(rename = "transportSHA256")]
    transport_sha256: String,
    completed_at: String,
}

#[derive(Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub(super) struct Seed {
    pub(super) session_id: String,
    pub(super) agent_ids: Vec<String>,
}

#[derive(Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(super) struct Binding {
    role: String,
    session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    parent_session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    child_session_id: String,
    project: String,
    native_target: String,
    source: String,
    sha256: String,
}

fn read_bound(root: &Path, name: &str, expected_hash: &str) -> Result<Vec<u8>> {
    let name = Path::new(name);
    ensure!(
        name.components().all(|c| matches!(c, Component::Normal(_))),
        "unsafe bound file name"
    );
    let mut path = root.to_path_buf();
    for component in name.components() {
        path.push(component);
        ensure!(
            !fs::symlink_metadata(&path)?.is_symlink(),
            "bound file path contains a symlink"
        );
    }
    ensure!(
        fs::metadata(&path)?.is_file(),
        "bound path is not a regular file"
    );
    let bytes = fs::read(path)?;
    ensure!(
        format!("{:x}", Sha256::digest(&bytes)) == expected_hash,
        "bound file SHA-256 changed: {}",
        name.display()
    );
    Ok(bytes)
}

// Go validates the whole material. This check independently ties the actual
// Rust-observed identities to the two canonical, immutable native rollout files.
pub(super) fn check_bindings(
    material: &Path,
    bindings: &[Binding],
    role: &str,
    expected_parents: &BTreeMap<&str, Option<&str>>,
    project: &Path,
) -> Result<()> {
    let mut seen = BTreeSet::new();
    for binding in bindings {
        let parent = expected_parents
            .get(binding.session_id.as_str())
            .context("unrelated native history binding")?;
        let child_id = parent.map(|_| binding.session_id.as_str()).unwrap_or("");
        ensure!(
            binding.role == role
                && binding.parent_session_id == parent.unwrap_or("")
                && binding.child_session_id == child_id
                && Path::new(&binding.project) == project
                && binding.native_target.starts_with("config/sessions/")
                && binding
                    .native_target
                    .ends_with(&format!("-{}.jsonl", binding.session_id))
                && binding.source == format!("history/{role}/{}", binding.native_target)
                && seen.insert(binding.session_id.as_str()),
            "unexpected or repeated bound native rollout identity"
        );
        let bytes = read_bound(material, &binding.source, &binding.sha256)?;
        let first = bytes
            .split(|byte| *byte == b'\n')
            .next()
            .context("empty bound rollout")?;
        let row: Value = serde_json::from_slice(first)?;
        let meta = &row["payload"];
        let parents = [
            &meta["parentThreadId"],
            &meta["parent_thread_id"],
            &meta["source"]["subagent"]["thread_spawn"]["parent_thread_id"],
        ];
        ensure!(
            row["type"] == "session_meta"
                && meta["id"] == binding.session_id
                && meta["cwd"] == binding.project
                && parents
                    .iter()
                    .filter_map(|value| value.as_str())
                    .all(|value| Some(value) == *parent)
                && (parent.is_none() || parents.iter().any(|value| value.as_str() == *parent)),
            "bound rollout metadata disagrees with its native identity"
        );
    }
    ensure!(
        seen.len() == expected_parents.len(),
        "native history binding is absent"
    );
    Ok(())
}

pub(super) fn check_child_history(
    runtime: &Runtime,
    plan: &Plan,
    main: &PeerNotice,
    root: &str,
    child: &str,
    workspace: &Path,
) -> Result<()> {
    let parents = BTreeMap::from([(root, None), (child, Some(root))]);
    if runtime.replay {
        let material = runtime.raw.parent().context("missing material root")?;
        return check_bindings(
            material,
            &plan.history.history_bindings,
            "runtime",
            &parents,
            workspace,
        );
    }
    let material = published_material(runtime, main);
    let captured: Plan = read_json(&material.join("plan.json"))?;
    check_bindings(
        &material,
        &captured.history.history_bindings,
        "runtime",
        &parents,
        workspace,
    )
}

fn published_material(runtime: &Runtime, main: &PeerNotice) -> std::path::PathBuf {
    runtime
        .fixture_dir
        .join("packages")
        .join(&main.material_id)
        .join("material")
}

pub(super) async fn self_seeded(
    runtime: &Runtime,
    plan: &Plan,
    workspace: &Path,
    parameters: &Parameters,
    report: &mut Value,
) -> Result<()> {
    let prompt = parameters
        .setup_prompt
        .as_deref()
        .context("missing 035 setup prompt")?;
    let ((root, child), main, _) =
        run_connection(runtime, "main", report, async |client, report| {
            let mut query = Query::new(client, report);
            let root = query.start(plan, workspace, Start::Saved).await?.id;
            let turn = query.submit(&root, prompt).await?;
            let mut observed = children::Observed::default();
            query
                .until_completion(&root, &turn, &mut observed, None, false)
                .await?;
            let child = observed.single_child(&root)?.to_owned();
            observed.check_setup(&root, &turn, &child)?;
            query.detach(&root).await?;
            Ok((root, child))
        })
        .await?;

    report["setupInputs"] = report["inputs"].clone();
    report["setupResults"] = report["results"].clone();
    report["inputs"] = json!(0);
    report["results"] = json!(0);

    let root_dir = if runtime.replay {
        let material = runtime
            .raw
            .parent()
            .context("missing 035 replay material root")?;
        ensure!(
            runtime.seed_root.as_deref() == Some(material),
            "035 replay seed is not its own material"
        );
        material.to_path_buf()
    } else {
        // The public record launcher seals setup under the execution named in
        // this connection's settled receipt, before the probe can claim it.
        runtime
            .fixture_dir
            .join("packages")
            .join(&main.material_id)
            .join("executions")
            .join(&main.execution_id)
            .join("capture")
    };
    let setup_path = root_dir.join("existing-children/setup.json");
    let setup: Setup = read_json(&setup_path)?;
    ensure!(
        setup.schema_version == 1
            && setup.provider == "codex"
            && native_id(&setup.seed.session_id)
            && setup.seed.session_id == root
            && setup.seed.agent_ids.as_slice() == [child.as_str()]
            && !setup.completed_at.is_empty(),
        "035 native setup identities differ from saved setup"
    );
    if runtime.replay {
        ensure!(
            plan.history.existing_children_setup.as_ref() == Some(&setup),
            "035 setup differs from the sealed plan projection"
        );
    }
    read_bound(&root_dir, "raw/raw.jsonl", &setup.raw_sha256)?;
    let transport: Value = serde_json::from_slice(&read_bound(
        &root_dir,
        "raw/transport.json",
        &setup.transport_sha256,
    )?)?;
    ensure!(
        transport["captureStatus"] == "complete"
            && transport["nativeExitCode"] == 0
            && ["nativeLaunched", "reaped", "clientEOF", "cleanupComplete"]
                .iter()
                .all(|key| transport[*key] == true)
            && transport["error"].is_null(),
        "035 setup capture did not settle successfully"
    );
    let parents = BTreeMap::from([(root.as_str(), None), (child.as_str(), Some(root.as_str()))]);
    check_bindings(
        &root_dir,
        &setup.history_bindings,
        "seed",
        &parents,
        workspace,
    )?;
    record_receipt(report, json!({"existing-setup": setup}));

    let ((), probe, _) = run_connection(runtime, "probe", report, async |client, report| {
        Query::new(client, report)
            .observe(&setup.seed, workspace, &plan.model)
            .await
    })
    .await?;
    same_execution(&main, &probe)?;
    let final_root = if runtime.replay {
        root_dir
    } else {
        published_material(runtime, &main)
    };
    let final_setup_path = final_root.join("existing-children/setup.json");
    let final_setup: Setup = read_json(&final_setup_path)?;
    ensure!(setup == final_setup, "035 setup changed after probe");
    let observation: Value = read_json(&final_root.join("existing-children/observation.json"))?;
    ensure!(
        observation["schemaVersion"] == 1
            && observation["sessionId"] == root
            && observation["startedAt"]
                .as_str()
                .is_some_and(|started| !started.is_empty())
            && observation["setupSHA256"]
                == format!("{:x}", Sha256::digest(fs::read(final_setup_path)?)),
        "035 observation boundary does not bind its exact completed setup"
    );
    record_receipt(
        report,
        json!({"existing-observation-boundary": observation}),
    );
    Ok(())
}
