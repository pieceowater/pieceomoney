// Package cfg holds the bot's environment configuration -- one Config built
// once from the environment, required keys fail startup instead of silently
// defaulting.
package cfg

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken    string
	OwnerUserID int64

	DefaultCurrency string
	Location        *time.Location

	// Unusual-payment alerts: a payment above UnusualMultiplier x the usual
	// size, and the number of payments in one day that triggers a warning.
	UnusualMultiplier float64
	DailyPaymentsWarn int

	DBPath string
}

var (
	instance *Config
	once     sync.Once
)

func Inst() *Config {
	once.Do(func() {
		if err := godotenv.Load(); err != nil {
			fmt.Println("No .env file found, loading from OS environment variables.")
		}

		tzName := getEnv("TZ_NAME", "Asia/Almaty")
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			log.Fatalf("environment variable TZ_NAME: unknown timezone %q: %v", tzName, err)
		}

		instance = &Config{
			BotToken:    getRequiredEnv("BOT_TOKEN"),
			OwnerUserID: getRequiredInt64("OWNER_USER_ID"),

			DefaultCurrency: strings.ToUpper(getEnv("DEFAULT_CURRENCY", "KZT")),
			Location:        loc,

			UnusualMultiplier: getEnvFloat("UNUSUAL_MULTIPLIER", 3),
			DailyPaymentsWarn: getEnvInt("DAILY_PAYMENTS_WARN", 6),

			DBPath: getEnv("DB_PATH", "data/money.db"),
		}
	})
	return instance
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		return value
	}
	return defaultValue
}

// getRequiredEnv fails startup instead of silently falling back to a
// hardcoded default for a value with no safe default.
func getRequiredEnv(key string) string {
	value, exists := os.LookupEnv(key)
	if !exists || value == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return value
}

func getRequiredInt64(key string) int64 {
	raw := getRequiredEnv(key)
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		log.Fatalf("environment variable %s must be an integer, got %q", key, raw)
	}
	return v
}

func getEnvInt(key string, defaultValue int) int {
	raw, exists := os.LookupEnv(key)
	if !exists || raw == "" {
		return defaultValue
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		log.Fatalf("environment variable %s must be an integer, got %q", key, raw)
	}
	return v
}

func getEnvFloat(key string, defaultValue float64) float64 {
	raw, exists := os.LookupEnv(key)
	if !exists || raw == "" {
		return defaultValue
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		log.Fatalf("environment variable %s must be a positive number, got %q", key, raw)
	}
	return v
}
