package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

type skillFile struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}
type skillEntry struct {
	URI         string         `json:"uri"`
	Frontmatter map[string]any `json:"frontmatter"`
	Resources   []skillFile    `json:"resources"`
}
type skillListParams struct {
	sdk.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}
type skillGetParams struct {
	sdk.ParamsBase
	URI string `json:"uri"`
}
type skillListResult struct {
	sdk.ResultBase
	sdk.Cacheable
	ResultType string       `json:"resultType"`
	Skills     []skillEntry `json:"skills"`
	NextCursor string       `json:"nextCursor,omitempty"`
}
type skillGetResult struct {
	sdk.ResultBase
	sdk.Cacheable
	ResultType string     `json:"resultType"`
	Skill      skillEntry `json:"skill"`
}

func (h *Handler) prepareSkills() error {
	h.skillFiles = map[string][]byte{}
	namePattern := regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	seen := map[string]bool{}
	for _, skill := range h.opts.Skills {
		if len(skill.Name) > 64 || !namePattern.MatchString(skill.Name) || seen[skill.Name] {
			return fmt.Errorf("mcp: invalid or duplicate skill name %q", skill.Name)
		}
		seen[skill.Name] = true
		front, err := skillFrontmatter(skill.Content)
		if err != nil {
			return fmt.Errorf("skill %s: %w", skill.Name, err)
		}
		if front["name"] != skill.Name || front["description"] != skill.Description || skill.Description == "" {
			return fmt.Errorf("mcp: skill %s frontmatter does not match its name and description", skill.Name)
		}
		root := "skill://vecdex/" + skill.Name + "/"
		entry := skillEntry{URI: root + "SKILL.md", Frontmatter: front, Resources: []skillFile{}}
		files := map[string][]byte{"SKILL.md": []byte(skill.Content)}
		for name, data := range skill.Files {
			if !validSkillPath(name) {
				return fmt.Errorf("mcp: invalid skill file %q", name)
			}
			files[name] = append([]byte(nil), data...)
		}
		if len(files) > 512 {
			return fmt.Errorf("mcp: skill %s has too many files", skill.Name)
		}
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		total := 0
		for _, name := range names {
			data := files[name]
			total += len(data)
			digest := sha256.Sum256(data)
			uri := root + name
			h.skillFiles[uri] = data
			entry.Resources = append(entry.Resources,
				skillFile{URI: uri,
					Digest: "sha256:" + hex.EncodeToString(digest[:]),
					Size:   len(data)})
		}
		if total > 16<<20 {
			return fmt.Errorf("mcp: skill %s exceeds 16 MiB", skill.Name)
		}
		h.skills = append(h.skills, entry)
	}
	sort.Slice(h.skills, func(i, j int) bool { return h.skills[i].URI < h.skills[j].URI })
	return nil
}

func skillFrontmatter(content string) (map[string]any, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, errors.New("unclosed SKILL.md frontmatter")
	}
	var front map[string]any
	if err := yaml.Unmarshal([]byte(content[4:4+end]), &front); err != nil {
		return nil, fmt.Errorf("parse skill frontmatter: %w", err)
	}
	if _, err := json.Marshal(front); err != nil {
		return nil, fmt.Errorf("frontmatter is not JSON-compatible: %w", err)
	}
	return front, nil
}

func (h *Handler) registerSkills(s *sdk.Server) error {
	if err := sdk.AddReceivingCustomMethod(s,
		"skills/list",
		func(_ context.Context,
			_ *sdk.ServerSession,
			p *skillListParams) (*skillListResult,
			error) {
			offset := 0
			if p != nil && p.Cursor != "" {
				raw, err := base64.RawURLEncoding.DecodeString(p.Cursor)
				if err != nil {
					return nil, resourceError(invalid("invalid skills cursor"))
				}
				offset, err = strconv.Atoi(string(raw))
				if err != nil || offset < 0 || offset >= len(h.skills) {
					return nil, resourceError(invalid("invalid skills cursor"))
				}
			}
			res := &skillListResult{ResultType: "complete",
				Cacheable: sdk.Cacheable{TTLMs: 300000,
					CacheScope: "private"},
				Skills: []skillEntry{}}
			for i := offset; i < len(h.skills); i++ {
				res.Skills = append(res.Skills, h.skills[i])
				if i+1 < len(h.skills) {
					res.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(i + 1)))
				} else {
					res.NextCursor = ""
				}
				if !h.fits(res) {
					res.Skills = res.Skills[:len(res.Skills)-1]
					if len(res.Skills) == 0 {
						return nil,
							resourceError(&agentError{Code: "response_too_large",
								Message: "A skill manifest exceeds the response budget.",
								Action:  "Ask the operator to split the skill bundle."})
					}
					res.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(i)))
					break
				}
			}
			return res, nil
		}); err != nil {
		return fmt.Errorf("register skills/list: %w", err)
	}
	err := sdk.AddReceivingCustomMethod(s, "skills/get", h.getSkill)
	if err != nil {
		return fmt.Errorf("register skills/get: %w", err)
	}
	return nil
}

func validSkillPath(name string) bool {
	return name != "" && name != "SKILL.md" && path.Clean(name) == name && !strings.HasPrefix(name, "/") &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\?#%")
}

func (h *Handler) getSkill(_ context.Context,
	_ *sdk.ServerSession,
	p *skillGetParams) (*skillGetResult,
	error) {
	if p == nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "uri is required"}
	}
	for _, skill := range h.skills {
		if skill.URI == p.URI {
			return &skillGetResult{ResultType: "complete",
					Cacheable: sdk.Cacheable{TTLMs: 300000,
						CacheScope: "private"},
					Skill: skill},
				nil
		}
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown skill URI"}
}
