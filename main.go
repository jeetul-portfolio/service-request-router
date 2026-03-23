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
	Name   string `json:"name,omitempty"`
	Host   string `json:"host"`
	Sort   int    `json:"sort"`
	Exact  string `json:"exact,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Regex  string `json:"regex,omitempty"`
}

type compiledRule struct {
	rule      Rule
	order     int
	matchType int
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
	addr := fmt.Sprintf(":%d", cfg.Port)

	log.Printf("service-request-router listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

type routerHandler struct {
	rules []compiledRule
}

func (h *routerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	for _, rule := range h.rules {
		if rule.matches(path) {
			log.Printf("route matched: method=%s from=%s path=%s rule=%s target=%s", r.Method, r.RemoteAddr, path, ruleIdentifier(rule), rule.rule.Host)
			rule.proxy.ServeHTTP(w, r)
			return
		}
	}

	log.Printf("route not found: method=%s from=%s path=%s", r.Method, r.RemoteAddr, path)
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

func (r compiledRule) matches(path string) bool {
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

func newProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director

	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Proto", req.URL.Scheme)
		req.Header.Set("X-Forwarded-For", req.RemoteAddr)
	}

	return proxy
}
