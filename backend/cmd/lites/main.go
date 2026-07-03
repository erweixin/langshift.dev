package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"lites/backend/internal/api"
	"lites/backend/internal/config"
	"lites/backend/internal/content"
	"lites/backend/internal/db"
	"lites/backend/internal/event"
	"lites/backend/internal/job"
	"lites/backend/internal/llm"
	"lites/backend/internal/llm/deepseek"
	runpkg "lites/backend/internal/run"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(ctx, os.Args[1:]); err != nil {
		logger.Error("lites exited", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	appConfig, err := config.Load()
	if err != nil {
		return err
	}

	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "serve":
		return serve(ctx, appConfig)
	case "migrate":
		return db.RunMigrations(appConfig.DatabaseURL, appConfig.MigrationsDir)
	case "help", "-h", "--help":
		fmt.Println("usage: lites [serve|migrate]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func serve(ctx context.Context, appConfig config.Config) error {
	pool, err := db.NewPool(ctx, appConfig.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	llmConfig, err := config.LoadLLMConfig(appConfig.LLMConfigPath)
	if err != nil {
		return err
	}
	router, err := newLLMRouter(llmConfig)
	if err != nil {
		return err
	}

	events := event.NewService(pool, event.Options{
		Dispatcher: runpkg.NewReducer(),
	})
	queue := job.NewQueue(pool, job.Options{})
	llmClient := llm.NewClient(pool, router, llm.ClientOptions{})
	contentService := content.NewService(events, content.ServiceOptions{})
	contentWorker := content.NewWorker(queue, events, llmClient, content.WorkerOptions{})
	go contentWorker.Run(ctx)

	server := api.NewServer(api.ServerConfig{
		Addr:              appConfig.HTTPAddr,
		SingleUser:        appConfig.SingleUser,
		ContentGeneration: contentService,
	})
	return server.ListenAndServe(ctx)
}

func newLLMRouter(llmConfig config.LLMConfig) (*llm.Router, error) {
	providers := make(map[string]llm.Provider, len(llmConfig.Providers))
	for name, providerConfig := range llmConfig.Providers {
		switch name {
		case "deepseek":
			providers[name] = deepseek.NewProvider(providerConfig, nil)
		default:
			return nil, fmt.Errorf("unsupported llm provider %q", name)
		}
	}
	return llm.NewRouter(llmConfig, providers)
}
