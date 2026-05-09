package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tidwall/gjson"
)

const defaultAddr = "127.0.0.1:9443"

type Options struct {
	Addr           string
	ConfigFile     string
	ClientCertFile string
	ClientKeyFile  string
	CAFile         string
	ServerName     string
	WorkDir        string
}

type discoveredConfig struct {
	path        string
	packageRoot string
	configDir   string
	caBaseDir   string
	listenAddr  string
	clientCA    string
}

func ResolveOptions(in Options) (Options, error) {
	opts := in
	workDir, err := resolveWorkDir(in.WorkDir)
	if err != nil {
		return opts, err
	}
	opts.WorkDir = workDir

	discovered, err := discoverConfig(opts)
	if err != nil {
		return opts, err
	}
	if discovered.path != "" {
		opts.ConfigFile = discovered.path
	}

	if opts.Addr == "" && discovered.listenAddr != "" {
		opts.Addr = discovered.listenAddr
	}
	if opts.ClientCertFile == "" {
		opts.ClientCertFile = filepath.Join(discoveredRoot(opts, discovered), "conf", "certs", "client.crt")
	}
	if opts.ClientKeyFile == "" {
		opts.ClientKeyFile = filepath.Join(discoveredRoot(opts, discovered), "conf", "certs", "client.key")
	}
	if opts.CAFile == "" {
		if discovered.clientCA != "" {
			opts.CAFile = resolveRelativePath(discovered.caBaseDir, discovered.clientCA)
		} else {
			opts.CAFile = filepath.Join(discovered.packageRoot, "conf", "certs", "client-ca.crt")
		}
	}

	if opts.Addr == "" {
		opts.Addr = defaultAddr
	}

	return opts, nil
}

func discoverConfig(opts Options) (discoveredConfig, error) {
	if opts.ConfigFile != "" {
		cfgPath := opts.ConfigFile
		if !filepath.IsAbs(cfgPath) {
			cfgPath = filepath.Join(opts.WorkDir, cfgPath)
		}
		cfgPath = filepath.Clean(cfgPath)
		listenAddr, clientCA, err := loadConfig(cfgPath)
		if err != nil {
			return discoveredConfig{}, err
		}

		return discoveredConfig{
			path:        cfgPath,
			packageRoot: defaultPackageRoot(opts.WorkDir),
			configDir:   filepath.Dir(cfgPath),
			caBaseDir:   filepath.Dir(cfgPath),
			listenAddr:  listenAddr,
			clientCA:    clientCA,
		}, nil
	}

	for _, candidate := range discoverConfigCandidates(opts.WorkDir) {
		listenAddr, clientCA, err := loadConfig(candidate.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return discoveredConfig{}, err
		}

		return discoveredConfig{
			path:        candidate.path,
			packageRoot: candidate.root,
			configDir:   filepath.Dir(candidate.path),
			caBaseDir:   candidate.root,
			listenAddr:  listenAddr,
			clientCA:    clientCA,
		}, nil
	}

	return discoveredConfig{
		packageRoot: opts.WorkDir,
		configDir:   opts.WorkDir,
		caBaseDir:   opts.WorkDir,
	}, nil
}

func discoverConfigCandidates(workDir string) []struct {
	path string
	root string
} {
	candidates := []struct {
		path string
		root string
	}{
		{
			path: filepath.Join(workDir, "data", "state", "config", "config.json"),
			root: workDir,
		},
		{
			path: filepath.Join(workDir, "conf", "config.json"),
			root: workDir,
		},
	}
	if filepath.Base(workDir) == "bin" {
		candidates = append(candidates, struct {
			path string
			root string
		}{
			path: filepath.Join(filepath.Dir(workDir), "data", "state", "config", "config.json"),
			root: filepath.Dir(workDir),
		})
		candidates = append(candidates, struct {
			path string
			root string
		}{
			path: filepath.Join(filepath.Dir(workDir), "conf", "config.json"),
			root: filepath.Dir(workDir),
		})
	}

	if fp := os.Getenv("FILESTASH_PATH"); fp != "" {
		candidates = append(candidates, struct {
			path string
			root string
		}{
			path: filepath.Join(fp, "state", "config", "config.json"),
			root: defaultPackageRoot(workDir),
		})
	}

	return candidates
}

func loadConfig(path string) (string, string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if !json.Valid(content) {
		return "", "", fmt.Errorf("invalid sidecar config JSON: %s", path)
	}

	listenAddr := gjson.GetBytes(content, "features.sidecar_grpc.listen_addr").String()
	clientCA := gjson.GetBytes(content, "features.sidecar_grpc.tls.client_ca_file").String()

	return listenAddr, clientCA, nil
}

func resolveRelativePath(baseDir, candidate string) string {
	if candidate == "" {
		return candidate
	}
	if filepath.IsAbs(candidate) {
		return candidate
	}
	return filepath.Join(baseDir, candidate)
}

func resolveWorkDir(workDir string) (string, error) {
	if workDir == "" {
		return os.Getwd()
	}
	if filepath.IsAbs(workDir) {
		return workDir, nil
	}
	wd, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve work dir: %w", err)
	}
	return wd, nil
}

func discoveredRoot(opts Options, cfg discoveredConfig) string {
	if cfg.packageRoot != "" {
		return cfg.packageRoot
	}
	return opts.WorkDir
}

func defaultPackageRoot(workDir string) string {
	if filepath.Base(workDir) == "bin" {
		return filepath.Dir(workDir)
	}
	return workDir
}
