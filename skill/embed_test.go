package skill

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedSkill(t *testing.T) {
	data, err := Files.ReadFile("jenkins-cli/SKILL.md")
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	content := string(data)
	require.True(t, strings.HasPrefix(content, "---\n"), "SKILL.md should open with a frontmatter delimiter")

	// the closing delimiter must follow the opening one, with the required fields in between
	rest := strings.TrimPrefix(content, "---\n")
	closeIdx := strings.Index(rest, "\n---")
	require.GreaterOrEqual(t, closeIdx, 0, "SKILL.md frontmatter should have a closing --- delimiter")
	frontmatter := rest[:closeIdx]
	assert.Contains(t, frontmatter, "name: jenkins-cli", "frontmatter should declare the skill name")
	assert.Contains(t, frontmatter, "description:", "frontmatter should declare a description")
}
