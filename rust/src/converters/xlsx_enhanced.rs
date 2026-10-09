use anyhow::{anyhow, Result};
use std::collections::HashMap;
use std::fs;
use std::io::{Cursor, Read};

use crate::config::ParseConfig;
use crate::converters::ooxml::{
    office_document_path, parse_relationships, relationship_part_path, resolve_part_path,
};
use crate::types::{ConversionResult, Converter, StreamInfo};
use crate::utils::html_to_md::{html_to_markdown_generated, normalize_tables_html};

// Reuse existing constants
const S: &str = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
const R: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const IMG_TOKEN_PREFIX: &str = "MARKITIMGTOKEN";

pub struct XlsxConverter;

impl Converter for XlsxConverter {
    fn name(&self) -> &'static str {
        "xlsx"
    }

    fn accepts(&self, info: &StreamInfo) -> bool {
        if info.extension.as_deref() == Some(".xlsx") {
            return true;
        }
        info.mimetype.as_deref().is_some_and(|m| {
            m.starts_with("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
        })
    }

    fn convert(&self, input: &[u8], info: &StreamInfo) -> Result<ConversionResult> {
        // Parse configuration from StreamInfo.extra (JSON string)
        let config_json = info.extra.as_deref().unwrap_or("");
        let config = crate::config::ParseConfig::from_json_or_default(config_json);

        let cursor = Cursor::new(input);
        let mut archive =
            zip::ZipArchive::new(cursor).map_err(|e| anyhow!("Invalid XLSX: {}", e))?;

        let workbook_path = read_zip_str(&mut archive, "_rels/.rels")
            .ok()
            .and_then(|xml| office_document_path(&xml).ok())
            .unwrap_or_else(|| "xl/workbook.xml".to_string());

        // Relationship map: id → (rel_type, target)
        let rels = read_rels(&mut archive, &workbook_path).unwrap_or_default();

        let workbook_xml = read_zip_str(&mut archive, &workbook_path)
            .map_err(|_| anyhow!("Invalid XLSX: missing main workbook part {workbook_path}"))?;

        // Image blobs: rel_id → (bytes, extension)
        let images = load_images(&mut archive, &rels, &workbook_path);

        // Prepare image output dir
        let image_dir = info.image_dir.as_deref();
        if let Some(dir) = image_dir {
            fs::create_dir_all(dir)?;
        }

        // Convert to HTML with enhanced processing
        let mut image_count: usize = 0;
        let html = workbook_to_html_enhanced(
            &workbook_xml,
            &rels,
            &images,
            image_dir,
            &mut image_count,
            &config,
            &mut archive,
        )?;

        let normalized = normalize_tables_html(&html);
        let mut markdown = html_to_markdown_generated(&normalized);

        // Replace image tokens with placeholder comments (no-imageDir path).
        if image_dir.is_none() {
            for i in 1..=image_count {
                let token = format!("{}{}", IMG_TOKEN_PREFIX, i);
                let comment = format!("<!-- image: image_{} -->", i);
                let escaped = token.replace('_', "\\_");
                markdown = markdown
                    .replace(&token, &comment)
                    .replace(&escaped, &comment);
            }
        }

        // Apply post-processing based on config
        let markdown = post_process_xlsx(&markdown, &config);

        Ok(ConversionResult::markdown(markdown.trim()))
    }
}

// Enhanced XLSX to HTML conversion
fn workbook_to_html_enhanced(
    workbook_xml: &str,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    config: &ParseConfig,
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
) -> Result<String> {
    let doc = roxmltree::Document::parse(workbook_xml)?;
    let root = doc.root_element();

    let mut html = String::new();

    // Find all worksheet relationships
    let mut sheet_rels = Vec::new();
    for sheet in root.descendants().filter(|n| n.tag_name().name() == "sheet") {
        if let Some(sheet_id) = sheet.attribute("r:id").or_else(|| sheet.attribute("id")) {
            if let Some((rel_type, target)) = rels.get(sheet_id) {
                if rel_type.contains("worksheet") {
                    let sheet_name = sheet.attribute("name").unwrap_or("Sheet");
                    sheet_rels.push((sheet_name.to_string(), target.clone()));
                }
            }
        }
    }

    // Process each worksheet
    for (sheet_name, target) in sheet_rels {
        // Resolve worksheet path
        let sheet_path = resolve_part_path("xl/workbook.xml", &target)
            .unwrap_or_else(|| format!("xl/{}", target));

        // Read worksheet content
        let sheet_xml = match read_zip_str(archive, &sheet_path) {
            Ok(xml) => xml,
            Err(_) => continue, // Skip invalid worksheets
        };

        // Add worksheet header
        html.push_str(&format!("\n<!-- Worksheet: {} -->\n", sheet_name));

        // Convert worksheet to HTML
        let sheet_html = sheet_to_html(
            &sheet_xml,
            rels,
            images,
            image_dir,
            image_count,
            config,
            archive,
            &sheet_path,
        )?;

        html.push_str(&sheet_html);
        html.push_str("\n\n");
    }

    Ok(html)
}

// Convert individual worksheet to HTML
fn sheet_to_html(
    sheet_xml: &str,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    config: &ParseConfig,
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    sheet_path: &str,
) -> Result<String> {
    let doc = roxmltree::Document::parse(sheet_xml)?;
    let root = doc.root_element();

    let mut html = String::new();

    if let Some(worksheet) = root.descendants().find(|n| n.tag_name().name() == "worksheet") {
        if let Some(sheet_data) = worksheet.descendants().find(|n| n.tag_name().name() == "sheetData") {
            // Build shared strings table if available
            let shared_strings = load_shared_strings(archive);

            // Process rows and cells
            let mut row_num = 0;
            for row in sheet_data.descendants().filter(|n| n.tag_name().name() == "row") {
                row_num += 1;

                // Check row limits based on config
                if row_num > config.max_csv_rows {
                    html.push_str("<!-- Row limit reached -->\n");
                    break;
                }

                html.push_str("<tr>\n");
                process_row(row, &shared_strings, &mut html, config)?;
                html.push_str("</tr>\n");
            }
        }
    }

    if !html.is_empty() {
        html = format!("<table>\n{}</table>", html);
    }

    Ok(html)
}

