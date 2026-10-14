package config

import (
	"os"
	"strconv"
)

type Config struct {
	AppName string
	AppEnv  string
	Port    string
	DB      DBConfig
	JWT     JWTConfig
}

type DBConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
}

type JWTConfig struct {
	Secret string
	TTLMin int
}

func Load() Config {
	cfg := Config{
		AppName: getEnv("APP_NAME", "PTIT_ERIPT_API"),
		AppEnv:  getEnv("APP_ENV", "development"),
		Port:    getEnv("PORT", "8080"),
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnvInt("DB_PORT", 3306),
			Name:     getEnv("DB_DATABASE", "lms_db"),
			User:     getEnv("DB_USERNAME", "lms_user"),
			Password: getEnv("DB_PASSWORD", "lms_password"),
		},
		JWT: JWTConfig{
			Secret: getEnv("JWT_SECRET", "change-me"),
			TTLMin: getEnvInt("JWT_TTL_MINUTES", 120),
		},
	}
	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
