package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtimedefault"
	"github.com/theramindex/silo-plugin-hdhomerun/internal/hdhr"
	pluginimpl "github.com/theramindex/silo-plugin-hdhomerun/internal/plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed manifest.json
var manifestJSON []byte
var buildVersion string

type runtimeServer struct {
	runtimedefault.Server
	manifest *pluginv1.PluginManifest
	engine   *hdhr.Engine
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}
func (s *runtimeServer) Configure(_ context.Context, r *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	for _, entry := range r.GetConfig() {
		if entry.GetKey() != "connection" {
			continue
		}
		b, _ := json.Marshal(entry.GetValue().AsMap())
		var cfg hdhr.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid connection settings")
		}
		if err := s.engine.Configure(cfg); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		s.engine.RefreshAsync()
	}
	return &pluginv1.ConfigureResponse{}, nil
}
func main() {
	manifest, err := publicmanifest.LoadWithChecksum(manifestJSON, buildVersion)
	if err != nil {
		panic(err)
	}
	if err := publicmanifest.ValidateCatalogPresentation(manifest, "https://github.com/theramindex/silo-plugin-hdhomerun"); err != nil {
		panic(err)
	}
	dir := os.Getenv("HDHOMERUN_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/continuum/plugins/silo.ramindex.hdhomerun"
	}
	engine, err := hdhr.NewEngine(dir)
	if err != nil {
		panic(err)
	}
	defer engine.Close()
	server := &pluginimpl.Server{Engine: engine}
	sdkruntime.Serve(sdkruntime.ServeConfig{Servers: sdkruntime.CapabilityServers{Runtime: &runtimeServer{manifest: manifest, engine: engine}, HttpRoutes: server, ScheduledTask: server}})
}
