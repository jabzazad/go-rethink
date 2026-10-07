// Package config loads .env and reads typed environment values.
package config

import (
	"bufio"
	"log"
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv reads KEY=VALUE lines from path into the environment.
// Variables already set in the real environment win. A missing file is not an error.
func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}

func String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Float(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return f
}

func Bool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return b
}
