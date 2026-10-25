package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	var (
		binaryPath = flag.String("binary", "", "path to built plugin binary")
		version    = flag.String("version", "", "plugin version to set in manifest")
		goos       = flag.String("goos", "linux", "target os")
		goarch     = flag.String("goarch", "amd64", "target arch")
		pluginID   = flag.String("plugin-id", "silo.ramindex.hdhomerun", "plugin_id value")
	)
	flag.Parse()

	if strings.TrimSpace(*binaryPath) == "" {
		fail("binary path is required")
	}
	if strings.TrimSpace(*version) == "" {
		fail("version is required")
	}

	binData, err := os.ReadFile(*binaryPath)
	if err != nil {
		failf("read binary %q: %v", *binaryPath, err)
	}

	sum := sha256.Sum256(binData)
	checksum := hex.EncodeToString(sum[:])

	templatePath := "manifest.json"
	templateData, err := os.ReadFile(templatePath)
	if err != nil {
		failf("read manifest template: %v", err)
	}

	manifest, err := publicmanifest.Load(templateData)
	if err != nil {
		failf("load manifest template: %v", err)
	}
	if err := publicmanifest.ValidateCatalogPresentation(manifest, "https://github.com/theramindex/silo-plugin-hdhomerun"); err != nil {
		failf("catalog presentation: %v", err)
	}

	manifest.PluginId = *pluginID
	manifest.Version = *version
	manifest.Checksum = checksum
	manifest.SupportedPlatforms = []*pluginv1.SupportedPlatform{{Os: firstNonEmpty(*goos, runtime.GOOS), Arch: firstNonEmpty(*goarch, runtime.GOARCH)}}

	baseName := filepath.Base(*binaryPath)
	outManifestPath := filepath.Join(filepath.Dir(*binaryPath), baseName+".manifest.json")
	outManifestData, err := protojson.Marshal(manifest)
	if err != nil {
		failf("encode output manifest: %v", err)
	}
	if err := os.WriteFile(outManifestPath, outManifestData, 0644); err != nil {
		failf("write output manifest: %v", err)
	}

	outChecksumPath := filepath.Join(filepath.Dir(*binaryPath), baseName+".sha256")
	if err := os.WriteFile(outChecksumPath, []byte(fmt.Sprintf("%s  %s\n", checksum, baseName)), 0644); err != nil {
		failf("write checksum file: %v", err)
	}

	outZipPath := filepath.Join(filepath.Dir(*binaryPath), baseName+".silo-plugin.zip")
	if err := writeUploadZip(outZipPath, binData, outManifestData); err != nil {
		failf("write upload zip: %v", err)
	}

	fmt.Printf("binary=%s\n", *binaryPath)
	fmt.Printf("manifest=%s\n", outManifestPath)
	fmt.Printf("checksum=%s\n", outChecksumPath)
	fmt.Printf("silo_plugin_zip=%s\n", outZipPath)
}

func writeUploadZip(path string, binaryData, manifestData []byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	zipWriter := zip.NewWriter(file)
	if err := writeZipEntry(zipWriter, "plugin", 0755, binaryData); err != nil {
		_ = zipWriter.Close()
		return err
	}
	if err := writeZipEntry(zipWriter, "manifest.json", 0644, manifestData); err != nil {
		_ = zipWriter.Close()
		return err
	}
	return zipWriter.Close()
}

func writeZipEntry(zipWriter *zip.Writer, name string, mode os.FileMode, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, bytes.NewReader(data))
	return err
}

func fail(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}

func failf(format string, args ...any) {
	fail(fmt.Sprintf(format, args...))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
