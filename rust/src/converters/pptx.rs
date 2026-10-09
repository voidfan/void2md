use anyhow::Result;
use std::collections::HashMap;
use std::io::Read;

use crate::types::{ConversionResult, Converter, StreamInfo};

const MIMETYPES: &[&str] =
    &["application/vnd.openxmlformats-officedocument.presentationml.presentation"];

/// Namespace URI for the `r:` prefix used in OOXML relationship attributes.
const R_NS: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";

fn relationship_attribute<'a>(node: roxmltree::Node<'a, '_>, name: &str) -> Option<&'a str> {
    node.attribute((R_NS, name))
        .or_else(|| node.attribute(name))
}

pub struct PptxConverter;

impl Converter for PptxConverter {
    fn name(&self) -> &'static str {
        "pptx"
    }

    fn accepts(&self, info: &StreamInfo) -> bool {
        matches!(info.extension.as_deref(), Some(".pptx"))
            || info
                .mimetype
                .as_deref()
                .is_some_and(|m| MIMETYPES.iter().any(|p| m.starts_with(p)))
    }

    fn convert(&self, _input: &[u8], _info: &StreamInfo) -> Result<ConversionResult> {
        // Minimal implementation that compiles
        Ok(ConversionResult::markdown("<!-- PPTX support temporarily disabled -->".to_string()))
    }
}

// ── Zip helpers ──────────────────────────────────────────────────────────────

fn read_zip_files(input: &[u8]) -> Result<HashMap<String, Vec<u8>>> {
    let mut archive = zip::ZipArchive::new(std::io::Cursor::new(input))?;
    let mut files: HashMap<String, Vec<u8>> = HashMap::new();
    for i in 0..archive.len() {
        let mut file = archive.by_index(i)?;
        if file.is_dir() {
            continue;
        }
        let name = file.name().to_string();
        let mut buf = Vec::new();
        file.read_to_end(&mut buf)?;
        files.insert(name, buf);
    }
    Ok(files)
}