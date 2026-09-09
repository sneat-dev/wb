package session

import "testing"

func TestProcessEvidenceMatchesCodexAppServerByExecutableAndRole(t *testing.T) {
	tests := []struct {
		name     string
		evidence ProcessEvidence
		want     bool
	}{
		{
			name: "nested MCP script path does not change executable identity",
			evidence: ProcessEvidence{
				Executable: "/Applications/ChatGPT.app/Contents/Resources/codex",
				Args: []string{
					"/Applications/ChatGPT.app/Contents/Resources/codex",
					"-c", "features.code_mode_host=true", "app-server",
					"-c", `mcp_servers.codex_app={"command"="/Applications/ChatGPT.app/Contents/Resources/plugins/openai-bundled/plugins/codex-app-tools/scripts/launch_codex_app_tools_mcp"}`,
				},
			},
			want: true,
		},
		{
			name: "shell executable is refused",
			evidence: ProcessEvidence{
				Executable: "/bin/zsh",
				Args:       []string{"zsh", "-c", "wb session register --pid $PPID --runtime codex"},
			},
			want: false,
		},
		{
			name: "codex without app-server role is refused",
			evidence: ProcessEvidence{
				Executable: "/usr/local/bin/codex",
				Args:       []string{"codex", "exec", "--help"},
			},
			want: false,
		},
		{
			name: "nested script token is not a role",
			evidence: ProcessEvidence{
				Executable: "/usr/local/bin/codex",
				Args:       []string{"codex", "-c", `mcp.command=/tmp/app-server-script`},
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := processEvidenceMatchesRuntime(test.evidence, "codex"); got != test.want {
				t.Fatalf("processEvidenceMatchesRuntime() = %t, want %t", got, test.want)
			}
		})
	}
}
