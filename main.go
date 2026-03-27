package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

type Config struct {
	Port  int    `json:"port"`
	Rules []Rule `json:"rules"`
}

type Rule struct {
	Name     string `json:"name,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Host     string `json:"host"`
	Sort     int    `json:"sort"`
	Exact    string `json:"exact,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Regex    string `json:"regex,omitempty"`
}

type compiledRule struct {
	rule      Rule
	order     int
	matchType int
	hostname  string
	exact     string
	prefix    string
	regex     *regexp.Regexp
	proxy     *httputil.ReverseProxy
}

const (
	matchPrefix = iota
	matchRegex
	matchExact
)

func main() {
	configPath := flag.String("config", "./config.json", "path to router config json")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	rules, err := compileRules(cfg.Rules)
	if err != nil {
		log.Fatalf("compile rules: %v", err)
	}

	handler := &routerHandler{rules: rules}
	handlerWithCORS := withCORS(handler, map[string]struct{}{
		"https://jeetulsamaiya.com":       {},
		"https://admin.jeetulsamaiya.com": {},
		"https://www.jeetulsamaiya.dev":   {},
		"https://localhost:5173":          {},
	}, []string{
		"jeetulsamaiya.com",
	})
	addr := fmt.Sprintf(":%d", cfg.Port)

	log.Printf("service-request-router listening on %s", addr)
	if err := http.ListenAndServe(addr, handlerWithCORS); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

type routerHandler struct {
	rules []compiledRule
}

func (h *routerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	reqHost := normalizeHostname(r.Host)
	for _, rule := range h.rules {
		if rule.matches(reqHost, path) {
			log.Printf("route matched: method=%s from=%s host=%s path=%s rule=%s target=%s", r.Method, r.RemoteAddr, reqHost, path, ruleIdentifier(rule), rule.rule.Host)
			rule.proxy.ServeHTTP(w, r)
			return
		}
	}

	log.Printf("route not found: method=%s from=%s host=%s path=%s", r.Method, r.RemoteAddr, reqHost, path)
	http.Error(w, fmt.Sprintf("no route configured for path %q", path), http.StatusNotFound)
}

func ruleIdentifier(rule compiledRule) string {
	switch rule.matchType {
	case matchExact:
		return fmt.Sprintf("exact:%s", rule.exact)
	case matchPrefix:
		return fmt.Sprintf("prefix:%s", rule.prefix)
	case matchRegex:
		return fmt.Sprintf("regex:%s", rule.rule.Regex)
	default:
		return "unknown"
	}
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}

	if cfg.Port <= 0 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("invalid port %d", cfg.Port)
	}

	if len(cfg.Rules) == 0 {
		return Config{}, errors.New("rules array cannot be empty")
	}

	return cfg, nil
}

func compileRules(rules []Rule) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))

	for i, rule := range rules {
		if strings.TrimSpace(rule.Host) == "" {
			return nil, fmt.Errorf("rule[%d]: host is required", i)
		}

		targetURL, err := url.Parse(rule.Host)
		if err != nil {
			return nil, fmt.Errorf("rule[%d]: invalid host %q: %w", i, rule.Host, err)
		}
		if targetURL.Scheme == "" || targetURL.Host == "" {
			return nil, fmt.Errorf("rule[%d]: host must be an absolute URL (example: http://service:8080)", i)
		}

		compiledRule := compiledRule{
			rule:  rule,
			order: i,
			proxy: newProxy(targetURL),
		}

		if strings.TrimSpace(rule.Hostname) != "" {
			compiledRule.hostname = strings.ToLower(strings.TrimSpace(rule.Hostname))
		}

		defined := 0
		if rule.Exact != "" {
			defined++
			compiledRule.matchType = matchExact
			compiledRule.exact = rule.Exact
		}
		if rule.Prefix != "" {
			defined++
			compiledRule.matchType = matchPrefix
			compiledRule.prefix = rule.Prefix
		}
		if rule.Regex != "" {
			defined++
			re, err := regexp.Compile(rule.Regex)
			if err != nil {
				return nil, fmt.Errorf("rule[%d]: invalid regex %q: %w", i, rule.Regex, err)
			}
			compiledRule.matchType = matchRegex
			compiledRule.regex = re
		}

		if defined != 1 {
			return nil, fmt.Errorf("rule[%d]: exactly one of exact, prefix, regex is required", i)
		}

		compiled = append(compiled, compiledRule)
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		left := compiled[i]
		right := compiled[j]

		if left.rule.Sort != right.rule.Sort {
			return left.rule.Sort > right.rule.Sort
		}

		leftWeight := matchWeight(left)
		rightWeight := matchWeight(right)
		if leftWeight != rightWeight {
			return leftWeight > rightWeight
		}

		leftLen := specificityLength(left)
		rightLen := specificityLength(right)
		if leftLen != rightLen {
			return leftLen > rightLen
		}

		return left.order < right.order
	})

	return compiled, nil
}

