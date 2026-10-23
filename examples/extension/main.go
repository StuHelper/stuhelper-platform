// An executable composition example. It is not included in the platform binary.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/StuHelper/stuhelper-platform/application"
	"github.com/StuHelper/stuhelper-platform/module"
	"github.com/StuHelper/stuhelper-platform/transport"
)

func main() {
	config, err := application.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	config.Enabled = append(config.Enabled, "example")
	app, err := application.New(config, application.Modules{
		Definitions: []module.Definition{{
			Descriptor: module.Descriptor{ID: "example", Version: "development", API: 1, Requires: []module.Dependency{{ID: "platform", API: 1}}, Capabilities: []string{"example.status"}},
			Start:      func(context.Context) error { return nil },
			Stop:       func(context.Context) error { return nil },
		}},
		Routes: []transport.Route{{
			Method: "GET", Path: "/api/v1/example/status", Module: "example", Capability: "example.status", Access: transport.Public,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				transport.JSON(w, 200, map[string]string{"status": "synthetic extension ready"})
			}),
		}},
	})
	if err != nil {
		log.Fatal(err)
	}
	app.Run()
}
