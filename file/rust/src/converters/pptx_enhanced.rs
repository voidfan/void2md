use anyhow::{anyhow, Result};
use std::collections::HashMap;
use std::fs;
use std::io::{Cursor, Read};

use crate::config::ParseConfig;
use crate::converters::pptx_chart_analyze::ChartAnalyzer;
use crate::converters::ooxml::{
    office_document_path, parse_relationships, relationship_part_path, resolve_part_path,
};
use crate::types::{ConversionResult, Converter, StreamInfo};
use crate::utils::html_to_md::{html_to_markdown_generated, normalize_tables_html};

// Reuse existing constants
const P: &str = "http://schemas.openxmlformats.org/presentationml/2006/main";
const R: &str = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const A: &str = "http://schemas.openxmlformats.org/drawingml/2006/main";
const IMG_TOKEN_PREFIX: &str = "MARKITIMGTOKEN";

pub struct PptxConverter;

impl Converter for PptxConverter {
    fn name(&self) -> &'static str {
        "pptx"
    }

    fn accepts(&self, info: &StreamInfo) -> bool {
        if info.extension.as_deref() == Some(".pptx") {
            return true;
        }
        info.mimetype.as_deref().is_some_and(|m| {
            m.starts_with("application/vnd.openxmlformats-officedocument.presentationml.presentation")
        })
    }

    fn convert(&self, input: &[u8], info: &StreamInfo) -> Result<ConversionResult> {
        // Parse configuration from StreamInfo.extra (JSON string)
        let config_json = info.extra.as_deref().unwrap_or("");
        let config = crate::config::ParseConfig::from_json_or_default(config_json);

        let cursor = Cursor::new(input);
        let mut archive =
            zip::ZipArchive::new(cursor).map_err(|e| anyhow!("Invalid PPTX: {}", e))?;

        let presentation_path = read_zip_str(&mut archive, "_rels/.rels")
            .ok()
            .and_then(|xml| office_document_path(&xml).ok())
            .unwrap_or_else(|| "ppt/presentation.xml".to_string());

        // Relationship map: id → (rel_type, target)
        let rels = read_rels(&mut archive, &presentation_path).unwrap_or_default();

        let pres_xml = read_zip_str(&mut archive, &presentation_path)
            .map_err(|_| anyhow!("Invalid PPTX: missing main presentation part {presentation_path}"))?;

        // Image blobs: rel_id → (bytes, extension)
        let images = load_images(&mut archive, &rels, &presentation_path);

        // Prepare image output dir
        let image_dir = info.image_dir.as_deref();
        if let Some(dir) = image_dir {
            fs::create_dir_all(dir)?;
        }

        // Convert to HTML with enhanced processing
        let mut image_count: usize = 0;
        let html = pres_to_html_enhanced(
            &pres_xml,
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
        let markdown = post_process_pptx(&markdown, &config);

        Ok(ConversionResult::markdown(markdown.trim()))
    }
}

// Enhanced PPTX to HTML conversion
fn pres_to_html_enhanced(
    pres_xml: &str,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    config: &ParseConfig,
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
) -> Result<String> {
    let doc = roxmltree::Document::parse(pres_xml)?;
    let root = doc.root_element();

    let mut html = String::new();
    let mut slide_num = 0;

    // Find all slide relationships
    let mut slide_rels = Vec::new();
    for (rel_id, (rel_type, target)) in rels {
        if rel_type.contains("slide") && !rel_type.contains("slideLayout") && !rel_type.contains("slideMaster") {
            slide_rels.push((rel_id.clone(), target.clone()));
        }
    }

    // Sort slides by ID to maintain order
    slide_rels.sort_by_key(|(id, _)| {
        id.split('_').last().unwrap_or("").parse::<usize>().unwrap_or(0)
    });

    for (rel_id, target) in slide_rels {
        slide_num += 1;

        // Resolve slide path
        let slide_path = resolve_part_path("ppt/presentation.xml", &target)
            .unwrap_or_else(|| target.clone());

        // Read slide content
        let slide_xml = match read_zip_str(archive, &slide_path) {
            Ok(xml) => xml,
            Err(_) => continue, // Skip invalid slides
        };

        // Analyze slide type and content
        let slide_info = analyze_slide(&slide_xml, config);

        // Add slide header based on type
        html.push_str(&format!("\n<!-- Slide {} - {} -->\n", slide_num, slide_info.slide_type));

        if config.chart_analysis && slide_info.has_charts {
            html.push_str("<!-- Chart analysis enabled -->\n");
        }

        // Convert slide content to HTML
        let slide_html = slide_to_html(
            &slide_xml,
            rels,
            images,
            image_dir,
            image_count,
            config,
            archive,
            &slide_path,
        )?;

        html.push_str(&slide_html);
        html.push_str("\n\n");
    }

    Ok(html)
}

// Slide analysis for advanced features
struct SlideInfo {
    slide_type: String,
    has_charts: bool,
    has_notes: bool,
    has_animations: bool,
}

impl SlideInfo {
    fn new() -> Self {
        Self {
            slide_type: "content".to_string(),
            has_charts: false,
            has_notes: false,
            has_animations: false,
        }
    }
}

