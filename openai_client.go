package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
		log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Yellow("No OpenAI token provided, client will be disabled").String())
		return nil, nil
	}
	log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Cyan("Initializing OpenAI client...").String())
	client := openai.NewClient(token)

	systemPrompt :=
		`
Проанализируй письмо и верни результат в формате JSON. Не добавляй ничего от себя.

Если уверенность 95% или выше, выбери один из типов:
- spam — спам
- fishing — фишинг
- notification — уведомление от банка/сервиса
- code — код входа/подтверждения
- human — личная переписка
- unknown — если не подходит ни к одному типу

Формат:
{
  "type": "spam|fishing|notification|code|human|unknown",
  "language": "Определи язык на котором письмо написано",
  "summary": "Краткое описание письма на языке language",
  "unsubscribe": "URL отписки, если есть" // поле необязательное
}
Если type = "code", в summary укажи только сам код.
`
	log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Green(au.Bold("OpenAI client initialized successfully")).String())
	return &OpenAIClient{
		client:       client,
		systemPrompt: systemPrompt,
	}, nil
}

type EmailType string

const (
	TypeSpam         EmailType = "spam"
	TypePhishing     EmailType = "fishing"
	TypeNotification EmailType = "notification"
	TypeCode         EmailType = "code"
	TypeHuman        EmailType = "human"
	TypeUnknown      EmailType = "unknown"
)

type EmailAnalysisResult struct {
	Type        EmailType `json:"type"`
	Summary     string    `json:"summary"`
	Unsubscribe string    `json:"unsubscribe,omitempty"`
}

func (oac *OpenAIClient) GenerateTextFromEmail(emailText string) (*EmailAnalysisResult, error) {

	if oac.client == nil {
		return nil, errors.New("OpenAI client not initialized")
	}
	if emailText == "" {
		return nil, errors.New("email text is empty")
	}

	log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Magenta("Analyzing email content with OpenAI...").String())
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

	log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Blue("Received response from OpenAI, processing...").String())
	content := cleanOpenAIResponse(resp.Choices[0].Message.Content)

	var result EmailAnalysisResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response as JSON: %w\nResponse: %s", err, content)
	}

	log.Printf(au.Gray(12, "[OPENAI]").String()+" "+au.Green("Analysis completed. Type: %s, Unsubscribe: %t, Summary: %t").String(), string(result.Type), result.Summary != "", result.Unsubscribe != "")
	return &result, nil
}

func cleanOpenAIResponse(resp string) string {

	log.Println(au.Gray(12, "[OPENAI]").String() + " " + au.Cyan("Cleaning OpenAI response...").String())
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
