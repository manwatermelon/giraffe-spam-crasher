package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ailabhub/giraffe-spam-crasher/internal/ai"
	"github.com/ailabhub/giraffe-spam-crasher/internal/bot"
	"github.com/ailabhub/giraffe-spam-crasher/internal/history"
	"github.com/redis/go-redis/v9"
)

func main() { //nolint:gocyclo,gocognit
	ctx := context.Background()
	logLevel := flag.String("log-level", "info", "Logging level (debug, info, warn, error)")
	historyFile := flag.String("history", "", "Path to the history file")

	apiProvider := flag.String("provider", "openai", "API provider (openai or anthropic)")
	model := flag.String("model", "gpt-4o-mini", "Model to use (e.g., gpt-4 for OpenAI, claude-2 for Anthropic)")
	promptPath := flag.String("prompt", "", "Path to the prompt text file")
	threshold := flag.Float64("spam-threshold", 0.5, "Threshold for classifying a message as spam")
	newUserThreshold := flag.Int("new-user-threshold", 1, "Threshold for classifying user as new")
	var whitelistChannels intSliceFlag
	flag.Var(&whitelistChannels, "whitelist-channels", "Comma-separated list of whitelisted channel IDs")
	flag.Parse()

	var logLevelValue slog.Level
	switch strings.ToLower(*logLevel) {
	case "debug":
		logLevelValue = slog.LevelDebug
	case "info":
		logLevelValue = slog.LevelInfo
	case "warn":
		logLevelValue = slog.LevelWarn
	case "error":
		logLevelValue = slog.LevelError
	default:
		fmt.Printf("Invalid log level: %s. Defaulting to info.\n", *logLevel)
		logLevelValue = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevelValue}))

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		logger.Error("REDIS_URL environment variable is not set")
		os.Exit(1)
	}
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Error("Failed to parse Redis URL", "error", err)
		os.Exit(1)
	}

	rdb := redis.NewClient(redisOptions)

	_, err = rdb.Ping(ctx).Result()
	if err != nil {
		logger.Error("Failed to connect to Redis", "error", err)
		os.Exit(1)
	}

	defer rdb.Close()

	logger.Info("Connected to Redis", "url", redisURL)

	// Check if Redis is empty
	keysCount, err := rdb.DBSize(ctx).Result()
	if err != nil {
		logger.Error("Failed to get Redis database size", "error", err)
		os.Exit(1)
	}

	// Load history if the flag is not empty and Redis is empty
	if *historyFile != "" && keysCount == 0 {
		err := history.ProcessFile(*historyFile, rdb)
		if err != nil {
			logger.Error("Failed to load history", "error", err)
			// Decide whether to continue or exit based on your requirements
			// os.Exit(1)
		} else {
			logger.Info("History loaded", "file", *historyFile)
		}
	} else if keysCount > 0 {
		logger.Info("Redis is not empty. Skipping history load.")
	}

	// Read API key from environment variable
	var apiKey string
	var provider ai.Provider
	rateLimit := 0.0
	switch *apiProvider {
	case "openai":
		apiKey = os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			fmt.Println("OPENAI_API_KEY environment variable is not set")
			os.Exit(1)
		}
		provider = ai.NewOpenAIProvider(apiKey, *model, rateLimit)
		logger.Info("Using OpenAI API", "model", *model)
	case "anthropic":
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			fmt.Println("ANTHROPIC_API_KEY environment variable is not set")
			os.Exit(1)
		}
		provider = ai.NewAnthropicProvider(apiKey, *model, rateLimit)
		logger.Info("Using Anthropic API", "model", *model)
	default:
		fmt.Printf("Unsupported API provider: %s\n", *apiProvider)
		os.Exit(1)
	}
	prompt := ""
	if *promptPath != "" {
		promptBytes, err := os.ReadFile(*promptPath)
		if err != nil {
			logger.Error("Failed to read prompt file", "error", err)
			os.Exit(1)
		}
		prompt = string(promptBytes)
	}
	if prompt == "" {
		fmt.Println("No prompt provided")
		os.Exit(1)
	}

	bot, err := bot.New(logger, rdb, provider, &bot.Config{
		Prompt:            prompt,
		Threshold:         *threshold,
		NewUserThreshold:  *newUserThreshold,
		WhitelistChannels: whitelistChannels,
	})

	if err != nil {
		logger.Error("Failed to create bot", "error", err)
		os.Exit(1)
	}

	go bot.Start()

	// Wait for interrupt signal to gracefully shutdown the bot
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down bot...")
	bot.Stop()
}

// intSliceFlag is a custom flag type for a slice of integers
type intSliceFlag []int64

func (i *intSliceFlag) String() string {
	return fmt.Sprint(*i)
}

func (i *intSliceFlag) Set(value string) error {
	if value == "" {
		return nil
	}
	var intValue int64
	_, err := fmt.Sscanf(value, "%d", &intValue)
	if err != nil {
		return err
	}
	*i = append(*i, intValue)
	return nil
}
