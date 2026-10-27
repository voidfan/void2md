// Package chatmodel corresponds to Python structured/core/chat_model/.
package chatmodel

import "context"

// LLMError corresponds to Python's LLMError(StrEnum); values are lowercase member names.
type LLMError string

const (
	LLMErrorServiceUnavailable LLMError = "serviceunavailable"
	LLMErrorInvalidArgument    LLMError = "invalidargument"
	LLMErrorGenerationError    LLMError = "generationerror"
	LLMErrorUnknownError       LLMError = "unknownerror"
)

// ChatResponse corresponds to Python's ChatResponse.
type ChatResponse struct {
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// ChatException corresponds to Python's ChatException.
type ChatException struct {
	Error LLMError `json:"error"`
	Msg   string   `json:"msg"`
}

// ChatModel corresponds to Python's BaseChatModel abstract base class.
type ChatModel interface {
	// AInvoke corresponds to BaseChatModel.ainvoke: (response, nil) on success,
	// (empty response, exception) on failure.
	AInvoke(ctx context.Context, query string) (*ChatResponse, *ChatException)
}
