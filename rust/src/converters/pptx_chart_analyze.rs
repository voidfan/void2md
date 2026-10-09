// ChartAnalyzer analyzes chart content in PPTX files
pub struct ChartAnalyzer;

impl ChartAnalyzer {
    pub fn new() -> Self {
        Self
    }

    // Analyze chart and extract basic information
    pub fn analyze_chart(&self, chart_xml: &str) -> ChartInfo {
        let mut info = ChartInfo::new();

        if let Ok(doc) = roxmltree::Document::parse(chart_xml) {
            let root = doc.root_element();

            // Detect chart type
            info.chart_type = self.detect_chart_type(&root);

            // Extract data series count
            info.series_count = self.count_data_series(&root);

            // Extract categories count
            info.categories_count = self.count_categories(&root);

            // Extract title if available
            info.title = self.extract_chart_title(&root);
        }

        info
    }

    fn detect_chart_type(&self, root: &roxmltree::Node) -> String {
        // Check for common chart type indicators
        if root.descendants().any(|n| n.tag_name().name() == "barChart") {
            return "bar".to_string();
        }
        if root.descendants().any(|n| n.tag_name().name() == "lineChart") {
            return "line".to_string();
        }
        if root.descendants().any(|n| n.tag_name().name() == "pieChart") {
            return "pie".to_string();
        }
        if root.descendants().any(|n| n.tag_name().name() == "scatterChart") {
            return "scatter".to_string();
        }
        "unknown".to_string()
    }

    fn count_data_series(&self, root: &roxmltree::Node) -> usize {
        root.descendants().filter(|n| n.tag_name().name() == "ser").count()
    }

    fn count_categories(&self, root: &roxmltree::Node) -> usize {
        root.descendants().filter(|n| n.tag_name().name() == "cat").count()
    }

    fn extract_chart_title(&self, root: &roxmltree::Node) -> Option<String> {
        if let Some(title_node) = root.descendants().find(|n| n.tag_name().name() == "title") {
            if let Some(tx_node) = title_node.descendants().find(|n| n.tag_name().name() == "tx") {
                if let Some(text) = tx_node.text() {
                    return Some(text.to_string());
                }
            }
        }
        None
    }
}

#[derive(Debug, Clone)]
pub struct ChartInfo {
    pub chart_type: String,
    pub series_count: usize,
    pub categories_count: usize,
    pub title: Option<String>,
}

impl ChartInfo {
    pub fn new() -> Self {
        Self {
            chart_type: "unknown".to_string(),
            series_count: 0,
            categories_count: 0,
            title: None,
        }
    }

    pub fn to_markdown_comment(&self) -> String {
        let mut comment = format!("<!-- CHART: type={}, series={}, categories={}",
                                 self.chart_type, self.series_count, self.categories_count);
        if let Some(ref title) = self.title {
            comment.push_str(&format!(", title=\"{}\"", title));
        }
        comment.push_str(" -->");
        comment
    }
}