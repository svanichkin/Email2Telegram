package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/sashabaranov/go-openai"
)

// OpenAIClient wraps the go-openai client.
type OpenAIClient struct {
	client *openai.Client
}

// NewOpenAIClient creates a new OpenAI client.
// If the token is empty, it returns a nil client and no error,
// indicating that OpenAI features will be disabled.
func NewOpenAIClient(token string) (*OpenAIClient, error) {
	if token == "" {
		return nil, nil // OpenAI features disabled, not an error for initialization itself.
	}
	client := openai.NewClient(token)
	// Optionally, a test request could be made here to validate the token
	// and return an error if it's invalid. For now, we assume a non-empty token
	// is potentially valid.
	return &OpenAIClient{client: client}, nil
}

// GenerateText generates text using the OpenAI API's chat completion.
func (oac *OpenAIClient) GenerateText(prompt string, model string, temperature float64) (string, error) {
	if oac.client == nil {
		return "", errors.New("OpenAI client not initialized")
	}

	resp, err := oac.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: prompt,
				},
			},
			Temperature: float32(temperature), // Convert float64 to float32
		},
	)

	if err != nil {
		return "", fmt.Errorf("OpenAI chat completion error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", errors.New("OpenAI returned no choices")
	}

	return resp.Choices[0].Message.Content, nil
}
