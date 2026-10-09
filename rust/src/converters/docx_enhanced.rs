use anyhow::{anyhow, Result};
use std::collections::HashMap;
use std::fs;
use std::io::{Cursor, Read};

use crate::config::ParseConfig;
use crate::converters::docx_seal_detect::SealDetector;
use crate::converters::docx_title_relevel::TitleLeveler;
use crate::converters::ooxml::{
    office_document_path, parse_relationships, relationship_part_path, resolve_part_path,
};
use crate::types::{ConversionResult, Converter, StreamInfo};
use crate::utils::html_to_md::{html_to_markdown_generated, normalize_tables_html};

// Reuse existing constants and helpers from original docx.rs
const W: &str = "http://schemas.openxmlformats.org/wordprocessingml/2006/main";
const R: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const A: &str = "http://schemas.openxmlformats.org/drawingml/2006/main";
const IMG_TOKEN_PREFIX: &str = "MARKITIMGTOKEN";

pub struct DocxConverter;

impl Converter for DocxConverter {
    fn name(&self) -> &'static str {
        "docx"
    }

    fn accepts(&self, info: &StreamInfo) -> bool {
        if info.extension.as_deref() == Some(".docx") {
            return true;
        }
        info.mimetype.as_deref().is_some_and(|m| {
            m.starts_with("application/vnd.openxmlformats-officedocument.wordprocessingml.document")
        })
    }

    fn convert(&self, input: &[u8], info: &StreamInfo) -> Result<ConversionResult> {
        // Parse configuration from StreamInfo.extra (JSON string)
        let config_json = info.extra.as_deref().unwrap_or("");
        let config = crate::config::ParseConfig::from_json_or_default(config_json);

        let cursor = Cursor::new(input);
        let mut archive =
            zip::ZipArchive::new(cursor).map_err(|e| anyhow!("Invalid DOCX: {}", e))?;

        let document_path = read_zip_str(&mut archive, "_rels/.rels")
            .ok()
            .and_then(|xml| office_document_path(&xml).ok())
            .unwrap_or_else(|| "word/document.xml".to_string());

        // Relationship map: id → (rel_type, target)
        let rels = read_rels(&mut archive, &document_path).unwrap_or_default();

        let doc_xml = read_zip_str(&mut archive, &document_path)
            .map_err(|_| anyhow!("Invalid DOCX: missing main document part {document_path}"))?;

        // Numbering: numId → isOrdered (optional)
        let num_types = read_numbering(&mut archive, &rels, &document_path).unwrap_or_default();

        // Image blobs: rel_id → (bytes, extension)
        let images = load_images(&mut archive, &rels, &document_path);

        // Prepare image output dir
        let image_dir = info.image_dir.as_deref();
        if let Some(dir) = image_dir {
            fs::create_dir_all(dir)?;
        }

        // Convert to HTML with enhanced processing
        let mut image_count: usize = 0;
        let html = doc_to_html_enhanced(
            &doc_xml,
            &rels,
            &images,
            &num_types,
            image_dir,
            &mut image_count,
            &config,
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
        let markdown = post_process_docx(&markdown, &config);

        Ok(ConversionResult::markdown(markdown.trim()))
    }
}

// Enhanced HTML generation with configuration support
fn doc_to_html_enhanced(
    doc_xml: &str,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    num_types: &HashMap<String, bool>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    config: &ParseConfig,
) -> Result<String> {
    let doc = roxmltree::Document::parse(doc_xml)?;
    let root = doc.root_element();

    let body = root
        .descendants()
        .find(|n| is_wn(*n, "body"))
        .ok_or_else(|| anyhow!("Invalid DOCX: missing w:body"))?;

    let mut html = String::new();
    let mut list_stack: Vec<(String, u32, bool)> = Vec::new();
    let mut scratch = String::new();

    // Enhanced processing: detect document structure
    let mut doc_structure = DocumentStructure::new();

    for node in body.children().filter(|n| n.is_element()) {
        if is_wn(node, "p") {
            // Analyze paragraph for structure detection
            doc_structure.analyze_paragraph(node);

            emit_paragraph_enhanced(
                node,
                rels,
                images,
                num_types,
                image_dir,
                image_count,
                &mut html,
                &mut list_stack,
                &mut scratch,
                config,
            );
        } else if is_wn(node, "tbl") {
            close_all_lists(&mut list_stack, &mut html);
            emit_table_enhanced(
                node,
                rels,
                images,
                image_dir,
                image_count,
                &mut html,
                &mut scratch,
                config,
            );
        }
    }

    close_all_lists(&mut list_stack, &mut html);

    // Apply structure-based enhancements
    html = apply_structure_enhancements(html, &doc_structure, config);

    Ok(html)
}

