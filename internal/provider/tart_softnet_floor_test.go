package provider

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/ysya/runscaler/internal/config"
)

func TestNewTartProvider_NetSoftnetFromScaleSetConfig(t *testing.T) {
	ss := config.ScaleSetConfig{
		RunnerImage: "macos-base:latest",
		MaxRunners:  1,
		Tart:        config.TartConfig{RunnerDir: "/Users/admin/actions-runner", NetSoftnet: true},
	}
	p := NewTartProviderWithCoordinator(ss, slog.New(slog.DiscardHandler), NewTartHostCoordinator(2), TartGuestMacOS)
	want := []string{"run", "runner-abc", "--no-graphics", "--net-softnet"}
	if got := p.runArgs("runner-abc"); !slices.Equal(got, want) {
		t.Errorf("runArgs = %q, want %q", got, want)
	}
}

func TestTartProvider_RunArgsWithoutSoftnetKeepNAT(t *testing.T) {
	p := newTestTartProvider(&mockCommandRunner{})
	for _, a := range p.runArgs("runner-abc") {
		if strings.HasPrefix(a, "--net-") {
			t.Errorf("default runArgs carry a network flag %q, want Tart's NAT", a)
		}
	}
}

func TestNewTartProvider_CloneMinFreeGB(t *testing.T) {
	ss := config.ScaleSetConfig{
		RunnerImage: "macos-base:latest",
		MaxRunners:  1,
		Tart:        config.TartConfig{CloneMinFreeGB: 170},
	}
	p := NewTartProviderWithCoordinator(ss, slog.New(slog.DiscardHandler), NewTartHostCoordinator(2), TartGuestMacOS)
	if want := uint64(170) << 30; p.cloneMinFree != want {
		t.Errorf("cloneMinFree = %d, want %d", p.cloneMinFree, want)
	}
}

func cloneCalls(cmd *mockCommandRunner) int {
	n := 0
	for _, c := range cmd.getCalls() {
		if c.name == "tart" && len(c.args) > 0 && c.args[0] == "clone" {
			n++
		}
	}
	return n
}

func TestTartProvider_CloneFloor(t *testing.T) {
	const floor = uint64(170) << 30
	cases := []struct {
		name      string
		free      uint64
		readErr   error
		wantClone bool
		wantErr   string
	}{
		{name: "above floor clones", free: floor + 1, wantClone: true},
		{name: "at floor clones", free: floor, wantClone: true},
		{name: "below floor refuses", free: floor - 1, wantErr: "below the floor"},
		{name: "unreadable refuses", readErr: errors.New("statfs: boom"), wantErr: "cannot read free space"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &mockCommandRunner{results: map[string]cmdResult{
				"tart clone": {}, "tart run": {}, "tart exec": {},
			}}
			p := newTestTartProvider(cmd)
			p.home = "/Volumes/VMs/tart"
			p.cloneMinFree = floor
			var readPath string
			p.freeBytes = func(path string) (uint64, error) {
				readPath = path
				return tc.free, tc.readErr
			}
			_, err := p.StartInstance(context.Background(), "runner-abc", "jit")
			if readPath != "/Volumes/VMs/tart" {
				t.Errorf("free space read on %q, want TART_HOME", readPath)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("StartInstance() error = %v, want it to contain %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("StartInstance() error: %v", err)
			}
			if got := cloneCalls(cmd) > 0; got != tc.wantClone {
				t.Errorf("clone ran = %v, want %v", got, tc.wantClone)
			}
			// A refusal must release the host permit.
			if tc.wantErr != "" {
				// macOSPermits holds the free permits: all must be back.
				if free, all := len(p.coordinator.macOSPermits), cap(p.coordinator.macOSPermits); free != all {
					t.Errorf("%d of %d macOS permits still held after a refusal", all-free, all)
				}
			}
		})
	}
}

func TestTartProvider_NoFloorNeverReadsDisk(t *testing.T) {
	cmd := &mockCommandRunner{results: map[string]cmdResult{"tart clone": {}, "tart run": {}, "tart exec": {}}}
	p := newTestTartProvider(cmd)
	p.freeBytes = func(string) (uint64, error) {
		t.Fatal("free space read with no floor set")
		return 0, nil
	}
	if _, err := p.StartInstance(context.Background(), "runner-abc", "jit"); err != nil {
		t.Fatalf("StartInstance() error: %v", err)
	}
}

func TestStatfsFreeBytesReadsARealFilesystem(t *testing.T) {
	free, err := statfsFreeBytes(t.TempDir())
	if err != nil || free == 0 {
		t.Fatalf("statfsFreeBytes(tempdir) = %d, %v; want > 0, nil", free, err)
	}
}
