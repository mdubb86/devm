package render

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderGitCredentials_EmptyBindings(t *testing.T) {
	creds, gitconfig := RenderGitCredentials(nil)
	assert.Equal(t, "", creds, "no bindings → empty credentials file")
	assert.Equal(t, fixedGitconfigBody(), gitconfig, "gitconfig body is fixed regardless of bindings")
}

func TestRenderGitCredentials_SinglePrimary(t *testing.T) {
	creds, _ := RenderGitCredentials([]RepoBinding{
		{URL: "https://github.com/mdubb86/sewtrue.git", Secret: "gh_token"},
	})
	assert.Equal(t,
		"https://x-access-token:__DEVM_SECRET_gh_token__@github.com/mdubb86/sewtrue.git\n",
		creds)
}

func TestRenderGitCredentials_MultiRepoMultiHost(t *testing.T) {
	creds, gitconfig := RenderGitCredentials([]RepoBinding{
		{URL: "https://github.com/mdubb86/sewtrue.git", Secret: "gh_workspace"},
		{URL: "https://gitlab.com/team/mono.git", Secret: "gitlab_ci"},
		{URL: "https://dev.azure.com/org/proj/_git/repo", Secret: "azdo_pat"},
	})
	assert.Equal(t,
		"https://x-access-token:__DEVM_SECRET_gh_workspace__@github.com/mdubb86/sewtrue.git\n"+
			"https://x-access-token:__DEVM_SECRET_gitlab_ci__@gitlab.com/team/mono.git\n"+
			"https://x-access-token:__DEVM_SECRET_azdo_pat__@dev.azure.com/org/proj/_git/repo\n",
		creds)
	assert.Contains(t, gitconfig, "useHttpPath = true",
		"multi-host gitconfig must enable path-scoped matching")
}

func TestRenderGitCredentials_SameHostDifferentSecrets(t *testing.T) {
	creds, gitconfig := RenderGitCredentials([]RepoBinding{
		{URL: "https://github.com/mdubb86/sewtrue.git", Secret: "gh_workspace"},
		{URL: "https://github.com/mdubb86/some-sdk.git", Secret: "gh_sdk_ro"},
	})
	// Two lines; both include full path so credential.useHttpPath=true routes them.
	assert.Equal(t,
		"https://x-access-token:__DEVM_SECRET_gh_workspace__@github.com/mdubb86/sewtrue.git\n"+
			"https://x-access-token:__DEVM_SECRET_gh_sdk_ro__@github.com/mdubb86/some-sdk.git\n",
		creds)
	assert.Contains(t, gitconfig, "useHttpPath = true")
}

func TestRenderGitCredentials_GitconfigIsFixed(t *testing.T) {
	// Fixed body regardless of bindings — see spec §Guest file layout.
	_, gitconfig := RenderGitCredentials(nil)
	assert.Equal(t, "[credential]\n    helper = store\n    useHttpPath = true\n", gitconfig)
}

func TestRenderGitCredentials_Deterministic(t *testing.T) {
	// Same input → same bytes across calls (no map iteration order leaks).
	bindings := []RepoBinding{
		{URL: "https://github.com/a/one.git", Secret: "s1"},
		{URL: "https://github.com/b/two.git", Secret: "s2"},
	}
	c1, _ := RenderGitCredentials(bindings)
	c2, _ := RenderGitCredentials(bindings)
	assert.Equal(t, c1, c2)
}

func TestRenderGitCredentials_SecretNameWithUnderscoresAndDigits(t *testing.T) {
	// TokenFor emits __DEVM_SECRET_<name>__ verbatim; the render must too.
	creds, _ := RenderGitCredentials([]RepoBinding{
		{URL: "https://gitlab.example.com/team/x.git", Secret: "gl_token_v2"},
	})
	assert.Contains(t, creds, "__DEVM_SECRET_gl_token_v2__")
}