// Document structure analysis for advanced features
struct DocumentStructure {
    has_abstract: bool,
    has_appendix: bool,
    has_header_footer: bool,
    title_levels: Vec<usize>,
}

impl DocumentStructure {
    fn new() -> Self {
        Self {
            has_abstract: false,
            has_appendix: false,
            has_header_footer: false,
            title_levels: Vec::new(),
        }
    }

    fn analyze_paragraph(&mut self, para: roxmltree::Node) {
        // Check for abstract/appendix keywords
        if let Some(text) = get_paragraph_text(para) {
            let text_lower = text.to_lowercase();
            if text_lower.contains("abstract") || text_lower.contains("摘要") {
                self.has_abstract = true;
            }
            if text_lower.contains("appendix") || text_lower.contains("附录") {
                self.has_appendix = true;
            }
        }

        // Check for header/footer indicators
        if let Some(ppr) = find_wchild(para, "pPr") {
            if find_wchild(ppr, "header") .is_some() || find_wchild(ppr, "footer").is_some() {
                self.has_header_footer = true;
            }
        }

        // Track title levels
        if let Some(ppr) = find_wchild(para, "pPr") {
            if let Some(pstyle) = find_wchild(ppr, "pStyle") {
                if let Some(val) = wattr(pstyle, "val") {
                    if val.starts_with("Heading") {
                        if let Ok(level) = val["Heading".len()..].parse::<usize>() {
                            self.title_levels.push(level);
                        }
                    }
                }
            }
        }
    }
}

fn get_paragraph_text(para: roxmltree::Node) -> Option<String> {
    let mut text = String::new();
    for child in para.children().filter(|n| n.is_element()) {
        if is_wn(child, "r") {
            if let Some(t_node) = find_wchild(child, "t") {
                if let Some(t_text) = t_node.text() {
                    text.push_str(t_text);
                }
            }
        }
    }
    if text.is_empty() {
        None
    } else {
        Some(text)
    }
}

// Enhanced paragraph emission with configuration support
#[allow(clippy::too_many_arguments)]
fn emit_paragraph_enhanced(
    para: roxmltree::Node,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    num_types: &HashMap<String, bool>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    html: &mut String,
    list_stack: &mut Vec<(String, u32, bool)>,
    scratch: &mut String,
    config: &ParseConfig,
) {
    let ppr = find_wchild(para, "pPr");

    // Handle header/footer based on config
    if config.keep_header_footer {
        // Keep header/footer content
    } else {
        // Skip header/footer paragraphs if not configured to keep them
        if ppr.map(|p| find_wchild(p, "header").is_some() || find_wchild(p, "footer").is_some()).unwrap_or(false) {
            return;
        }
    }

    let style_val = ppr
        .and_then(|p| find_wchild(p, "pStyle"))
        .and_then(|s| wattr(s, "val"))
        .unwrap_or("");

    let tag = style_to_tag_enhanced(style_val, config);

    let num_pr = ppr.and_then(|p| find_wchild(p, "numPr"));
    let num_id = num_pr
        .and_then(|np| find_wchild(np, "numId"))
        .and_then(|n| wattr(n, "val"))
        .filter(|v| *v != "0")
        .map(|v| v.to_string());
    let ilvl = num_pr
        .and_then(|np| find_wchild(np, "ilvl"))
        .and_then(|n| wattr(n, "val"))
        .and_then(|v| v.parse::<u32>().ok())
        .unwrap_or(0);

    scratch.clear();
    para_content_html(para, rels, images, image_dir, image_count, scratch);
    let inner = scratch.as_str();

    if let Some(ref nid) = num_id {
        let is_ordered = *num_types.get(nid.as_str()).unwrap_or(&false);
        manage_list(nid, ilvl, is_ordered, html, list_stack);
        html.push_str("<li>");
        html.push_str(inner);
        html.push_str("</li>\n");
    } else {
        close_all_lists(list_stack, html);
        if inner.trim().is_empty() && tag == "p" {
            return;
        }
        html.push('<');
        html.push_str(tag);
        html.push('>');
        html.push_str(inner);
        html.push_str("</");
        html.push_str(tag);
        html.push_str(">\n");
    }
}

