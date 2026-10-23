package main

import (
	"github.com/StuHelper/stuhelper-platform/application"
	"log/slog"
	"os"
)

func main() {
	config, err := application.LoadConfig()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	app, err := application.New(config, application.Modules{})
	if err != nil {
		slog.Error("invalid platform composition", "error", err)
		os.Exit(1)
	}
	app.Run()
}
