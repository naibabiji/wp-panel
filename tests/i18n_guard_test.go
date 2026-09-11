package tests

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const (
	zhLocalePath = "../i18n/locales/zh-CN.json"
	enLocalePath = "../i18n/locales/en-US.json"
)

func TestLocalesHaveMatchingKeys(t *testing.T) {
	zh, en := loadLocales(t)
	assertSameKeys(t, flattenedKeys(zh), flattenedKeys(en))
}

func TestEnglishLocaleContainsNoUnexpectedChinese(t *testing.T) {
	_, en := loadLocales(t)
	hanPattern := regexp.MustCompile(`\p{Han}`)
	for _, key := range flattenedKeys(en) {
		value, ok := lookup(en, key).(string)
		if ok && hanPattern.MatchString(value) {
			t.Errorf("en-US translation %q contains Chinese text: %q", key, value)
		}
	}
}

func TestReferencedTranslationKeysExist(t *testing.T) {
	zh, en := loadLocales(t)
	references := collectTranslationReferences(t)
	for source, keys := range references {
		for _, key := range keys {
			if lookup(zh, key) == nil {
				t.Errorf("%s uses key %q missing from zh-CN", source, key)
			}
			if lookup(en, key) == nil {
				t.Errorf("%s uses key %q missing from en-US", source, key)
			}
		}
	}
}

func TestTemplateScriptKeysAreExposed(t *testing.T) {
	loadLocales(t)
	scriptKeys := collectScriptTranslationKeys(t)
	if len(scriptKeys) == 0 {
		return
	}

	exposed := collectExposedKeys(t)
	for source, keys := range scriptKeys {
		for _, key := range keys {
			if !exposed[key] {
				t.Errorf("%s uses unexposed JavaScript translation key %q", source, key)
			}
		}
	}
}

func loadLocales(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	zh, zhErr := readLocale(zhLocalePath)
	en, enErr := readLocale(enLocalePath)
	if errors.Is(zhErr, fs.ErrNotExist) && errors.Is(enErr, fs.ErrNotExist) {
		t.Skip("i18n locales do not exist yet; guards activate when both locale files are added")
	}
	if zhErr != nil {
		t.Fatalf("read %s: %v", zhLocalePath, zhErr)
	}
	if enErr != nil {
		t.Fatalf("read %s: %v", enLocalePath, enErr)
	}
	return zh, en
}

func readLocale(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var locale map[string]any
	if err := json.Unmarshal(data, &locale); err != nil {
		return nil, err
	}
	return locale, nil
}

func assertSameKeys(t *testing.T, zhKeys, enKeys []string) {
	t.Helper()
	zh := make(map[string]bool, len(zhKeys))
	en := make(map[string]bool, len(enKeys))
	for _, key := range zhKeys {
		zh[key] = true
	}
	for _, key := range enKeys {
		en[key] = true
	}
	for _, key := range zhKeys {
		if !en[key] {
			t.Errorf("en-US locale is missing key %q", key)
		}
	}
	for _, key := range enKeys {
		if !zh[key] {
			t.Errorf("zh-CN locale is missing key %q", key)
		}
	}
}

func flattenedKeys(messages map[string]any) []string {
	var keys []string
	var walk func(map[string]any, string)
	walk = func(current map[string]any, prefix string) {
		for key, value := range current {
			fullKey := key
			if prefix != "" {
				fullKey = prefix + "." + key
			}
			if nested, ok := value.(map[string]any); ok {
				walk(nested, fullKey)
				continue
			}
			keys = append(keys, fullKey)
		}
	}
	walk(messages, "")
	sort.Strings(keys)
	return keys
}

func lookup(messages map[string]any, key string) any {
	var current any = messages
	for _, part := range strings.Split(key, ".") {
		nested, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = nested[part]
		if current == nil {
			return nil
		}
	}
	return current
}

func collectTranslationReferences(t *testing.T) map[string][]string {
	t.Helper()
	references := map[string][]string{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`i18n\.(?:TE|T)\([^,\n]+,\s*"([^"]+)"`),
		regexp.MustCompile(`\{\{t \.Lang "([^"]+)"`),
	}
	walkFiles(t, "..", func(path string, content []byte) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		for _, pattern := range patterns {
			for _, match := range pattern.FindAllSubmatch(content, -1) {
				key := string(match[1])
				if !strings.HasSuffix(key, ".") {
					references[path] = appendUnique(references[path], key)
				}
			}
		}
	})
	for source, keys := range collectScriptTranslationKeys(t) {
		references[source] = appendUnique(references[source], keys...)
	}
	return references
}

func collectScriptTranslationKeys(t *testing.T) map[string][]string {
	t.Helper()
	keys := map[string][]string{}
	keyPattern := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])t\('([a-z][a-z0-9_.-]+)'`)

	walkFiles(t, "../templates", func(path string, content []byte) {
		for _, match := range keyPattern.FindAllSubmatch(content, -1) {
			keys[path] = appendUnique(keys[path], string(match[1]))
		}
	})
	content, err := os.ReadFile("../static/js/app.js")
	if err == nil {
		for _, match := range keyPattern.FindAllSubmatch(content, -1) {
			keys["../static/js/app.js"] = appendUnique(keys["../static/js/app.js"], string(match[1]))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return keys
}

func collectExposedKeys(t *testing.T) map[string]bool {
	t.Helper()
	content, err := os.ReadFile("../router/router.go")
	if err != nil {
		t.Fatal(err)
	}
	blockPattern := regexp.MustCompile(`(?s)var\s+i18nKeys\s*=\s*\[\]string\s*\{(.*?)\}`)
	match := blockPattern.FindSubmatch(content)
	if match == nil {
		t.Fatal("router/router.go must define i18nKeys when JavaScript translation keys are used")
	}
	keyPattern := regexp.MustCompile(`"([a-z][a-z0-9_.-]+)"`)
	exposed := map[string]bool{}
	for _, keyMatch := range keyPattern.FindAllSubmatch(match[1], -1) {
		exposed[string(keyMatch[1])] = true
	}
	return exposed
}

func walkFiles(t *testing.T, root string, visit func(string, []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".gocache", ".codex-deploy", ".playwright-cli", "dist", "output":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".html", ".js":
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			visit(path, content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if !seen[value] {
			values = append(values, value)
			seen[value] = true
		}
	}
	sort.Strings(values)
	return values
}
