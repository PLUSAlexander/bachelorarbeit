package main

import (
	"bufio"
	"encoding/csv"
	"flag"
	"fmt"
	"io/ioutil"
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

// Erzeuge einen Client mit CookieJar und Timeout
func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: 15 * time.Second,
	}
}

// getVQD holt das vqd-Token
func getVQD(client *http.Client, query string) (string, error) {
	initURL := fmt.Sprintf("https://duckduckgo.com/?q=%s", url.QueryEscape(query))
	req, _ := http.NewRequest("GET", initURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	re := regexp.MustCompile(`vqd=['"](\d+-[0-9a-f]+)['"]`)
	m := re.FindStringSubmatch(string(body))
	if len(m) < 2 {
		return "", fmt.Errorf("vqd token not found")
	}
	return m[1], nil
}

// searchDuckDuckGoHTML führt die HTML-Suche durch mit Retry-Logik
func searchDuckDuckGoHTML(client *http.Client, query string) ([]string, error) {
	// Hol dir das erste vqd
	vqd, err := getVQD(client, query)
	if err != nil {
		return nil, err
	}

	base := "https://duckduckgo.com/html/"
	params := url.Values{}
	params.Set("q", query)
	params.Set("vqd", vqd)
	params.Set("kl", "us-en")

	var resp *http.Response
	// bis zu 3 Versuche
	for attempt := 1; attempt <= 3; attempt++ {
		searchURL := fmt.Sprintf("%s?%s", base, params.Encode())
		req, _ := http.NewRequest("GET", searchURL, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		// Referer kann helfen
		req.Header.Set("Referer", "https://duckduckgo.com/")

		resp, err = client.Do(req)
		if err != nil {
			return nil, err
		}
		// prüfen, ob 200 OK
		if resp.StatusCode == http.StatusOK {
			break
		}
		// wenn nicht, 2 Sekunden warten und neues vqd holen
		resp.Body.Close()
		time.Sleep(2 * time.Second)
		vqd, _ = getVQD(client, query)
		params.Set("vqd", vqd)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nach 3 Versuchen immer noch Status %d", resp.StatusCode)
	}

	// HTML parsen wie gehabt …
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}

	var links []string
	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			var href, classAttr string
			for _, a := range n.Attr {
				if a.Key == "class" {
					classAttr = a.Val
				} else if a.Key == "href" {
					href = a.Val
				}
			}
			if strings.Contains(classAttr, "result__a") && href != "" {
				raw := href
				if strings.HasPrefix(raw, "//") {
					raw = "https:" + raw
				}
				u, err := url.QueryUnescape(raw)
				if err != nil {
					u = raw
				}
				if strings.HasPrefix(u, "/l/?") {
					parts, _ := url.ParseQuery(strings.TrimPrefix(u, "/l/?"))
					if real, ok := parts["uddg"]; ok && len(real) > 0 {
						u = real[0]
					}
				}
				links = append(links, u)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	// Dedup
	seen := make(map[string]void)
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
	csvPath := flag.String("input", "list_of_names_and_affiliations.csv", "CSV file path")
	outPath := flag.String("output", "links_output.txt", "Output file path")
	flag.Parse()

	inFile, err := os.Open(*csvPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Opening CSV: %v\n", err)
		os.Exit(1)
	}
	defer inFile.Close()

	os.MkdirAll(filepath.Dir(*outPath), 0755)
	outFile, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Creating output: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()
	writer := bufio.NewWriter(outFile)

	reader := csv.NewReader(inFile)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Reading CSV: %v\n", err)
		os.Exit(1)
	}

	// Ein einziger Client für alle
	client := newClient()

	for i, record := range records {
		if len(record) == 0 || strings.TrimSpace(record[0]) == "" {
			continue
		}
		if i == 0 {
			low := strings.ToLower(strings.Join(record, ","))
			if strings.Contains(low, "name") && strings.Contains(low, "institution") {
				continue
			}
		}

		name := strings.TrimSpace(record[0])
		inst := ""
		if len(record) > 1 {
			inst = strings.TrimSpace(record[1])
		}
		query := name
		if inst != "" {
			query += " " + inst
		}

		fmt.Fprintf(os.Stderr, "[DEBUG] Searching for: %s\n", query)
		links, err := searchDuckDuckGoHTML(client, query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] search error for %s: %v\n", query, err)
			continue
		}

		writer.WriteString(name + ":\n")
		if len(links) == 0 {
			writer.WriteString("(no results)\n")
		} else {
			for _, l := range links {
				writer.WriteString(l + "\n")
			}
		}
		writer.WriteString("\n")
	}

	writer.Flush()
	fmt.Fprintf(os.Stderr, "[DEBUG] Output written\n")
}
