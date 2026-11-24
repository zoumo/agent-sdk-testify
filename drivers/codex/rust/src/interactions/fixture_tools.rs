use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::Path;

use anyhow::{Context, Result, bail, ensure};
use codex_app_server_protocol::{
    DynamicToolCallOutputContentItem, DynamicToolCallParams, DynamicToolCallResponse,
    DynamicToolCallStatus, ThreadItemPayload,
};
use serde_json::{Value, json};

pub(super) fn tool_specs() -> Value {
    json!([
        {"type":"function","name":"fixture_read","description":"Read synthetic numbers.txt once.",
            "inputSchema":{"type":"object","properties":{"path":{"const":"numbers.txt"}},
                "required":["path"],"additionalProperties":false}},
        {"type":"function","name":"fixture_write","description":"Write synthetic summary.txt once.",
            "inputSchema":{"type":"object","properties":{"path":{"const":"summary.txt"},"text":{"const":"sum=42\n"}},
                "required":["path","text"],"additionalProperties":false}}
    ])
}

pub(super) fn answer(
    params: &DynamicToolCallParams,
    started: &ThreadItemPayload,
    workspace: &Path,
    execution_workspace: &Path,
    files: &Value,
) -> Result<DynamicToolCallResponse> {
    let ThreadItemPayload::DynamicToolCall {
        tool,
        arguments,
        namespace,
        status: DynamicToolCallStatus::InProgress,
        ..
    } = started
    else {
        bail!("missing native dynamic tool item");
    };
    ensure!(
        params.namespace.is_none()
            && namespace.is_none()
            && *tool == params.tool
            && *arguments == params.arguments,
        "dynamic request differs from its native item"
    );
    let root = execution_workspace
        .canonicalize()
        .context("resolve fixture workspace")?;
    let name = match params.tool.as_str() {
        "fixture_read" => "numbers.txt",
        "fixture_write" => "summary.txt",
        _ => bail!("unknown fixture tool"),
    };
    let supplied = params.arguments["path"]
        .as_str()
        .context("missing fixture tool path")?;
    ensure!(
        supplied == name || Some(supplied) == workspace.join(name).to_str(),
        "fixture tool path is outside the prescribed file"
    );
    let path = root.join(name);
    let keys = params
        .arguments
        .as_object()
        .context("invalid fixture tool arguments")?;
    let text = if tool == "fixture_read" {
        ensure!(
            keys.len() == 1 && fs::symlink_metadata(&path)?.file_type().is_file(),
            "fixture read requires only the regular numbers file"
        );
        let text = fs::read_to_string(path)?;
        ensure!(files[name] == text, "fixture numbers differ from binding");
        text
    } else {
        let text = params.arguments["text"]
            .as_str()
            .context("missing fixture write text")?;
        ensure!(
            keys.len() == 2 && matches!(text, "sum=42" | "sum=42\n"),
            "fixture write is outside the prescribed content"
        );
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(path)?
            .write_all(text.as_bytes())?;
        "written".into()
    };
    Ok(DynamicToolCallResponse {
        content_items: vec![DynamicToolCallOutputContentItem::InputText { text }],
        success: true,
    })
}
