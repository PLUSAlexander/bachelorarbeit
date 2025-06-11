package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/net/html"
)

type Entry struct {
	Identifier string
	Links      []string
}

type Result struct {
	Identifier string
	Email      string
}

func main() {
	inputPath := "links_output.txt"
	outputPath := "emails.csv"

	// API-Key abfragen
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Print("Bitte OPENAI_API_KEY eingeben: ")
		reader := bufio.NewReader(os.Stdin)
		entered, err := reader.ReadString('\n')
		if err != nil {
			log.Fatal("Fehler beim Einlesen des API Key: ", err)
		}
		apiKey = strings.TrimSpace(entered)
		if apiKey == "" {
			log.Fatal("Kein API Key eingegeben. Abbruch.")
		}
	}
	client := openai.NewClient(apiKey)

	entries, err := loadEntries(inputPath)
	if err != nil {
		log.Fatal(err)
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		log.Fatalf("Fehler beim Erstellen der CSV: %v", err)
	}
	defer outFile.Close()
	writer := csv.NewWriter(outFile)
	defer writer.Flush()
	writer.Write([]string{"Name+Institution", "Email"})

	httpClient := &http.Client{Timeout: 15 * time.Second}
	emailRx := regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

	for _, ent := range entries {
		email := findEmailForPerson(context.Background(), client, httpClient, ent, emailRx)
		writer.Write([]string{ent.Identifier, email})
		fmt.Printf("%s -> %s\n", ent.Identifier, email)
	}
}

func loadEntries(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var entries []Entry
	var cur Entry
	for scanner.Scan() {
		l := strings.TrimSpace(scanner.Text())
		if l == "" {
			continue
		}
		if strings.HasSuffix(l, ":") {
			if cur.Identifier != "" {
				entries = append(entries, cur)
			}
			cur = Entry{Identifier: strings.TrimSuffix(l, ":"), Links: nil}
		} else {
			cur.Links = append(cur.Links, l)
		}
	}
	if cur.Identifier != "" {
		entries = append(entries, cur)
	}
	return entries, scanner.Err()
}

func findEmailForPerson(ctx context.Context, client *openai.Client, httpClient *http.Client, ent Entry, emailRx *regexp.Regexp) string {
	model := "gpt-4-turbo"
	urls := ent.Links
	if len(urls) == 0 {
		urls = searchDuckDuckGo(ent.Identifier+" E-Mail", httpClient)
	}
	for _, raw := range urls {
		link := raw
		if strings.Contains(link, "uddg=") {
			if u, err := url.Parse(link); err == nil {
				if q := u.Query().Get("uddg"); q != "" {
					if d, err2 := url.QueryUnescape(q); err2 == nil {
						link = d
					}
				}
			}
		}
		text := fetchPageText(link, httpClient)
		if text == "" {
			continue
		}
		snippet := text
		if len(snippet) > 4000 {
			snippet = snippet[:4000]
		}
		prompt := fmt.Sprintf(
			`Beispiel:
Text: "Kontakt: max.mustermann@example.com" -> max.mustermann@example.com

Seite: %s
%s

Gib nur die E-Mail-Adresse für '%s'. Oder 'Keine gefunden'.`,
			link, snippet, ent.Identifier,
		)
		resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:               model,
			Messages:            []openai.ChatCompletionMessage{{Role: "user", Content: prompt}},
			Temperature:         0.3,
			TopP:                0.9,
			MaxCompletionTokens: 60,
		})
		if err != nil {
			log.Printf("OpenAI-Fehler für %s: %v", link, err)
			continue
		}
		ans := strings.TrimSpace(resp.Choices[0].Message.Content)
		if m := emailRx.FindString(ans); m != "" {
			return m
		}
	}
	return ""
}

func searchDuckDuckGo(query string, httpClient *http.Client) []string {
	u := "https://duckduckgo.com/html/?q=" + url.QueryEscape(query)
	resp, err := httpClient.Get(u)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil
	}
	var links []string
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					links = append(links, a.Val)
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)
	if len(links) > 10 {
		return links[:10]
	}
	return links
}

func fetchPageText(u string, httpClient *http.Client) string {
	resp, err := httpClient.Get(u)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()
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
