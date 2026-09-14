package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// leaseLeaf acquires a worktree and returns the last segment of its path, the
// only part of the layout tooling can read from the working directory alone.
func leaseLeaf(t *testing.T, repoDir, homeDir string, extraEnv []string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, extraEnv, append([]string{"get", "--lease"}, args...)...)
	if code != 0 {
		t.Fatalf("get --lease %v failed (code %d): %s", args, code, stderr)
	}
	return filepath.Base(strings.TrimSpace(stdout))
}

func TestGetWithoutUniqueLeafKeepsRepositoryName(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	if got, want := leaseLeaf(t, repoDir, homeDir, nil), filepath.Base(repoDir); got != want {
		t.Errorf("worktree leaf = %q, want the unchanged default %q", got, want)
	}
}

func TestGetUniqueLeafFlagNamesWorktreeAfterItsSlot(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	if got, want := leaseLeaf(t, repoDir, homeDir, nil, "--unique-leaf"), filepath.Base(repoDir)+"-1"; got != want {
		t.Errorf("worktree leaf = %q, want %q", got, want)
	}
}

func TestGetUniqueLeafFlagGivesEachSlotADistinctLeaf(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	first := leaseLeaf(t, repoDir, homeDir, nil, "--unique-leaf")
	second := leaseLeaf(t, repoDir, homeDir, nil, "--unique-leaf")

	if first == second {
		t.Errorf("expected distinct leaf names, got %q for both slots", first)
	}
}

func TestGetUsesConfiguredUniqueLeaf(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)
	writeRepoConfig(t, repoDir, "unique_leaf = true\n")

	if got, want := leaseLeaf(t, repoDir, homeDir, nil), filepath.Base(repoDir)+"-1"; got != want {
		t.Errorf("worktree leaf = %q, want %q", got, want)
	}
}

func TestGetUniqueLeafEnvVarEnablesWithoutConfig(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	got := leaseLeaf(t, repoDir, homeDir, []string{"TREEHOUSE_UNIQUE_LEAF=1"})
	if want := filepath.Base(repoDir) + "-1"; got != want {
		t.Errorf("worktree leaf = %q, want %q", got, want)
	}
}

func TestGetUniqueLeafPrecedenceFlagOverEnvOverConfig(t *testing.T) {
	t.Run("env overrides config", func(t *testing.T) {
		repoDir, homeDir := setupTestRepo(t)
		writeRepoConfig(t, repoDir, "unique_leaf = true\n")

		got := leaseLeaf(t, repoDir, homeDir, []string{"TREEHOUSE_UNIQUE_LEAF=0"})
		if want := filepath.Base(repoDir); got != want {
			t.Errorf("worktree leaf = %q, want the env var to win with %q", got, want)
		}
	})

	t.Run("flag overrides env and config", func(t *testing.T) {
		repoDir, homeDir := setupTestRepo(t)
		writeRepoConfig(t, repoDir, "unique_leaf = false\n")

		got := leaseLeaf(t, repoDir, homeDir, []string{"TREEHOUSE_UNIQUE_LEAF=0"}, "--unique-leaf")
		if want := filepath.Base(repoDir) + "-1"; got != want {
			t.Errorf("worktree leaf = %q, want the flag to win with %q", got, want)
		}
	})

	t.Run("flag turns the option off for one acquisition", func(t *testing.T) {
		repoDir, homeDir := setupTestRepo(t)
		writeRepoConfig(t, repoDir, "unique_leaf = true\n")

		got := leaseLeaf(t, repoDir, homeDir, []string{"TREEHOUSE_UNIQUE_LEAF=1"}, "--unique-leaf=false")
		if want := filepath.Base(repoDir); got != want {
			t.Errorf("worktree leaf = %q, want %q", got, want)
		}
	})
}

func TestGetUniqueLeafReusesExistingWorktreeWithoutMovingIt(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease failed (code %d): %s", code, stderr)
	}
	shared := strings.TrimSpace(stdout)

	if _, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "return", shared); code != 0 {
		t.Fatalf("return failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code = runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--unique-leaf")
	if code != 0 {
		t.Fatalf("get --lease --unique-leaf failed (code %d): %s", code, stderr)
	}
	if recycled := strings.TrimSpace(stdout); recycled != shared {
		t.Errorf("recycled worktree = %q, want the existing %q left in place", recycled, shared)
	}
}