fn style_to_tag_enhanced(style: &str, config: &ParseConfig) -> &'static str {
    let base_tag = match style {
        "Title" | "Heading1" | "Heading 1" => "h1",
        "Heading2" | "Heading 2" => "h2",
        "Heading3" | "Heading 3" => "h3",
        "Heading4" | "Heading 4" => "h4",
        "Heading5" | "Heading 5" => "h5",
        "Heading6" | "Heading 6" => "h6",
        _ => "p",
    };

    // Apply title re-leveling if configured
    if config.relevel_titles && base_tag.starts_with('h') {
        // TODO: Implement title re-leveling logic based on document structure
        // For now, return base tag
    }

    base_tag
}

// Enhanced table emission
#[allow(clippy::too_many_arguments)]
fn emit_table_enhanced(
    tbl: roxmltree::Node,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    html: &mut String,
    scratch: &mut String,
    _config: &ParseConfig,
) {
    html.push_str("<table>\n");
    for tr in tbl.children().filter(|n| is_wn(*n, "tr")) {
        html.push_str("<tr>\n");
        for tc in tr.children().filter(|n| is_wn(*n, "tc")) {
            html.push_str("<td>");
            let mut first = true;
            for p in tc.children().filter(|n| is_wn(*n, "p")) {
                scratch.clear();
                para_content_html(p, rels, images, image_dir, image_count, scratch);
                if scratch.trim().is_empty() {
                    continue;
                }
                if !first {
                    html.push_str("<br>");
                }
                html.push_str(scratch);
                first = false;
            }
            html.push_str("</td>\n");
        }
        html.push_str("</tr>\n");
    }
    html.push_str("</table>\n");
}

// Post-processing functions
fn post_process_docx(markdown: &str, config: &ParseConfig) -> String {
    let mut result = markdown.to_string();

    // Handle appendix removal if not configured to keep
    if !config.keep_appendix {
        result = remove_appendix_sections(&result);
    }

    // Handle seal recognition if enabled
    if config.seal_rec {
        result = mark_seal_regions(&result);
    }

    // Apply title re-leveling if configured
    if config.relevel_titles {
        // Note: This is a simplified implementation
        // In practice, we'd need to convert markdown back to HTML,
        // relevel, then convert back to markdown
        // For now, we'll do basic heading level adjustment
        result = relevel_markdown_headings(&result);
    }

    result
}

fn relevel_markdown_headings(markdown: &str) -> String {
    let mut result = String::new();
    let mut current_level = 1;

    for line in markdown.lines() {
        if line.starts_with('#') {
            let original_level = line.chars().take_while(|c| *c == '#').count();
            if original_level == 1 {
                current_level = 1;
            } else if original_level > current_level {
                current_level = (current_level + 1).min(6);
            } else if original_level < current_level {
                current_level = original_level.max(1);
            }

            let new_heading = "#".repeat(current_level);
            let content = line.trim_start_matches('#').trim_start();
            result.push_str(&format!("{} {}\n", new_heading, content));
        } else {
            result.push_str(line);
            result.push('\n');
        }
    }

    result
}

fn remove_appendix_sections(markdown: &str) -> String {
    // Simple implementation: remove sections starting with "Appendix" or "附录"
    let lines: Vec<&str> = markdown.lines().collect();
    let mut result_lines = Vec::new();
    let mut in_appendix = false;

    for line in lines {
        let line_lower = line.to_lowercase();
        if line_lower.starts_with("appendix") || line_lower.starts_with("附录") {
            in_appendix = true;
            continue;
        }

        if in_appendix {
            // Skip lines that are part of appendix
            // Simple heuristic: skip until we hit a heading or empty line
            if line.trim().is_empty() || line.starts_with('#') {
                in_appendix = false;
                result_lines.push(line);
            }
        } else {
            result_lines.push(line);
        }
    }

    result_lines.join("\n")
}

fn mark_seal_regions(markdown: &str) -> String {
    let detector = SealDetector::new();
    let seals = detector.detect_seals(markdown);

    if seals.is_empty() {
        return markdown.to_string();
    }

    let mut result = markdown.to_string();
    for seal in seals {
        if seal.confidence > 0.5 {
            // Add seal marker comment
            let seal_marker = format!("<!-- SEAL DETECTED: {} (confidence: {:.2}) -->",
                                     seal.keyword, seal.confidence);
            result = format!("{}\n{}", seal_marker, result);
            break; // Only add one marker to avoid duplication
        }
    }

    result
}

