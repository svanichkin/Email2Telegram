package main

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"github.com/jhillyerd/enmime"
	"golang.org/x/net/html"
)

// ParsedEmailData holds extracted information from an email
type ParsedEmailData struct {
	From        string
	To          []string
	Subject     string
	TextBody    string
	Attachments map[string][]byte
}

func ParseEmail(raw []byte) (*ParsedEmailData, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	data := &ParsedEmailData{
		Subject:     env.GetHeader("Subject"),
		TextBody:    env.Text,
		Attachments: make(map[string][]byte),
	}

	// Обработка From
	if from := env.GetHeader("From"); from != "" {
		data.From = cleanAddress(from)
	}

	// Обработка To
	if to := env.GetHeader("To"); to != "" {
		data.To = parseAddressList(to)
	} else {
		data.To = []string{}
	}

	if env.HTML != "" {
		data.TextBody = extractTextAndLinks(env.HTML)
	}

	// Обработка вложений
	for _, att := range env.Attachments {
		data.Attachments[att.FileName] = att.Content
	}

	return data, nil
}

func sanitizeToPrintable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) ||
			unicode.IsDigit(r) ||
			unicode.IsPunct(r) ||
			r == ' ' || r == '\n' || r == '\t' || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func extractTextAndLinks(htmlInput string) string {
	doc, err := html.Parse(strings.NewReader(htmlInput))
	if err != nil {
		return ""
	}

	var b strings.Builder

	skipTags := map[string]struct{}{
		"script": {}, "style": {}, "img": {}, "head": {},
	}

	lineBreakTags := map[string]struct{}{
		"br": {}, "p": {}, "div": {}, "li": {}, "tr": {},
	}

	var render func(*html.Node)
	render = func(n *html.Node) {
		if n == nil {
			return
		}

		if n.Type == html.ElementNode {
			if _, skip := skipTags[n.Data]; skip {
				return
			}

			if n.Data == "a" {
				href := ""
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						href = attr.Val
						break
					}
				}
				textInside := getText(n)
				textInside = strings.TrimSpace(textInside)

				if textInside != "" && href != "" {
					// поставим пробел, если он нужен:
					appendSpaceIfNeeded(&b)
					b.WriteString(fmt.Sprintf("[%s](%s)", textInside, href))
					appendSpaceIfNeeded(&b)
					return
				}
			}
		}

		if n.Type == html.TextNode {
			data := strings.TrimSpace(n.Data)
			if data != "" {
				appendSpaceIfNeeded(&b)
				b.WriteString(data)
				appendSpaceIfNeeded(&b)
			}
		} else {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
		}

		if n.Type == html.ElementNode {
			if _, br := lineBreakTags[n.Data]; br {
				b.WriteString("\n")
			}
		}
	}

	render(doc)
	result := condenseSpaceAndLines(b.String())
	result = sanitizeToPrintable(result)
	return result
}

// Функция вставляет пробел, если последний символ не пробельный и не перевод строки
func appendSpaceIfNeeded(b *strings.Builder) {
	if b.Len() == 0 {
		return
	}
	lastChar, _, _ := strings.Cut(b.String()[b.Len()-1:], "")
	if lastChar != " " && lastChar != "\n" {
		b.WriteByte(' ')
	}
}

// Возвращает только текстовое содержимое node (даже если there are inline теги внутри, например <strong>)
func getText(n *html.Node) string {
	var text strings.Builder
	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(n)
	return text.String()
}

// Функция для удаления лишних пробелов и пустых строк, оставлять максимум 1 пустую строку между абзацами
func condenseSpaceAndLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	prevEmpty := true
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if !prevEmpty {
				out = append(out, "")
			}
			prevEmpty = true
		} else {
			out = append(out, l)
			prevEmpty = false
		}
	}
	return strings.Join(out, "\n")
}

func cleanAddress(addr string) string {
	// Удаляем двойные кавычки
	addr = strings.ReplaceAll(addr, "\"", "")

	// Убираем угловые скобки, сохраняя их содержимое
	if start := strings.Index(addr, "<"); start != -1 {
		if end := strings.Index(addr, ">"); end != -1 && end > start {
			email := strings.TrimSpace(addr[start+1 : end])
			name := strings.TrimSpace(addr[:start])

			if name != "" {
				return fmt.Sprintf("%s %s", email, name)
			}
			return email
		}
	}
	return strings.TrimSpace(addr)
}

func parseAddressList(header string) []string {
	var addresses []string
	current := ""
	inQuotes := false
	inAngle := false

	for _, r := range header {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current += string(r)
		case r == '<':
			inAngle = true
			current += string(r)
		case r == '>':
			inAngle = false
			current += string(r)
		case r == ',' && !inQuotes && !inAngle:
			addr := cleanAddress(current)
			if addr != "" {
				addresses = append(addresses, addr)
			}
			current = ""
		default:
			current += string(r)
		}
	}

	// Добавляем последний адрес
	if addr := cleanAddress(current); addr != "" {
		addresses = append(addresses, addr)
	}

	return addresses
}
