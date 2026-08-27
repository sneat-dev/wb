package gitremote

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNormalizesHostedAndExactLocalIdentities(t *testing.T) {
	httpsRemote := mustParse(t, "https://github.com/acme/app.git")
	sshRemote := mustParse(t, "ssh://github.com/acme/app.git")
	sshGitRemote := mustParse(t, "ssh://git@github.com/acme/app.git")
	scpRemote := mustParse(t, "git@github.com:acme/app.git")
	if !httpsRemote.Identity.Equal(sshRemote.Identity) || !httpsRemote.Identity.Equal(sshGitRemote.Identity) || !httpsRemote.Identity.Equal(scpRemote.Identity) {
		t.Fatal("equivalent hosted SSH/HTTPS spellings did not normalize to one identity")
	}
	wrongHost := mustParse(t, "https://evil.example/acme/app.git")
	if httpsRemote.Identity.Equal(wrongHost.Identity) {
		t.Fatal("same owner/repository on a different host compared equal")
	}

	local := filepath.Join(t.TempDir(), "remotes", "acme", "app.git")
	localPath := mustParse(t, local)
	localFileURL := mustParse(t, "file://"+local)
	if !localPath.Identity.Equal(localFileURL.Identity) {
		t.Fatal("equivalent exact local path spellings did not compare equal")
	}
	otherLocal := mustParse(t, filepath.Join(t.TempDir(), "remotes", "acme", "app.git"))
	if localPath.Identity.Equal(otherLocal.Identity) {
		t.Fatal("different local paths with the same owner/repository compared equal")
	}
}

func TestParseRejectsUnsafeRemoteWithoutEchoingIt(t *testing.T) {
	remotes := []string{
		"ssh://other@github.com/acme/app.git",
		"ssh://git:top-secret@github.com/acme/app.git",
		"https://user:top-secret@github.com/acme/app.git",
		"https://github.com/acme/app.git?token=top-secret",
		"https://github.com/acme/app.git#top-secret",
		"https://github.com/acme%2fapp.git",
		"https://github.com/prefix/acme/app.git",
		"https://github.com/acme/../app.git",
		"relative/acme/app.git",
		"-option-like",
	}
	for _, raw := range remotes {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatal("unsafe remote was accepted")
			} else if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "top-secret") {
				t.Fatalf("error exposed remote data: %v", err)
			}
		})
	}
}

func mustParse(t *testing.T, raw string) Remote {
	t.Helper()
	remote, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return remote
}