fn apply_structure_enhancements(html: String, _structure: &DocumentStructure, _config: &ParseConfig) -> String {
    // Apply structure-based enhancements like abstract handling, etc.
    html
}

// Reuse existing helper functions from original docx.rs
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

fn read_numbering(
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    rels: &HashMap<String, (String, String)>,
    document_path: &str,
) -> Result<HashMap<String, bool>> {
    let numbering_path = rels
        .values()
        .find(|(kind, _)| kind.ends_with("/numbering"))
        .and_then(|(_, target)| resolve_part_path(document_path, target))
        .unwrap_or_else(|| {
            resolve_part_path(document_path, "numbering.xml")
                .unwrap_or_else(|| "word/numbering.xml".to_string())
        });
    let xml = match read_zip_str(archive, &numbering_path) {
        Ok(s) => s,
        Err(_) => return Ok(HashMap::new()),
    };
    let doc = roxmltree::Document::parse(&xml)?;
    let root = doc.root_element();

    let mut abstract_ordered: HashMap<String, bool> = HashMap::new();
    for node in root.children().filter(|n| n.is_element()) {
        if is_wn(node, "abstractNum") {
            let abs_id = wattr(node, "abstractNumId").unwrap_or("").to_string();
            let ordered = node
                .descendants()
                .find(|n| is_wn(*n, "numFmt"))
                .and_then(|n| wattr(n, "val"))
                .map(|v| !matches!(v, "bullet" | "none"))
                .unwrap_or(false);
            abstract_ordered.insert(abs_id, ordered);
        }
    }

    let mut map: HashMap<String, bool> = HashMap::new();
    for node in root.children().filter(|n| n.is_element()) {
        if is_wn(node, "num") {
            let num_id = wattr(node, "numId").unwrap_or("").to_string();
            let abs_id = node
                .descendants()
                .find(|n| is_wn(*n, "abstractNumId"))
                .and_then(|n| wattr(n, "val"))
                .unwrap_or("")
                .to_string();
            let ordered = *abstract_ordered.get(&abs_id).unwrap_or(&false);
            map.insert(num_id, ordered);
        }
    }
    Ok(map)
}

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

fn manage_list(
    num_id: &str,
    ilvl: u32,
    is_ordered: bool,
    html: &mut String,
    stack: &mut Vec<(String, u32, bool)>,
) {
    while let Some((top_nid, top_ilvl, top_ordered)) = stack.last() {
        let should_pop = top_nid != num_id || *top_ilvl > ilvl;
        if !should_pop {
            break;
        }
        let tag = if *top_ordered { "ol" } else { "ul" };
        html.push_str(&format!(
            "</{}>
",
            tag
        ));
        stack.pop();
    }

    if stack
        .last()
        .map(|(nid, lvl, _)| nid != num_id || *lvl < ilvl)
        .unwrap_or(true)
    {
        let tag = if is_ordered { "ol" } else { "ul" };
        html.push_str(&format!("<{}>\n", tag));
        stack.push((num_id.to_string(), ilvl, is_ordered));
    }
}

fn close_all_lists(stack: &mut Vec<(String, u32, bool)>, html: &mut String) {
    while let Some((_, _, is_ordered)) = stack.pop() {
        let tag = if is_ordered { "ol" } else { "ul" };
        html.push_str(&format!(
            "</{}>
",
            tag
        ));
    }
}

fn para_content_html(
    para: roxmltree::Node,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    out: &mut String,
) {
    for child in para.children().filter(|n| n.is_element()) {
        if is_wn(child, "r") {
            run_html(child, images, image_dir, image_count, out);
        } else if is_wn(child, "hyperlink") {
            let href = child
                .attribute((R, "id"))
                .or_else(|| child.attribute("id"))
                .and_then(|id| rels.get(id))
                .filter(|(kind, _)| kind.ends_with("/hyperlink") || kind == "hyperlink")
                .map(|(_, target)| target.as_str())
                .unwrap_or("");

            if !href.is_empty() {
                out.push_str("<a href=\"");
                out.push_str(&html_escape(href));
                out.push_str("\">");
            }
            for r in child.children().filter(|n| is_wn(*n, "r")) {
                run_html(r, images, image_dir, image_count, out);
            }
            if !href.is_empty() {
                out.push_str("</a>");
            }
        }
    }
}

