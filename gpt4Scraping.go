package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/net/html"
)

func main() {
	// Flags for input/output
	inputPath := flag.String("input", "links.csv", "Path to input CSV file (first column: Name+Institution, subsequent columns: URLs)")
	outputPath := flag.String("output", "emails.csv", "Path to output CSV file")
	flag.Parse()

	// OpenAI API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable is required")
	}
	client := openai.NewClient(apiKey)

	// Read input CSV
	inFile, err := os.Open(*inputPath)
	if err != nil {
		log.Fatalf("Error opening input file: %v", err)
	}
	defer inFile.Close()

	r := csv.NewReader(inFile)
	allRows, err := r.ReadAll()
	if err != nil {
		log.Fatalf("Error reading CSV: %v", err)
	}

	// Prepare output CSV
	outFile, err := os.Create(*outputPath)
	if err != nil {
		log.Fatalf("Error creating output file: %v", err)
	}
	defer outFile.Close()
	w := csv.NewWriter(outFile)
	defer w.Flush()

	// Write header
	if err := w.Write([]string{"Name+Institution", "Email"}); err != nil {
		log.Fatalf("Error writing header: %v", err)
	}

	// Process rows
	for i, row := range allRows {
		if i == 0 {
			continue
		}
		identifier := row[0]
		urls := row[1:]
		email := findEmailForPerson(context.Background(), client, identifier, urls)
		if err := w.Write([]string{identifier, email}); err != nil {
			log.Printf("Error writing record for %s: %v", identifier, err)
		}
		fmt.Printf("%s -> %s\n", identifier, email)
	}
}

// findEmailForPerson uses GPT-4-mini to extract the email from page text
func findEmailForPerson(ctx context.Context, client *openai.Client, identifier string, urls []string) string {
	emailRegex := regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	model := "o4-mini"

	for _, url := range urls {
		text := fetchPageText(url)
		if text == "" {
			continue
		}
		// Truncate to 2000 chars
		snippet := text
		if len(snippet) > 2000 {
			snippet = snippet[:2000]
		}

		// Build prompt
		prompt := fmt.Sprintf("Hier ist ein Ausschnitt der Seite %s:\n%s\n\nFinde die wahrscheinlichste E-Mail-Adresse der Person '%s'. Wenn keine gefunden wird, antworte nur mit 'Keine gefunden'.", url, snippet, identifier)

		// Call GPT-4-mini
		resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:       model,
			Messages:    []openai.ChatCompletionMessage{{Role: "user", Content: prompt}},
			MaxTokens:   50,
			Temperature: 0,
		})
		if err != nil {
			log.Printf("API error for %s: %v", url, err)
			continue
		}
		answer := strings.TrimSpace(resp.Choices[0].Message.Content)

		// Check for email pattern
		if emailRegex.MatchString(answer) {
			return emailRegex.FindString(answer)
		}
		if strings.EqualFold(answer, "Keine gefunden") {
			continue
		}
		// Fallback: first found email
		if m := emailRegex.FindString(answer); m != "" {
			return m
		}
	}
	return ""
}

// fetchPageText fetches HTML and extracts textual content
func fetchPageText(url string) string {
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	// Parse HTML
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				sb.WriteString(t + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	return sb.String()
}
