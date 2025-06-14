package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Record hält den Namen und Institution eines Forschers sowie die zugehörigen URLs.
type Record struct {
	Name string
	URLs []string
}

func main() {
	const inputFile = "links_output.txt"
	const outputFile = "output.csv"

	records, err := parseInput(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Fehler beim Einlesen der Datei %s: %v\n", inputFile, err)
		return
	}

	// CSV-Ausgabe vorbereiten
	csvFile, err := os.Create(outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Fehler beim Erstellen der CSV-Datei %s: %v\n", outputFile, err)
		return
	}
	defer csvFile.Close()

	writer := csv.NewWriter(csvFile)
	defer writer.Flush()
	writer.Write([]string{"Name und Institution", "E-Mail"})

	client := &http.Client{Timeout: 10 * time.Second}
	emailRegex := regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

	for _, rec := range records {
		nameInst := rec.Name

		// paralleles Abrufen und Parsen der URLs
		found := make(map[string]struct{})
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 5) // max 5 gleichzeitig

		for _, rawURL := range rec.URLs {
			urlStr := extractTargetURL(rawURL)
			if strings.Contains(urlStr, "linkedin.com") {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(u string) {
				defer wg.Done()
				defer func() { <-sem }()

				body, err := fetchURL(client, u)
				if err != nil {
					return
				}
				for _, e := range emailRegex.FindAllString(string(body), -1) {
					mu.Lock()
					found[e] = struct{}{}
					mu.Unlock()
				}
			}(urlStr)
		}
		wg.Wait()

		// beste E‑Mail ermitteln
		email := ""
		if len(found) > 0 {
			email = selectBestEmail(rec.Name, found)
		}

		writer.Write([]string{nameInst, email})
	}

	fmt.Printf("Erstellt '%s' mit %d Einträgen.\n", outputFile, len(records))
}

// parseInput liest das Dokument ein und gruppiert Namen/Institution und URLs.
func parseInput(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	var current *Record
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, ":") {
			name := strings.TrimSuffix(line, ":")
			r := Record{Name: name}
			records = append(records, r)
			current = &records[len(records)-1]
		} else if current != nil {
			current.URLs = append(current.URLs, line)
		}
	}
	return records, scanner.Err()
}

// extractTargetURL dekodiert DuckDuckGo-Redirects zu echten URLs.
func extractTargetURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			if dec, err := url.QueryUnescape(uddg); err == nil {
				return dec
			}
		}
	}
	return raw
}

// fetchURL lädt eine URL und gibt den Body zurück.
func fetchURL(client *http.Client, targetURL string) ([]byte, error) {
	resp, err := client.Get(targetURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

// selectBestEmail wählt die E-Mail mit geringster Levenshtein-Distanz.
func selectBestEmail(name string, found map[string]struct{}) string {
	bestDist := -1
	bestEmail := ""
	normName := normalize(name)
	for email := range found {
		d := levenshteinDistance(normName, normalize(strings.Split(email, "@")[0]))
		if bestDist < 0 || d < bestDist {
			bestDist = d
			bestEmail = email
		}
	}
	return bestEmail
}

// normalize entfernt Nicht-Alphanumerisches und wandelt in Kleinbuchstaben um.
func normalize(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9]`)
	return re.ReplaceAllString(s, "")
}

// levenshteinDistance berechnet die minimale Bearbeitungsdistanz.
func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
		dp[i][0] = i
	}
	for j := 1; j <= lb; j++ {
		dp[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			dp[i][j] = min(dp[i-1][j]+1, dp[i][j-1]+1, dp[i-1][j-1]+cost)
		}
	}
	return dp[la][lb]
}

func min(x, y, z int) int {
	m := x
	if y < m {
		m = y
	}
	if z < m {
		m = z
	}
	return m
}
