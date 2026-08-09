package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	plugin "github.com/reptil1990/zoraxy-dns-resolver/mod/zoraxy_plugin"
)

//go:embed www/*
var webContent embed.FS

const (
	PLUGIN_ID = "dns-resolver"
	UI_PATH   = "/ui"
)

var logOut io.Writer = os.Stdout

func main() {
	cfg, err := plugin.ServeAndRecvSpec(&plugin.IntroSpect{
		ID:            PLUGIN_ID,
		Name:          "DNS Resolver",
		Author:        "reptil1990",
		AuthorContact: "reptil1990@me.com",
		Description:   "Split-horizon DNS resolver: answers internal hostnames locally and forwards everything else to an upstream resolver",
		URL:           "https://github.com/reptil1990/zoraxy-dns-resolver",
		Type:          plugin.PluginType_Utilities,
		VersionMajor:  0,
		VersionMinor:  2,
		VersionPatch:  0,
		UIPath:        UI_PATH,
		PermittedAPIEndpoints: []plugin.PermittedAPIEndpoint{
			{
				Method:   http.MethodPost,
				Endpoint: "/plugin/api/proxy/list",
				Reason:   "Auto-sync internal DNS records from the configured HTTP reverse proxy hosts",
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "startup error:", err)
		os.Exit(1)
	}

	store := loadConfig()
	st := newStats(time.Now())
	cache := newCache()
	zoraxy := newZoraxyClient(cfg.ZoraxyPort, cfg.APIKey)
	recs := newRecords(store, zoraxy)
	recs.startSync()

	res := &resolver{cfg: store, cache: cache, recs: recs, stats: st}
	mgr := &dnsManager{handler: res}
	if err := mgr.apply(store.get().DNSPort); err != nil {
		fmt.Fprintln(logOut, "dns start:", err)
	}

	mux := http.NewServeMux()
	uiRouter := plugin.NewPluginEmbedUIRouter(PLUGIN_ID, &webContent, "/www", UI_PATH)
	uiRouter.RegisterTerminateHandler(func() {}, mux)
	mux.Handle(UI_PATH+"/", uiRouter.Handler())
	mux.HandleFunc(UI_PATH+"/api/stats", handleStats(st, mgr, recs))
	mux.HandleFunc(UI_PATH+"/api/config", handleConfig(store, mgr, recs))

	pluginAddr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	fmt.Fprintln(logOut, "Plugin server listening on", pluginAddr)
	if err := http.ListenAndServe(pluginAddr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "plugin server:", err)
		os.Exit(1)
	}
}

func handleStats(st *stats, mgr *dnsManager, recs *records) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"dns_port":     mgr.port,
			"record_count": recs.size(),
			"sync":         recs.syncStatus(),
			"stats":        st.snapshot(time.Now()),
		})
	}
}

func handleConfig(store *configStore, mgr *dnsManager, recs *records) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, store.get())

		case http.MethodPost:
			var incoming Config
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if incoming.DNSPort < 1 || incoming.DNSPort > 65535 {
				http.Error(w, "dns_port out of range", http.StatusBadRequest)
				return
			}

			oldPort := store.get().DNSPort
			if err := store.set(incoming); err != nil {
				http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
				return
			}

			result := map[string]any{"saved": true}
			if incoming.DNSPort != oldPort {
				if err := mgr.apply(incoming.DNSPort); err != nil {
					result["dns_error"] = err.Error()
				}
			}
			go recs.rebuild()
			writeJSON(w, result)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
