package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// void used for deduplication
type void struct{}

// List of User-Agent strings for rotation
var defaultUserAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.1 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/115.0",
}

// newClient creates an HTTP client with a cookie jar and timeout.
func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
	}
}

// randomUserAgent picks a random User-Agent.
func randomUserAgent() string {
	return defaultUserAgents[rand.Intn(len(defaultUserAgents))]
}

// getVQD fetches the DuckDuckGo token needed for search.
func getVQD(client *http.Client, query string) (string, error) {
	url := fmt.Sprintf("https://duckduckgo.com/?q=%s", url.QueryEscape(query))
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", randomUserAgent())
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`vqd=['"](\d+-[0-9a-f]+)['"]`)
	m := re.FindStringSubmatch(string(body))
	if len(m) < 2 {
		return "", fmt.Errorf("vqd token not found")
	}
	return m[1], nil
}

// searchDuckDuckGoHTML performs a DuckDuckGo search and returns result URLs.
func searchDuckDuckGoHTML(client *http.Client, query string) ([]string, error) {
	base := "https://duckduckgo.com/html/"

	var resp *http.Response
	// Retry with backoff
	for attempt := 1; attempt <= 8; attempt++ {
		vqd, err := getVQD(client, query)
		if err != nil {
			return nil, err
		}

		params := url.Values{}
		params.Set("q", query)
		params.Set("vqd", vqd)
		params.Set("kl", "us-en")
		searchURL := base + "?" + params.Encode()

		req, err := http.NewRequest("GET", searchURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", randomUserAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		req.Header.Set("Referer", "https://duckduckgo.com/")

		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusOK {
			break
		}

		resp.Body.Close()
		// exponential backoff + jitter
		delay := time.Duration(attempt*700)*time.Millisecond + time.Duration(rand.Intn(700))*time.Millisecond
		if resp.StatusCode == http.StatusAccepted {
			delay += 3 * time.Second
		}
		time.Sleep(delay)
		if attempt%4 == 0 {
			client = newClient()
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed after retries: status %d", resp.StatusCode)
	}

	// parse HTML
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}

	var links []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			var href, cls string
			for _, a := range n.Attr {
				if a.Key == "class" {
					cls = a.Val
				} else if a.Key == "href" {
					href = a.Val
				}
			}
			if strings.Contains(cls, "result__a") && href != "" {
				link := href
				if strings.HasPrefix(link, "//") {
					link = "https:" + link
				}
				u, err := url.QueryUnescape(link)
				if err == nil {
					link = u
				}
				if strings.HasPrefix(link, "/l/?") {
					q, _ := url.ParseQuery(strings.TrimPrefix(link, "/l/?"))
					if real, ok := q["uddg"]; ok && len(real) > 0 {
						link = real[0]
					}
				}
				links = append(links, link)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	// dedupe
	seen := map[string]void{}
	var unique []string
	for _, l := range links {
		if _, ok := seen[l]; !ok {
			seen[l] = void{}
			unique = append(unique, l)
		}
	}
	return unique, nil
}

func main() {
	rand.Seed(time.Now().UnixNano())

	inPath := flag.String("input", "list_of_names_and_affiliations.csv", "CSV input path")
	outPath := flag.String("output", "links_output.txt", "Output path")
	flag.Parse()

	inFile, err := os.Open(*inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening input: %v\n", err)
		os.Exit(1)
	}
	defer inFile.Close()

	rdr := csv.NewReader(inFile)
	rdr.FieldsPerRecord = -1
	recs, err := rdr.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading CSV: %v\n", err)
		os.Exit(1)
	}

	os.MkdirAll(filepath.Dir(*outPath), 0755)
	outFile, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()
	w := bufio.NewWriter(outFile)

	client := newClient()

	for i, row := range recs {
		if len(row) == 0 || strings.TrimSpace(row[0]) == "" {
			continue
		}
		if i == 0 {
			low := strings.ToLower(strings.Join(row, ","))
			if strings.Contains(low, "name") && strings.Contains(low, "institution") {
				continue
			}
		}

		if i > 0 && i%5 == 0 {
			client = newClient()
		}

		query := strings.TrimSpace(row[0])
		if len(row) > 1 && strings.TrimSpace(row[1]) != "" {
			query += " " + strings.TrimSpace(row[1])
		}

		fmt.Fprintf(os.Stderr, "Searching for: %s\n", query)
		links, err := searchDuckDuckGoHTML(client, query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error for %s: %v\n", query, err)
			continue
		}

		w.WriteString(query + ":\n")
		if len(links) == 0 {
			w.WriteString("(no results)\n")
		} else {
			for _, l := range links {
				w.WriteString(l + "\n")
			}
		}
		w.WriteString("\n")

		time.Sleep(time.Duration(700+rand.Intn(800)) * time.Millisecond)
	}

	w.Flush()
	fmt.Fprintln(os.Stderr, "Output written.")
}
