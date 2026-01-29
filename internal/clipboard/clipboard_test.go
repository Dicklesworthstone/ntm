package clipboard

import (
	"fmt"
	"os"
	"testing"
)

func newStubDetector(goos string, env map[string]string, bins map[string]bool, version string) detector {
	getenv := func(key string) string {
		if env == nil {
			return ""
		}
		return env[key]
	}
	lookPath := func(bin string) error {
		if bins != nil && bins[bin] {
			return nil
		}
		return fmt.Errorf("not found")
	}
	readFile := func(path string) ([]byte, error) {
		if path == "/proc/version" && version != "" {
			return []byte(version), nil
		}
		return nil, os.ErrNotExist
	}
	return detector{
		goos:     goos,
		getenv:   getenv,
		lookPath: lookPath,
		readFile: readFile,
	}
}

func TestChooseBackendDarwinPbcopy(t *testing.T) {
	det := newStubDetector("darwin", nil, map[string]bool{"pbcopy": true, "pbpaste": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "pbcopy" {
		t.Fatalf("expected pbcopy backend, got %s", b.name())
	}
}

func TestChooseBackendWayland(t *testing.T) {
	det := newStubDetector("linux", map[string]string{"XDG_SESSION_TYPE": "wayland"}, map[string]bool{"wl-copy": true, "wl-paste": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "wl-copy" {
		t.Fatalf("expected wl-copy backend, got %s", b.name())
	}
}

func TestChooseBackendXclip(t *testing.T) {
	det := newStubDetector("linux", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xclip": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "xclip" {
		t.Fatalf("expected xclip backend, got %s", b.name())
	}
}

func TestChooseBackendXselFallback(t *testing.T) {
	det := newStubDetector("linux", map[string]string{"DISPLAY": ":0"}, map[string]bool{"xsel": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "xsel" {
		t.Fatalf("expected xsel backend, got %s", b.name())
	}
}

func TestChooseBackendWSL(t *testing.T) {
	det := newStubDetector("linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, map[string]bool{"clip.exe": true, "powershell.exe": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "wsl-clipboard" {
		t.Fatalf("expected wsl-clipboard backend, got %s", b.name())
	}
}

func TestChooseBackendTmuxFallback(t *testing.T) {
	det := newStubDetector("linux", map[string]string{"TMUX": "1"}, map[string]bool{"tmux": true}, "")
	b, err := chooseBackend(det)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.name() != "tmux-buffer" {
		t.Fatalf("expected tmux-buffer backend, got %s", b.name())
	}
}

func TestChooseBackendNoTools(t *testing.T) {
	det := newStubDetector("linux", nil, nil, "")
	if _, err := chooseBackend(det); err == nil {
		t.Fatalf("expected error when no clipboard tools found")
	}
}

func TestIsWSL(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		version string
		want    bool
	}{
		{"WSL_DISTRO_NAME set", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, "", true},
		{"WSL_INTEROP set", map[string]string{"WSL_INTEROP": "/run/wsl"}, "", true},
		{"microsoft in /proc/version", nil, "Linux 5.15.0-microsoft-standard-WSL2", true},
		{"Microsoft uppercase", nil, "Linux MICROSOFT something", true},
		{"not WSL", nil, "Linux 6.1.0-generic", false},
		{"no env no version", nil, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			det := newStubDetector("linux", tc.env, nil, tc.version)
			got := isWSL(det)
			if got != tc.want {
				t.Errorf("isWSL() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsWayland(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"XDG_SESSION_TYPE=wayland", map[string]string{"XDG_SESSION_TYPE": "wayland"}, true},
		{"XDG_SESSION_TYPE=Wayland", map[string]string{"XDG_SESSION_TYPE": "Wayland"}, true},
		{"XDG_SESSION_TYPE=x11", map[string]string{"XDG_SESSION_TYPE": "x11"}, false},
		{"WAYLAND_DISPLAY set", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"no wayland env", map[string]string{"DISPLAY": ":0"}, false},
		{"empty env", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			det := newStubDetector("linux", tc.env, nil, "")
			got := isWayland(det)
			if got != tc.want {
				t.Errorf("isWayland() = %v, want %v", got, tc.want)
			}
		})
	}
}