// TestGetUniqueLeafCombinesWithIncludeFile covers both ways get acquires a
// slot, which share one set of acquire options: a newly created slot gets the
// unique leaf AND is seeded from the --include-file manifest, so neither option
// silently drops the other.
func TestGetUniqueLeafCombinesWithIncludeFile(t *testing.T) {
	setup := func(t *testing.T) (repoDir, homeDir, manifest string) {
		t.Helper()
		repoDir, homeDir = setupTestRepo(t)
		gitCmd(t, repoDir, "config", "core.autocrlf", "false")
		if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("*.seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, repoDir, "add", ".gitignore")
		gitCmd(t, repoDir, "commit", "-m", "ignore seeds")
		gitCmd(t, repoDir, "push", "origin", "main")
		if err := os.WriteFile(filepath.Join(repoDir, "local.seed"), []byte("local\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		manifest = filepath.Join(t.TempDir(), "personal.include")
		if err := os.WriteFile(manifest, []byte("local.seed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return repoDir, homeDir, manifest
	}
	assertSeededUniqueSlot := func(t *testing.T, repoDir, wtPath string) {
		t.Helper()
		if got, want := filepath.Base(wtPath), filepath.Base(repoDir)+"-1"; got != want {
			t.Errorf("worktree leaf = %q, want %q", got, want)
		}
		if got, err := os.ReadFile(filepath.Join(wtPath, "local.seed")); err != nil || string(got) != "local\n" {
			t.Errorf("local.seed = %q, %v; want the --include-file seed", got, err)
		}
	}

	t.Run("lease", func(t *testing.T) {
		repoDir, homeDir, manifest := setup(t)

		stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil,
			"get", "--lease", "--unique-leaf", "--include-file", manifest)
		if code != 0 {
			t.Fatalf("get --lease --unique-leaf --include-file failed (code %d): %s", code, stderr)
		}
		assertSeededUniqueSlot(t, repoDir, strings.TrimSpace(stdout))
	})

	t.Run("subshell", func(t *testing.T) {
		repoDir, homeDir, manifest := setup(t)

		// The seed is cleaned when get returns the slot, so the slot is
		// inspected while the subshell is still running inside it.
		signals := t.TempDir()
		readyFile := filepath.Join(signals, "ready")
		releaseFile := filepath.Join(signals, "release")
		getCmd := exec.Command(treehouseBin, "get", "--unique-leaf", "--include-file", manifest)
		getCmd.Dir = repoDir
		getCmd.Env = buildEnv(homeDir,
			"SHELL="+waitShellBin,
			"TREEHOUSE_TEST_READY="+readyFile,
			"TREEHOUSE_TEST_RELEASE="+releaseFile,
		)
		var getErrBuf bytes.Buffer
		getCmd.Stderr = &getErrBuf
		if err := getCmd.Start(); err != nil {
			t.Fatalf("failed to start get: %v", err)
		}
		t.Cleanup(func() {
			os.WriteFile(releaseFile, nil, 0o644)
			getCmd.Wait()
		})

		var wtPath string
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(readyFile); err == nil && len(b) > 0 {
				wtPath = string(b)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if wtPath == "" {
			t.Fatalf("the get subshell never reported its worktree; get stderr: %s", getErrBuf.String())
		}
		assertSeededUniqueSlot(t, repoDir, wtPath)

		if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := getCmd.Wait(); err != nil {
			t.Fatalf("get exited with an error: %v\nstderr: %s", err, getErrBuf.String())
		}
	})
}

func TestGetUniqueLeafKeepsPathOnlyStdout(t *testing.T) {
	repoDir, homeDir := setupTestRepo(t)

	stdout, stderr, code := runTreehouse(t, repoDir, homeDir, nil, "get", "--lease", "--unique-leaf")
	if code != 0 {
		t.Fatalf("get --lease --unique-leaf failed (code %d): %s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 || !filepath.IsAbs(lines[0]) {
		t.Errorf("expected a single absolute path on stdout, got %q", stdout)
	}
}
