// TitleLeveler handles title re-leveling based on document structure
pub struct TitleLeveler {
    current_level: usize,
    max_level: usize,
}

impl TitleLeveler {
    pub fn new() -> Self {
        Self {
            current_level: 0,
            max_level: 6,
        }
    }

    // Relevel titles based on actual document hierarchy
    pub fn relevel_titles(&mut self, html: &str) -> String {
        let mut result = String::new();
        let lines: Vec<&str> = html.lines().collect();

        for line in lines {
            if line.starts_with("<h") && line.len() > 2 {
                let level_char = line.chars().nth(1).unwrap_or('1');
                if let Some(level) = level_char.to_digit(10) {
                    let new_level = self.calculate_new_level(level as usize);
                    let new_tag = format!("<h{}", new_level.min(self.max_level));
                    let replaced = line.replacen(&format!("<h{}", level), &new_tag, 1);
                    result.push_str(&replaced);
                } else {
                    result.push_str(line);
                }
            } else {
                result.push_str(line);
            }
            result.push('\n');
        }

        result
    }

    fn calculate_new_level(&mut self, original_level: usize) -> usize {
        // Simple algorithm: maintain relative hierarchy but normalize levels
        if original_level == 1 {
            self.current_level = 1;
        } else if original_level > self.current_level {
            self.current_level = (self.current_level + 1).min(self.max_level);
        } else if original_level < self.current_level {
            self.current_level = original_level.max(1);
        }
        self.current_level
    }
}