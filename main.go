package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"time"

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
	pluginv1.UnimplementedConfigurationChecksServer
	manifest *pluginv1.PluginManifest
	engine   *hdhr.Engine
}

func (s *runtimeServer) TestConfiguration(ctx context.Context, request *pluginv1.TestConfigurationRequest) (*pluginv1.TestConfigurationResponse, error) {
	var cfg hdhr.Config
	for _, entry := range request.GetConfig() {
		if entry.GetKey() != "connection" {
			continue
		}
		encoded, err := json.Marshal(entry.GetValue().AsMap())
		if err != nil || json.Unmarshal(encoded, &cfg) != nil {
			return &pluginv1.TestConfigurationResponse{Message: "Invalid endpoint configuration."}, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := hdhr.CheckConfiguration(ctx, cfg); err != nil {
		return &pluginv1.TestConfigurationResponse{Message: err.Error()}, nil
	}
	return &pluginv1.TestConfigurationResponse{Ok: true}, nil
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
	if len(os.Args) > 1 && os.Args[1] == "manifest" {
		sdkruntime.Serve(sdkruntime.ServeConfig{Servers: sdkruntime.CapabilityServers{Runtime: &runtimeServer{manifest: manifest}}})
		return
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
	runtime := &runtimeServer{manifest: manifest, engine: engine}
	sdkruntime.ServeManifestWithOptions(manifestJSON, buildVersion, sdkruntime.CapabilityServers{HttpRoutes: server, ScheduledTask: server},
		sdkruntime.WithConfigurationChecks(runtime),
		sdkruntime.WithConfigure(func(ctx context.Context, entries []*pluginv1.ConfigEntry) error {
			_, err := runtime.Configure(ctx, &pluginv1.ConfigureRequest{Config: entries})
			return err
		}))
}