fn run_html(
    run: roxmltree::Node,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    out: &mut String,
) {
    let drawing = find_wchild(run, "drawing").or_else(|| {
        run.children()
            .find(|n| n.is_element() && n.tag_name().name() == "drawing")
    });

    if let Some(drawing) = drawing {
        if let Some(blip) = find_descendant_ns(drawing, "blip", A) {
            let embed = blip
                .attribute((R, "embed"))
                .or_else(|| blip.attribute("embed"))
                .unwrap_or("");
            if !embed.is_empty() {
                if let Some((img_bytes, ext)) = images.get(embed) {
                    *image_count += 1;
                    let n = *image_count;
                    let ext_out = if ext == "jpeg" { "jpg" } else { ext.as_str() };

                    if let Some(dir) = image_dir {
                        let path = format!("{}/image_{}.{}", dir, n, ext_out);
                        let _ = fs::write(&path, img_bytes);
                        out.push_str("<img src=\"");
                        out.push_str(&path);
                        out.push_str("\" alt=\"image_");
                        out.push_str(&n.to_string());
                        out.push_str("\">");
                    } else {
                        out.push_str(IMG_TOKEN_PREFIX);
                        out.push_str(&n.to_string());
                    }
                }
            }
        }
        return;
    }

    let start = out.len();
    for child in run.children().filter(|n| n.is_element()) {
        if is_wn(child, "t") {
            out.push_str(&html_escape(child.text().unwrap_or("")));
        } else if is_wn(child, "tab") {
            out.push('\t');
        } else if is_wn(child, "br") {
            out.push_str("<br>");
        }
    }

    if out.len() == start {
        return;
    }

    if let Some(rpr) = find_wchild(run, "rPr") {
        let bold = find_wchild(rpr, "b").is_some();
        let italic = find_wchild(rpr, "i").is_some();
        let strike = find_wchild(rpr, "strike").is_some();

        if strike {
            out.insert_str(start, "<s>");
            out.push_str("</s>");
        }
        if italic {
            out.insert_str(start, "<em>");
            out.push_str("</em>");
        }
        if bold {
            out.insert_str(start, "<strong>");
            out.push_str("</strong>");
        }
    }
}

// XML helpers (same as original)
fn is_word_namespace(namespace: Option<&str>) -> bool {
    namespace.is_some_and(|value| value == W || value.ends_with("/wordprocessingml/main"))
}

fn is_wn(node: roxmltree::Node, name: &str) -> bool {
    node.is_element()
        && node.tag_name().name() == name
        && is_word_namespace(node.tag_name().namespace())
}

fn wattr<'a>(node: roxmltree::Node<'a, '_>, name: &str) -> Option<&'a str> {
    node.attributes()
        .find(|attribute| attribute.name() == name && is_word_namespace(attribute.namespace()))
        .map(|attribute| attribute.value())
        .or_else(|| node.attribute(name))
}

fn find_wchild<'a, 'input>(
    node: roxmltree::Node<'a, 'input>,
    name: &str,
) -> Option<roxmltree::Node<'a, 'input>> {
    node.children().find(|n| {
        n.is_element() && n.tag_name().name() == name && is_word_namespace(n.tag_name().namespace())
    })
}

fn find_descendant_ns<'a, 'input>(
    node: roxmltree::Node<'a, 'input>,
    name: &str,
    ns: &str,
) -> Option<roxmltree::Node<'a, 'input>> {
    for child in node.children().filter(|n| n.is_element()) {
        if child.tag_name().name() == name && child.tag_name().namespace() == Some(ns) {
            return Some(child);
        }
        if let Some(found) = find_descendant_ns(child, name, ns) {
            return Some(found);
        }
    }
    None
}

fn html_escape(s: &str) -> std::borrow::Cow<'_, str> {
    let bytes = s.as_bytes();
    let Some(first) = bytes
        .iter()
        .position(|b| matches!(b, b'&' | b'<' | b'>' | b'"'))
    else {
        return std::borrow::Cow::Borrowed(s);
    };
    let mut out = String::with_capacity(s.len() + 8);
    out.push_str(&s[..first]);
    let mut start = first;
    for (i, &b) in bytes.iter().enumerate().skip(first) {
        let rep = match b {
            b'&' => "&amp;",
            b'<' => "&lt;",
            b'>' => "&gt;",
            b'"' => "&quot;",
            _ => continue,
        };
        out.push_str(&s[start..i]);
        out.push_str(rep);
        start = i + 1;
    }
    out.push_str(&s[start..]);
    std::borrow::Cow::Owned(out)
}