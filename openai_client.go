package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sashabaranov/go-openai"
)

type OpenAIClient struct {
	client       *openai.Client
	systemPrompt string
}

func NewOpenAIClient(token string) (*OpenAIClient, error) {

	if token == "" {
		return nil, nil
	}
	client := openai.NewClient(token)

	systemPrompt := `Проанализируй следующее письмо и определи, является ли оно спамом. Верни результат в формате JSON:

- Если письмо спам с вероятностью 95%, верни:
  {
    "is_spam": true,
    "summary": "Краткое описание содержания на языке письма"
  }

- Если письмо содержит код подтверждения или входа, верни:
  {
    "is_spam": false,
    "code": "Код из письма"
  }

- Если письмо не спам или спам с вероятностью менее 95% и не содержит код, верни:
  {
    "is_spam": false
  }

Ты помощник, который кратко пересказывает email-сообщения на языке письма. Не добавляй ничего от себя.`

	return &OpenAIClient{
		client:       client,
		systemPrompt: systemPrompt,
	}, nil
}

type EmailAnalysisResult struct {
	IsSpam  bool   `json:"is_spam"`
	Summary string `json:"summary,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (oac *OpenAIClient) GenerateTextFromEmail(emailText string) (*EmailAnalysisResult, error) {
	if oac.client == nil {
		return nil, errors.New("OpenAI client not initialized")
	}

	if emailText == "" {
		return nil, errors.New("email text is empty")
	}

	resp, err := oac.client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			//Model: "gpt-4o-mini",
			//Model: "gpt-4.1",
			Model: "gpt-4o",
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: oac.systemPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: emailText,
				},
			},
			Temperature: 0.25,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("OpenAI chat completion error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, errors.New("OpenAI returned no choices")
	}

	content := cleanOpenAIResponse(resp.Choices[0].Message.Content)

	var result EmailAnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response as JSON: %w\nResponse: %s", err, content)
	}

	return &result, nil
}

func cleanOpenAIResponse(resp string) string {

	re := regexp.MustCompile("(?s)^```json\\s*(.*)\\s*```$|^```\\s*(.*)\\s*```$")
	matches := re.FindStringSubmatch(resp)
	if len(matches) > 0 {
		for _, m := range matches[1:] {
			if m != "" {
				return m
			}
		}
	}

	return strings.TrimSpace(resp)
}
