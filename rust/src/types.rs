use anyhow::Result;
use serde::Serialize;

/// Metadata about the stream being converted.
#[derive(Debug, Clone, Default)]
pub struct StreamInfo {
    pub mimetype: Option<String>,
    pub extension: Option<String>,
    /// Optional source charset hint. Converters currently decode lossy UTF-8.
    #[allow(dead_code)]
    pub charset: Option<String>,
    pub filename: Option<String>,
    pub local_path: Option<String>,
    pub url: Option<String>,
    /// Directory to write extracted images/diagrams.
    pub image_dir: Option<String>,
    /// Embed images as base64 data URLs instead of saving to disk.
    /// When true, image_dir is ignored and images are embedded directly in markdown.
    pub embed_images: bool,
    /// Extra configuration JSON string for enhanced converters
    pub extra: Option<String>,
}

#[derive(Debug, Clone)]
pub struct ConversionResult {
    pub markdown: String,
    pub title: Option<String>,
    pub pages: Vec<PageInfo>,
    pub error: Option<String>,
    pub success: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct PageInfo {
    pub content: String,
    pub page_num: usize,
    pub has_image: bool,
    pub has_table: bool,
    pub has_chart: bool,
}

impl ConversionResult {
    pub fn markdown(markdown: impl Into<String>) -> Self {
        Self {
            markdown: markdown.into(),
            title: None,
            pages: vec![],
            error: None,
            success: true,
        }
    }

    pub fn partial_success(markdown: impl Into<String>, error: impl Into<String>) -> Self {
        Self {
            markdown: markdown.into(),
            title: None,
            pages: vec![],
            error: Some(error.into()),
            success: false,
        }
    }
}

pub trait Converter {
    /// Human-readable name for error messages.
    fn name(&self) -> &'static str;

    /// Quick check: can this converter handle the given stream?
    fn accepts(&self, info: &StreamInfo) -> bool;

    /// Convert the source to markdown.
    fn convert(&self, input: &[u8], info: &StreamInfo) -> Result<ConversionResult>;

    /// Optional URL-first hook. When Some, called before the default fetch
    /// so the converter can handle URL fetching itself.
    fn convert_url(&self, _url: &str) -> Option<Result<ConversionResult>> {
        None
    }
}