fn analyze_slide(slide_xml: &str, config: &ParseConfig) -> SlideInfo {
    let mut info = SlideInfo::new();

    if let Ok(doc) = roxmltree::Document::parse(slide_xml) {
        let root = doc.root_element();

        // Detect slide type
        if let Some(sld_layout) = root.descendants().find(|n| n.tag_name().name() == "sldLayout") {
            if let Some(layout_name) = sld_layout.attribute("name") {
                if layout_name.to_lowercase().contains("title") {
                    info.slide_type = "title".to_string();
                } else if layout_name.to_lowercase().contains("blank") {
                    info.slide_type = "blank".to_string();
                }
            }
        }

        // Check for charts (if chart_analysis is enabled)
        if config.chart_analysis {
            info.has_charts = root.descendants().any(|n| {
                n.tag_name().namespace() == Some("http://schemas.openxmlformats.org/drawingml/2006/chart") ||
                n.tag_name().name() == "chart"
            });

            // If charts are found, analyze them
            if info.has_charts {
                // In a real implementation, we would load and parse the actual chart files
                // For now, we'll add a placeholder comment
                html.push_str("<!-- CHART ANALYSIS: Charts detected but not fully parsed -->\n");
            }
        }

        // Check for notes
        info.has_notes = root.descendants().any(|n| n.tag_name().name() == "notes");

        // Check for animations
        info.has_animations = root.descendants().any(|n| {
            n.tag_name().name() == "anim" || n.tag_name().name() == "timing"
        });
    }

    info
}

// Convert individual slide to HTML
fn slide_to_html(
    slide_xml: &str,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    config: &ParseConfig,
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    slide_path: &str,
) -> Result<String> {
    let doc = roxmltree::Document::parse(slide_xml)?;
    let root = doc.root_element();

    let mut html = String::new();
    let mut scratch = String::new();

    // Process slide content
    if let Some(cSld) = root.descendants().find(|n| n.tag_name().name() == "cSld") {
        for sp_tree in cSld.descendants().filter(|n| n.tag_name().name() == "spTree") {
            for shape in sp_tree.children().filter(|n| n.is_element()) {
                process_shape(shape, rels, images, image_dir, image_count, &mut html, &mut scratch, config, archive, slide_path)?;
            }
        }
    }

    Ok(html)
}

// Process individual shape (text box, image, chart, etc.)
fn process_shape(
    shape: roxmltree::Node,
    rels: &HashMap<String, (String, String)>,
    images: &HashMap<String, (Vec<u8>, String)>,
    image_dir: Option<&str>,
    image_count: &mut usize,
    html: &mut String,
    scratch: &mut String,
    config: &ParseConfig,
    archive: &mut zip::ZipArchive<Cursor<&[u8]>>,
    slide_path: &str,
) -> Result<()> {
    // Handle text shapes
    if let Some(tx_body) = shape.descendants().find(|n| n.tag_name().name() == "txBody") {
        scratch.clear();
        process_text_body(tx_body, scratch);

        if !scratch.trim().is_empty() {
            // Determine if this is a title or content
            let is_title = shape.descendants().any(|n| {
                n.tag_name().name() == "ph" &&
                n.attribute("type").map(|t| t == "title" || t == "ctrTitle").unwrap_or(false)
            });

            if is_title {
                html.push_str("<h2>");
                html.push_str(scratch.trim());
                html.push_str("</h2>\n");
            } else {
                html.push_str("<p>");
                html.push_str(scratch.trim());
                html.push_str("</p>\n");
            }
        }
    }

    // Handle images
    if let Some(blip_fill) = shape.descendants().find(|n| n.tag_name().name() == "blipFill") {
        if let Some(blip) = blip_fill.descendants().find(|n| n.tag_name().name() == "blip") {
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
                        let path = format!("{}/slide_image_{}.{}", dir, n, ext_out);
                        let _ = fs::write(&path, img_bytes);
                        html.push_str("<img src=\"");
                        html.push_str(&path);
                        html.push_str("\" alt=\"slide_image_");
                        html.push_str(&n.to_string());
                        html.push_str("\">\n");
                    } else {
                        html.push_str(IMG_TOKEN_PREFIX);
                        html.push_str(&n.to_string());
                        html.push_str("\n");
                    }
                }
            }
        }
    }

    // Handle charts (placeholder - real implementation would parse chart data)
    if config.chart_analysis {
        if shape.descendants().any(|n| {
            n.tag_name().namespace() == Some("http://schemas.openxmlformats.org/drawingml/2006/chart")
        }) {
            html.push_str("<!-- Chart content -->\n");
        }
    }

    Ok(())
}

// Process text body content
fn process_text_body(tx_body: roxmltree::Node, out: &mut String) {
    for p in tx_body.descendants().filter(|n| n.tag_name().name() == "a:p") {
        let mut para_text = String::new();

        for r in p.descendants().filter(|n| n.tag_name().name() == "a:r") {
            if let Some(t) = r.descendants().find(|n| n.tag_name().name() == "a:t") {
                if let Some(text) = t.text() {
                    para_text.push_str(text);
                }
            }
        }

        if !para_text.is_empty() {
            if !out.is_empty() {
                out.push('\n');
            }
            out.push_str(&para_text);
        }
    }
}

// Load images from PPTX
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

// Post-processing for PPTX
fn post_process_pptx(markdown: &str, _config: &ParseConfig) -> String {
    // Apply PPTX-specific post-processing
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

// XML helpers for PPTX
fn is_presentation_namespace(namespace: Option<&str>) -> bool {
    namespace.is_some_and(|value| value == P || value.ends_with("/presentationml/main"))
}

// ... additional helper functions as needed