// Load shared strings table (for cell value references)
fn load_shared_strings(archive: &mut zip::ZipArchive<Cursor<&[u8]>>) -> HashMap<String, String> {
    let mut shared_strings = HashMap::new();

    if let Ok(sst_xml) = read_zip_str(archive, "xl/sharedStrings.xml") {
        if let Ok(doc) = roxmltree::Document::parse(&sst_xml) {
            let root = doc.root_element();
            let mut idx = 0;

            for si in root.descendants().filter(|n| n.tag_name().name() == "si") {
                let mut text = String::new();
                for t in si.descendants().filter(|n| n.tag_name().name() == "t") {
                    if let Some(t_text) = t.text() {
                        text.push_str(t_text);
                    }
                }
                shared_strings.insert(idx.to_string(), text);
                idx += 1;
            }
        }
    }

    shared_strings
}

// Process individual row
fn process_row(
    row: roxmltree::Node,
    shared_strings: &HashMap<String, String>,
    html: &mut String,
    config: &ParseConfig,
) -> Result<()> {
    // Get row height and other properties if needed
    let _row_ht = row.attribute("ht").and_then(|ht| ht.parse::<f64>().ok());

    // Process cells
    for cell in row.descendants().filter(|n| n.tag_name().name() == "c") {
        process_cell(cell, shared_strings, html, config)?;
    }

    Ok(())
}

// Process individual cell
fn process_cell(
    cell: roxmltree::Node,
    shared_strings: &HashMap<String, String>,
    html: &mut String,
    _config: &ParseConfig,
) -> Result<()> {
    let cell_ref = cell.attribute("r").unwrap_or("");

    // Get cell type
    let cell_type = cell.attribute("t").unwrap_or("n"); // n=number, s=shared string, str=string, b=boolean, etc.

    // Get cell value
    let mut cell_value = String::new();
    if let Some(v) = cell.descendants().find(|n| n.tag_name().name() == "v") {
        if let Some(v_text) = v.text() {
            cell_value = v_text.to_string();
        }
    }

    // Resolve shared string if applicable
    if cell_type == "s" {
        if let Ok(idx) = cell_value.parse::<usize>() {
            cell_value = shared_strings.get(&idx.to_string()).cloned().unwrap_or_default();
        }
    }

    // Handle formulas (placeholder - real implementation would evaluate formulas)
    if let Some(f) = cell.descendants().find(|n| n.tag_name().name() == "f") {
        if let Some(formula) = f.text() {
            // In a real implementation, we would evaluate the formula
            // For now, just show the formula or calculated value
            cell_value = format!("={}", formula);
        }
    }

    // Handle merged cells (placeholder)
    let is_merged = cell.descendants().any(|n| n.tag_name().name() == "mergeCell");

    html.push_str("<td");
    if is_merged {
        html.push_str(" colspan=\"2\""); // Simplified handling
    }
    html.push_str(">");

    // Escape HTML special characters
    html.push_str(&html_escape(&cell_value));
    html.push_str("</td>\n");

    Ok(())
}

// Load images from XLSX
fn load_images(
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    rels: &HashMap<String, (String, String)>,
    document_path: &str,
) -> HashMap<String, (Vec<u8>, String)> {
    let mut map = HashMap::new();
    for (rel_id, (rel_type, target)) in rels {
        if !rel_type.contains("image") {
            continue;
        }
        let Some(zip_path) = resolve_part_path(document_path, target) else {
            continue;
        };
        let ext = zip_path.rsplit('.').next().unwrap_or("png").to_lowercase();
        if let Ok(bytes) = read_zip_bytes(archive, &zip_path) {
            map.insert(rel_id.clone(), (bytes, ext));
        }
    }
    map
}

// Post-processing for XLSX
fn post_process_xlsx(markdown: &str, _config: &ParseConfig) -> String {
    // Apply XLSX-specific post-processing
    // This could include table formatting, column alignment, etc.
    markdown.to_string()
}

// Helper functions (reuse from DOCX where possible)
fn read_zip_bytes(archive: &mut zip::ZipArchive<Cursor<&[u8]>>, name: &str) -> Result<Vec<u8>> {
    let mut file = archive.by_name(name)?;
    let mut buf = Vec::new();
    file.read_to_end(&mut buf)?;
    Ok(buf)
}

fn read_zip_str(archive: &mut zip::ZipArchive<Cursor<&[u8]>>, name: &str) -> Result<String> {
    let bytes = read_zip_bytes(archive, name)?;
    Ok(String::from_utf8_lossy(&bytes).into_owned())
}

fn read_rels(
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    document_path: &str,
) -> Result<HashMap<String, (String, String)>> {
    let xml = read_zip_str(archive, &relationship_part_path(document_path))?;
    Ok(parse_relationships(&xml))
}

// HTML escaping function
fn html_escape(s: &str) -> String {
    s.replace("&", "&amp;")
        .replace("<", "&lt;")
        .replace(">", "&gt;")
        .replace("\"", "&quot;")
        .replace("'", "&#39;")
}

// XML helpers for XLSX
fn is_spreadsheet_namespace(namespace: Option<&str>) -> bool {
    namespace.is_some_and(|value| value == S || value.ends_with("/spreadsheetml/main"))
}