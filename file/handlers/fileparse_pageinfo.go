package handlers

import "strings"

// generatePageInfo creates virtual page information to match file-parse API
func generatePageInfo(markdown string) []interface{} {
	// For now, create a single page with basic metadata
	hasImage := strings.Contains(markdown, "<img") || strings.Contains(markdown, "![")
	hasTable := strings.Contains(markdown, "<table") || strings.Contains(markdown, "|")

	page := map[string]interface{}{
		"content":    markdown,
		"page_num":   1,
		"has_image":  hasImage,
		"has_table":  hasTable,
		"has_chart":  false, // TODO: detect charts
	}

	return []interface{}{page}
}