use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ParseConfig {
    #[serde(default = "default_keep_header_footer")]
    pub keep_header_footer: bool,
    #[serde(default = "default_keep_appendix")]
    pub keep_appendix: bool,
    #[serde(default = "default_relevel_titles")]
    pub relevel_titles: bool,
    #[serde(default = "default_seal_rec")]
    pub seal_rec: bool,
    #[serde(default = "default_chart_analysis")]
    pub chart_analysis: bool,
    #[serde(default = "default_image_analysis")]
    pub image_analysis: bool,
    #[serde(default = "default_max_csv_rows")]
    pub max_csv_rows: usize,
}

fn default_keep_header_footer() -> bool {
    true
}

fn default_keep_appendix() -> bool {
    true
}

fn default_relevel_titles() -> bool {
    true
}

fn default_seal_rec() -> bool {
    true
}

fn default_chart_analysis() -> bool {
    true
}

fn default_image_analysis() -> bool {
    true
}

fn default_max_csv_rows() -> usize {
    5000
}

impl Default for ParseConfig {
    fn default() -> Self {
        Self {
            keep_header_footer: default_keep_header_footer(),
            keep_appendix: default_keep_appendix(),
            relevel_titles: default_relevel_titles(),
            seal_rec: default_seal_rec(),
            chart_analysis: default_chart_analysis(),
            image_analysis: default_image_analysis(),
            max_csv_rows: default_max_csv_rows(),
        }
    }
}

// Helper function to merge config from JSON string with defaults
impl ParseConfig {
    pub fn from_json_or_default(json_str: &str) -> Self {
        if json_str.is_empty() {
            return Self::default();
        }

        match serde_json::from_str(json_str) {
            Ok(config) => config,
            Err(_) => {
                // If JSON parsing fails, log warning and use defaults
                // In production, this should be logged properly
                Self::default()
            }
        }
    }
}