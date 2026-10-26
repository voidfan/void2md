package domain

import (
	"strings"

	"void2md/dbc/pkg/intent"
)

// DomainClassifier classifies queries into business domains
type DomainClassifier struct {
	domainKeywords map[string][]string
}

// NewDomainClassifier creates a new domain classifier with default keywords
func NewDomainClassifier() *DomainClassifier {
	return &DomainClassifier{
		domainKeywords: map[string][]string{
			"sales": {
				"sale", "revenue", "income", "profit", "cost", "price", "order", "purchase",
				"transaction", "billing", "invoice", "payment", "customer", "client",
			},
			"support": {
				"issue", "ticket", "bug", "defect", "problem", "task", "work", "jira",
				"resolution", "status", "priority", "assignee", "reporter", "comment",
			},
			"inventory": {
				"stock", "inventory", "product", "item", "warehouse", "quantity", "sku",
				"supply", "demand", "availability", "shelf", "storage",
			},
			"marketing": {
				"campaign", "ad", "advertisement", "click", "conversion", "lead", "prospect",
				"email", "social", "engagement", "impression", "roi", "analytics",
			},
			"hr": {
				"employee", "staff", "hire", "recruit", "salary", "payroll", "benefit",
				"department", "team", "manager", "performance", "review", "training",
			},
			"finance": {
				"account", "balance", "budget", "expense", "revenue", "tax", "audit",
				"financial", "statement", "report", "forecast", "projection",
			},
		},
	}
}

// ClassifyDomain classifies an Intent IR into a business domain
func (dc *DomainClassifier) ClassifyDomain(ir *intent.IntentIR) string {
	// Check target metrics first
	for _, metric := range ir.TargetMetrics {
		if domain := dc.classifyTerm(metric); domain != "" {
			return domain
		}
	}

	// Check implicit assumptions
	for _, assumption := range ir.ImplicitAssumptions {
		if domain := dc.classifyTerm(assumption); domain != "" {
			return domain
		}
	}

	// Check filter conditions
	for _, filter := range ir.FilterConditions {
		if domain := dc.classifyTerm(filter.Field); domain != "" {
			return domain
		}
		if domain := dc.classifyTerm(filter.Value); domain != "" {
			return domain
		}
	}

	// Check grouping dimensions
	for _, dim := range ir.GroupingDimensions {
		if domain := dc.classifyTerm(dim.Field); domain != "" {
			return domain
		}
	}

	// Default to "support" for Jira-like databases
	return "support"
}

// classifyTerm classifies a single term into a business domain
func (dc *DomainClassifier) classifyTerm(term string) string {
	termLower := strings.ToLower(term)

	for domain, keywords := range dc.domainKeywords {
		for _, keyword := range keywords {
			if strings.Contains(termLower, keyword) {
				return domain
			}
		}
	}

	return ""
}

// GetDomainTables returns tables that are likely relevant for a given domain
func (dc *DomainClassifier) GetDomainTables(domain string) []string {
	domainTables := map[string][]string{
		"sales": {
			"orders", "sales", "transactions", "customers", "products", "invoices",
			"payments", "pricing", "discounts",
		},
		"support": {
			"jiraissue", "issues", "tickets", "bugs", "tasks", "work_items",
			"comments", "attachments", "workflows", "statuses", "priorities",
		},
		"inventory": {
			"inventory", "stock", "products", "items", "warehouses", "suppliers",
			"purchase_orders", "shipments", "locations",
		},
		"marketing": {
			"campaigns", "ads", "clicks", "conversions", "leads", "prospects",
			"emails", "social_posts", "engagements", "analytics",
		},
		"hr": {
			"employees", "staff", "departments", "teams", "managers", "salaries",
			"benefits", "performance_reviews", "training", "hiring",
		},
		"finance": {
			"accounts", "balances", "budgets", "expenses", "revenues", "taxes",
			"financial_statements", "reports", "forecasts", "projections",
		},
	}

	if tables, exists := domainTables[domain]; exists {
		return tables
	}

	// Return generic tables if domain not found
	return []string{"data", "records", "entries", "items"}
}