func matchWeight(rule compiledRule) int {
	switch rule.matchType {
	case matchExact:
		return 3
	case matchPrefix:
		return 2
	case matchRegex:
		return 1
	default:
		return 0
	}
}

func specificityLength(rule compiledRule) int {
	switch rule.matchType {
	case matchExact:
		return len(rule.exact)
	case matchPrefix:
		return len(rule.prefix)
	case matchRegex:
		return len(rule.rule.Regex)
	default:
		return 0
	}
}

func (r compiledRule) matches(requestHost, path string) bool {
	if r.hostname != "" && !strings.EqualFold(r.hostname, requestHost) {
		return false
	}

	switch r.matchType {
	case matchExact:
		return path == r.exact
	case matchPrefix:
		return strings.HasPrefix(path, r.prefix)
	case matchRegex:
		return r.regex.MatchString(path)
	default:
		return false
	}
}

func normalizeHostname(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}

	parsed, err := url.Parse("//" + host)
	if err != nil {
		return strings.ToLower(host)
	}

	name := parsed.Hostname()
	if name == "" {
		return strings.ToLower(host)
	}

	return strings.ToLower(name)
}

func newProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	originalModifyResponse := proxy.ModifyResponse

	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Proto", req.URL.Scheme)
		req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		if originalModifyResponse != nil {
			if err := originalModifyResponse(resp); err != nil {
				return err
			}
		}

		resp.Header.Del("Access-Control-Allow-Origin")
		resp.Header.Del("Access-Control-Allow-Methods")
		resp.Header.Del("Access-Control-Allow-Headers")
		resp.Header.Del("Access-Control-Allow-Credentials")
		resp.Header.Del("Access-Control-Expose-Headers")
		resp.Header.Del("Access-Control-Max-Age")

		return nil
	}

	return proxy
}

func withCORS(next http.Handler, allowedOrigins map[string]struct{}, allowedSubdomainRoots []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/")
		originAllowed := isOriginAllowed(origin, allowedOrigins, allowedSubdomainRoots)

		if originAllowed {
			addVaryHeader(w.Header(), "Origin")
			addVaryHeader(w.Header(), "Access-Control-Request-Method")
			addVaryHeader(w.Header(), "Access-Control-Request-Headers")
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			requestedHeaders := strings.TrimSpace(r.Header.Get("Access-Control-Request-Headers"))
			if requestedHeaders != "" {
				w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
			} else {
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Origin, X-Requested-With, If-None-Match")
			}
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			if !originAllowed {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func isOriginAllowed(origin string, allowedOrigins map[string]struct{}, allowedSubdomainRoots []string) bool {
	if _, ok := allowedOrigins[origin]; ok {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" {
		return false
	}

	hostname := strings.ToLower(originURL.Hostname())

	for _, root := range allowedSubdomainRoots {
		normalizedRoot := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(root)), ".")
		if normalizedRoot == "" {
			continue
		}

		if strings.HasSuffix(hostname, "."+normalizedRoot) {
			return true
		}
	}

	return false
}

func addVaryHeader(header http.Header, value string) {
	vary := header.Values("Vary")
	for _, existing := range vary {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
