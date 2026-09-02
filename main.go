package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const apiBase = "https://api.cloudflare.com/client/v4"

var ipv4Re = regexp.MustCompile(`^(\d{1,3})(\.\d{1,3}){3}$`)

var defaultProviders = []string{
	"https://api.cloudflare.com/cdn-cgi/trace",
	"https://api.ipify.org",
	"https://checkip.amazonaws.com",
	"https://ifconfig.me/ip",
}

type config struct {
	apiToken  string
	records   []string
	ttl       int
	interval  int
	cacheFile string
	heartbeat string
	providers []string
	client    *http.Client
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfResponse struct {
	Success bool            `json:"success"`
	Errors  []cfError       `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type dnsRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func loadConfig() (*config, error) {
	cfg := &config{
		apiToken:  os.Getenv("CLOUDFLARE_API_TOKEN"),
		records:   splitRecords(os.Getenv("CF_RECORDS")),
		ttl:       envInt("CF_TTL", 120),
		interval:  envInt("CHECK_INTERVAL", 300),
		cacheFile: envStr("CACHE_FILE", "/data/ip.cache"),
		heartbeat: envStr("HEARTBEAT_FILE", "/data/heartbeat"),
		providers: splitProviders(os.Getenv("CF_IP_PROVIDERS")),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	if cfg.apiToken == "" {
		return nil, fmt.Errorf("CLOUDFLARE_API_TOKEN is required")
	}
	if len(cfg.records) == 0 {
		return nil, fmt.Errorf("CF_RECORDS is required (comma-separated FQDNs)")
	}
	return cfg, nil
}

func splitRecords(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(item)), ".")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func splitProviders(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return defaultProviders
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return defaultProviders
	}
	return out
}

func validIPv4(s string) bool {
	if !ipv4Re.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func getPublicIP(cfg *config) (string, error) {
	var lastErr error
	for _, provider := range cfg.providers {
		ip, err := fetchFromProvider(cfg, provider)
		if err != nil {
			lastErr = err
			log.Printf("provider %s failed: %v", provider, err)
			continue
		}
		if validIPv4(ip) {
			log.Printf("detected public IP %s via %s", ip, provider)
			return ip, nil
		}
		log.Printf("provider %s returned unexpected body", provider)
	}
	return "", fmt.Errorf("unable to determine public IP: %w", lastErr)
}

func fetchFromProvider(cfg *config, provider string) (string, error) {
	resp, err := cfg.client.Get(provider)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	if strings.HasSuffix(provider, "/cdn-cgi/trace") {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "ip=") {
				return strings.TrimSpace(line[3:]), nil
			}
		}
		return "", fmt.Errorf("ip not found in trace")
	}
	return strings.TrimSpace(string(body)), nil
}

func cfRequest(cfg *config, method, url string, payload map[string]interface{}) (*cfResponse, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cfg.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out cfResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func resolveZone(cfg *config, fqdn string) (string, error) {
	labels := strings.Split(fqdn, ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		resp, err := cfRequest(cfg, http.MethodGet, apiBase+"/zones?name="+candidate, nil)
		if err != nil {
			log.Printf("failed to resolve zone %s: %v", candidate, err)
			continue
		}
		if !resp.Success {
			log.Printf("api error resolving zone %s: %v", candidate, cfErrMsg(resp))
			continue
		}
		var zones []zone
		if err := json.Unmarshal(resp.Result, &zones); err == nil && len(zones) > 0 {
			log.Printf("zone %s -> %s", candidate, zones[0].ID)
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("zone not found for %s", fqdn)
}

func findRecord(cfg *config, zoneID, fqdn string) (*dnsRecord, error) {
	resp, err := cfRequest(cfg, http.MethodGet, apiBase+"/zones/"+zoneID+"/dns_records?name="+fqdn+"&type=A", nil)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("list records: %s", cfErrMsg(resp))
	}
	var records []dnsRecord
	if err := json.Unmarshal(resp.Result, &records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no A record for %s", fqdn)
	}
	return &records[0], nil
}

func updateRecord(cfg *config, zoneID string, record *dnsRecord, ip string, ttl int) error {
	payload := map[string]interface{}{
		"content": ip,
		"ttl":     ttl,
		"proxied": false,
	}
	resp, err := cfRequest(cfg, http.MethodPatch, apiBase+"/zones/"+zoneID+"/dns_records/"+record.ID, payload)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("update %s: %s", record.Name, cfErrMsg(resp))
	}
	log.Printf("updated %s -> %s (ttl=%ds)", record.Name, ip, ttl)
	return nil
}

func cfErrMsg(resp *cfResponse) string {
	if len(resp.Errors) == 0 {
		return "unknown error"
	}
	msgs := make([]string, 0, len(resp.Errors))
	for _, e := range resp.Errors {
		msgs = append(msgs, e.Message)
	}
	return strings.Join(msgs, "; ")
}

func readFileTrim(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeFile(path, content string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func doHealthcheck(cfg *config) bool {
	raw, err := readFileTrim(cfg.heartbeat)
	if err != nil {
		return false
	}
	last, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(last, 0)) < time.Duration(cfg.interval)*time.Second*3
}

func run(cfg *config) {
	zoneCache := make(map[string]string)

	for {
		currentIP, err := getPublicIP(cfg)
		if err != nil {
			log.Printf("skipping cycle: %v", err)
		} else {
			cachedIP, _ := readFileTrim(cfg.cacheFile)
			if currentIP == cachedIP {
				log.Printf("IP unchanged (%s)", currentIP)
			} else {
				log.Printf("IP changed: %s -> %s", cachedIP, currentIP)
				updatedAll := true
				for _, fqdn := range cfg.records {
					zoneID, ok := zoneCache[fqdn]
					if !ok {
						if zoneID, err = resolveZone(cfg, fqdn); err != nil {
							log.Printf("%v (skipping)", err)
							updatedAll = false
							continue
						}
						zoneCache[fqdn] = zoneID
					}
					record, err := findRecord(cfg, zoneID, fqdn)
					if err != nil {
						log.Printf("lookup %s failed: %v (skipping)", fqdn, err)
						updatedAll = false
						continue
					}
					if err := updateRecord(cfg, zoneID, record, currentIP, cfg.ttl); err != nil {
						log.Printf("update %s failed: %v", fqdn, err)
						updatedAll = false
					}
				}
				if updatedAll {
					if err := writeFile(cfg.cacheFile, currentIP); err != nil {
						log.Printf("failed to write cache: %v", err)
					}
				}
			}
		}

		if err := writeFile(cfg.heartbeat, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
			log.Printf("failed to write heartbeat: %v", err)
		}

		time.Sleep(time.Duration(cfg.interval) * time.Second)
	}
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "run healthcheck and exit")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("%v", err)
	}

	if *healthcheck {
		if doHealthcheck(cfg) {
			os.Exit(0)
		}
		os.Exit(1)
	}

	log.Printf("starting Cloudflare DDNS — records: %s, interval: %ds, ttl: %ds",
		strings.Join(cfg.records, ", "), cfg.interval, cfg.ttl)

	run(cfg)
}
