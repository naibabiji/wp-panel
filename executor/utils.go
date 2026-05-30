package executor

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"regexp"
	"runtime/debug"
	"strings"
)

func GoSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("goroutine panic: %v\n%s", r, debug.Stack())
			}
		}()
		fn()
	}()
}

func generatePassword(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

func IsValidDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 {
		return false
	}
	re := regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9\-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
	return re.MatchString(domain)
}

func buildServerNames(domain string, aliases []string) string {
	names := []string{domain}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias != "" && IsValidDomain(alias) {
			names = append(names, alias)
		}
	}
	return strings.Join(names, " ")
}
