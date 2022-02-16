package main

import (
	"os"
	"strconv"
	"time"

	"github.com/reconquest/pkg/log"
)

func intEnv(key string) int {
	value := stringEnv(key)

	result, err := strconv.Atoi(value)
	if err != nil {
		log.Fatalf(err, "string to int: %s", key)
	}

	return result
}

func stringEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf(nil, "no env %q specified", key)
	}

	return value
}

func durationEnv(key string) time.Duration {
	value := stringEnv(key)

	duration, err := time.ParseDuration(value)
	if err != nil {
		log.Fatalf(err, "parse duration: %s for %s", value, key)
	}

	return duration
}
