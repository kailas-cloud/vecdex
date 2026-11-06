// Package skills embeds the single source of truth for installable and MCP skills.
package skills

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed bundle/*/SKILL.md
var files embed.FS

// Skill is a portable skill package. Content includes the manifest frontmatter.
type Skill struct {
	Name, Description, Content string
	Files                      map[string][]byte
}

// All returns independent copies of the four shipped packages in stable order.
func All() []Skill {
	names := []string{"memory-use", "memory-maintenance", "memory-handoff", "memory-administration"}
	result := make([]Skill, 0, len(names))
	for _, name := range names {
		data, err := files.ReadFile("bundle/" + name + "/SKILL.md")
		if err != nil {
			panic(fmt.Sprintf("embedded skill %s: %v", name, err))
		}
		content := string(data)
		var description string
		header := strings.SplitN(content, "---", 3)[1]
		for _, line := range strings.Split(header, "\n") {
			if value, ok := strings.CutPrefix(line, "description: "); ok {
				description = value
			}
		}
		result = append(result, Skill{Name: name, Description: description, Content: content})
	}
	return result
}
