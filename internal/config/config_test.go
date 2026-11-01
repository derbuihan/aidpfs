package config

import "testing"

func TestResolve(t *testing.T) {
	for _, tt := range []struct {
		root, rel, want string
		bad             bool
	}{
		{"/Workspace/日本語", "a b/%#", "/Workspace/日本語/a b/%#", false},
		{"/", "", "/", false}, {"/a", "../b", "", true}, {"/a", "x/../b", "", true}, {"/a", "/b", "", true}, {"/a", "x//b", "", true}, {"/a", "a\x00b", "", true},
	} {
		got, err := Resolve(tt.root, tt.rel)
		if (err != nil) != tt.bad || got != tt.want {
			t.Errorf("Resolve(%q,%q) = %q, %v", tt.root, tt.rel, got, err)
		}
	}
}
func TestValidate(t *testing.T) {
	c := Config{Profile: "DEFAULT", Region: "ap-osaka-1", PlatformID: "id", WorkspaceKey: "key", RemotePath: "/Workspace", MountPoint: "/mnt/test"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://example.com", "https://user@example.com", "https://example.com/path", "https://example.com/?secret=x"} {
		c.Endpoint = endpoint
		if c.Validate() == nil {
			t.Errorf("accepted %q", endpoint)
		}
	}
}
