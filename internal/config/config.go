package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

type Config struct {
	Profile, Region, Endpoint, PlatformID, WorkspaceKey, RemotePath, MountPoint string
}

func (c Config) Validate() error {
	for _, item := range []struct{ name, value string }{
		{"profile", c.Profile}, {"region", c.Region}, {"platform-id", c.PlatformID}, {"workspace-key", c.WorkspaceKey}, {"mount-point", c.MountPoint},
	} {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("%s is required", item.name)
		}
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("endpoint must be an HTTPS origin without credentials, path, query, or fragment")
		}
	}
	if !strings.HasPrefix(c.RemotePath, "/") || path.Clean(c.RemotePath) != c.RemotePath || strings.ContainsRune(c.RemotePath, '\x00') {
		return errors.New("remote-path must be a clean absolute path")
	}
	if c.RemotePath == "/" {
		return errors.New("remote-path / is not supported; select a workspace subtree such as /Workspace")
	}
	if !filepath.IsAbs(c.MountPoint) || filepath.Clean(c.MountPoint) != c.MountPoint {
		return errors.New("mount-point must be a clean absolute path")
	}
	return nil
}

// Resolve confines a slash-separated relative path beneath root.
func Resolve(root, rel string) (string, error) {
	if !strings.HasPrefix(root, "/") || path.Clean(root) != root || strings.ContainsRune(root, '\x00') {
		return "", errors.New("invalid root")
	}
	if strings.ContainsRune(rel, '\x00') || strings.HasPrefix(rel, "/") {
		return "", errors.New("invalid relative path")
	}
	if rel != "" {
		for _, part := range strings.Split(rel, "/") {
			if part == "" || part == "." || part == ".." {
				return "", errors.New("invalid relative path")
			}
		}
	}
	return path.Join(root, rel), nil
}
