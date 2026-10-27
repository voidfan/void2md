package api

import (
	"encoding/json"
	"errors"

	"void2md/structured/pkg/metadata"
)

// SqlQueryRequest corresponds to Python's SqlQueryRequest.
type SqlQueryRequest struct {
	SourceConfig DataSourceConfig `json:"source_config"`
	SQL          string           `json:"sql"`
}

// SqlQueryResponse corresponds to Python's SqlQueryResponse.
// cols/data must be non-nil empty slices: Python's default_factory=list serializes as [];
// nil would become null.
type SqlQueryResponse struct {
	Cols []string `json:"cols"`
	Data [][]any  `json:"data"`
}

// QueryByNLRequest corresponds to Python's QueryByNLRequest.
// schema_format is accepted but **never used**: Python does not pass it to the Agent, so the
// Agent default "m_schema" is always in effect. Kept so requests are not rejected.
type QueryByNLRequest struct {
	SourceConfig      DataSourceConfig           `json:"source_config"`
	Query             string                     `json:"query"`
	RetrievedMetadata *metadata.DatabaseMetadata `json:"retrieved_metadata"`
	GenerateSQLLLM    LLMInfo                    `json:"generate_sql_llm"`
	EvaluateSQLLLM    *LLMInfo                   `json:"evaluate_sql_llm"`
	SchemaFormat      string                     `json:"schema_format"`
	Evidence          string                     `json:"evidence"`
	ResultNumLimit    int                        `json:"result_num_limit"`
}

// QueryByNLResponse corresponds to Python's QueryByNLResponse.
type QueryByNLResponse struct {
	Answer       string           `json:"answer"`
	SQL          string           `json:"sql"`
	SQLRes       SqlQueryResponse `json:"sql_res"`
	InputTokens  int              `json:"input_tokens"`
	OutputTokens int              `json:"output_tokens"`
}

// UnmarshalJSON replicates QueryByNLRequest's defaults (evidence="", result_num_limit=100,
// schema_format="markdown") and validation. Like pydantic, one pass reports **all** field errors
// in declaration order; locs are body-relative ("body" prepended by readJSONBody).
func (r *QueryByNLRequest) UnmarshalJSON(data []byte) error {
	raw, objErr := objectFields(data)
	if objErr != nil {
		return &ValidationErrors{Errors: []validationError{*objErr}}
	}
	fs := fieldSet{raw: raw, parent: data}
	var errs []validationError

	r.Evidence = ""
	r.ResultNumLimit = 100
	r.SchemaFormat = "markdown"

	if v, ok := raw["source_config"]; ok {
		fieldErrs, err := decodeModelField("source_config", v, &r.SourceConfig)
		if err != nil {
			return err
		}
		errs = append(errs, fieldErrs...)
	} else {
		errs = append(errs, fs.missing("source_config"))
	}

	fs.str(&errs, "query", &r.Query, true, false)

	if v, ok := raw["retrieved_metadata"]; ok && !isJSONNull(v) {
		var m metadata.DatabaseMetadata
		fieldErrs, err := decodeModelField("retrieved_metadata", v, unmarshalerFunc(func(b []byte) error {
			return json.Unmarshal(b, &m)
		}))
		if err != nil {
			return err
		}
		errs = append(errs, fieldErrs...)
		if len(fieldErrs) == 0 {
			r.RetrievedMetadata = &m
		}
	}

	if v, ok := raw["generate_sql_llm"]; ok {
		fieldErrs, err := decodeModelField("generate_sql_llm", v, &r.GenerateSQLLLM)
		if err != nil {
			return err
		}
		errs = append(errs, fieldErrs...)
	} else {
		errs = append(errs, fs.missing("generate_sql_llm"))
	}

	if v, ok := raw["evaluate_sql_llm"]; ok && !isJSONNull(v) {
		var info LLMInfo
		fieldErrs, err := decodeModelField("evaluate_sql_llm", v, &info)
		if err != nil {
			return err
		}
		errs = append(errs, fieldErrs...)
		if len(fieldErrs) == 0 {
			r.EvaluateSQLLLM = &info
		}
	}

	fs.literal(&errs, "schema_format", &r.SchemaFormat, []string{"markdown", "m_schema"}, false)
	fs.str(&errs, "evidence", &r.Evidence, false, false)
	fs.integer(&errs, "result_num_limit", &r.ResultNumLimit, false)

	if len(errs) > 0 {
		return &ValidationErrors{Errors: errs}
	}
	return nil
}

// UnmarshalJSON replicates SqlQueryRequest's required-field validation.
// source_config errors expand to ["source_config", <tag>, <field>] — DataSourceConfig adds the
// tag itself; here only the ["source_config"] prefix is added.
func (r *SqlQueryRequest) UnmarshalJSON(data []byte) error {
	raw, objErr := objectFields(data)
	if objErr != nil {
		return &ValidationErrors{Errors: []validationError{*objErr}}
	}
	fs := fieldSet{raw: raw, parent: data}
	var errs []validationError

	if v, ok := raw["source_config"]; ok {
		fieldErrs, err := decodeModelField("source_config", v, &r.SourceConfig)
		if err != nil {
			return err
		}
		errs = append(errs, fieldErrs...)
	} else {
		errs = append(errs, fs.missing("source_config"))
	}

	fs.str(&errs, "sql", &r.SQL, true, false)

	if len(errs) > 0 {
		return &ValidationErrors{Errors: errs}
	}
	return nil
}

// decodeModelField parses a model-typed field (source_config / generate_sql_llm / …).
// Returned locs are relative to the request body root (field-name prefix added). Non-object
// value -> model_attributes_type. The error return is for non-validation failures (e.g. inside
// the metadata model); the caller decides.
func decodeModelField(field string, value json.RawMessage, dst json.Unmarshaler) ([]validationError, error) {
	if !isObject(value) {
		return []validationError{{
			Type: "model_attributes_type", Loc: []any{field}, Msg: msgModelAttrs, Input: compactRaw(value),
		}}, nil
	}
	if err := dst.UnmarshalJSON(value); err != nil {
		var ve *ValidationErrors
		if !errors.As(err, &ve) {
			return nil, err
		}
		return relocate(ve.Errors, field), nil
	}
	return nil, nil
}

// unmarshalerFunc adapts a function into a json.Unmarshaler, for models without their own
// UnmarshalJSON (e.g. metadata.DatabaseMetadata).
type unmarshalerFunc func([]byte) error

func (f unmarshalerFunc) UnmarshalJSON(b []byte) error { return f(b) }
